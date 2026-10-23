package push

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// VAPID 是 hub 的推送签名密钥（RFC 8292），P-256。私钥明文存库，是设计 5.3 的例外；它不经任何接口返回。
//
// 私钥只以指针 priv 存着，不另存字节切片：VAPID 按值嵌在别的类型的未导出字段里时，fmt 不调 Format，
// 而是反射打印各字段，切片会原样打出，深层指针只打地址。所以任何格式化路径都打不出私钥标量。
type VAPID struct {
	priv *ecdsa.PrivateKey
	pub  []byte // 65 字节未压缩公钥点
}

// jwtLifetime 是 JWT 的有效期。RFC 8292 第 2 节要求 exp 不超过请求时刻 24 小时。
const jwtLifetime = 12 * time.Hour

// newVAPID 存下私钥与公钥编码。Go 1.25 起 (*ecdsa.PrivateKey).Bytes 与 (*ecdsa.PublicKey).Bytes 会返回错误，
// 在构造时各调一次：公钥编码存下，私钥只验证能编码（不存字节，见 VAPID 的注释），Raw 就不会再出错。
func newVAPID(priv *ecdsa.PrivateKey) (*VAPID, error) {
	if _, err := priv.Bytes(); err != nil {
		return nil, fmt.Errorf("VAPID 私钥编码: %w", err)
	}
	pub, err := priv.PublicKey.Bytes()
	if err != nil {
		return nil, fmt.Errorf("VAPID 公钥编码: %w", err)
	}
	return &VAPID{priv: priv, pub: pub}, nil
}

// GenerateVAPID 生成新密钥。
func GenerateVAPID() (*VAPID, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return newVAPID(priv)
}

// ParseVAPID 从 32 字节私钥标量恢复密钥。零与不小于曲线阶的标量都拒绝。
func ParseVAPID(raw []byte) (*VAPID, error) {
	if len(raw) != 32 {
		return nil, fmt.Errorf("VAPID 私钥应为 32 字节，得到 %d", len(raw))
	}
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		return nil, fmt.Errorf("VAPID 私钥: %w", err)
	}
	return newVAPID(priv)
}

// Raw 返回 32 字节私钥标量（拷贝），存库用。每次现编码：(*ecdsa.PrivateKey).Bytes 返回的是标准库缓存里的切片，必须拷贝。
func (v *VAPID) Raw() []byte {
	raw, err := v.priv.Bytes()
	if err != nil {
		panic("push: VAPID 私钥编码失败") // newVAPID 已验证过能编码，走不到这里
	}
	return bytes.Clone(raw)
}

// PublicKey 返回 65 字节未压缩公钥点（拷贝）。
func (v *VAPID) PublicKey() []byte { return bytes.Clone(v.pub) }

// PublicKeyB64 是给网页 applicationServerKey 用的 base64url（无补位），也是 Authorization 头里的 k。
func (v *VAPID) PublicKeyB64() string { return base64.RawURLEncoding.EncodeToString(v.pub) }

// Format 让任何格式动词打印 VAPID 都只显示公钥，私钥不会顺手进日志。值接收者，指针与值都适用。
func (v VAPID) Format(f fmt.State, _ rune) {
	fmt.Fprintf(f, "VAPID{k=%s}", base64.RawURLEncoding.EncodeToString(v.pub))
}

// jwtHeader 是 RFC 8292 图 2 的头，编码后固定为 eyJ0eXAiOiJKV1QiLCJhbGciOiJFUzI1NiJ9。
const jwtHeader = `{"typ":"JWT","alg":"ES256"}`

// vapidClaims 的字段顺序即 JSON 里的顺序：aud、exp、sub（与 RFC 8292 图 2 一致）。
type vapidClaims struct {
	Aud string `json:"aud"`
	Exp int64  `json:"exp"`
	Sub string `json:"sub"`
}

// Authorization 生成 `vapid t=<JWT>, k=<公钥>`（RFC 8292 第 3 节）。
// audience 是推送端点 origin 的规范串行化（https://host[:port]，小写主机、省掉默认端口 443、不带路径），
// 用 endpointOrigin 从端点得到；subject 是 hub 的联系地址，只收 mailto:地址 或 https:// 地址；exp 取 now + 12 小时。
// 签名是 ES256，JWS 形式的 r‖s 各 32 字节（不是 ASN.1 DER）；各段都是无补位的 base64url。
func (v *VAPID) Authorization(audience, subject string, now time.Time) (string, error) {
	return v.header(audience, subject, jwtExp(now))
}

// jwtExp 是墙钟时刻 now 签发的 JWT 的 exp（Unix 秒）。
func jwtExp(now time.Time) int64 { return now.Add(jwtLifetime).Unix() }

// header 用给定的 exp（Unix 秒）签发 Authorization 头。Client 的令牌缓存要记下 exp 判断时效，所以由它算好传进来。
func (v *VAPID) header(audience, subject string, exp int64) (string, error) {
	if err := checkAudience(audience); err != nil {
		return "", err
	}
	if !validSubject(subject) {
		return "", fmt.Errorf("VAPID subject 必须是 mailto:地址 或 https:// 地址，得到 %q", subject)
	}
	claims, err := json.Marshal(vapidClaims{Aud: audience, Exp: exp, Sub: subject})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	input := enc.EncodeToString([]byte(jwtHeader)) + "." + enc.EncodeToString(claims)
	h := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, v.priv, h[:])
	if err != nil {
		return "", fmt.Errorf("VAPID 签名: %w", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + input + "." + enc.EncodeToString(sig) + ", k=" + v.PublicKeyB64(), nil
}

// checkAudience 要求 audience 正好是某个推送端点 origin 的规范串行化（RFC 8292 第 2 节、RFC 6454 第 6 节）。
func checkAudience(audience string) error {
	u, err := url.Parse(audience)
	if err == nil && u.Path == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" {
		if o, err := originOf(u); err == nil && o == audience {
			return nil
		}
	}
	return fmt.Errorf("audience 必须是推送端点的 origin（https://host[:port]，小写主机、不带默认端口与路径），得到 %q", audience)
}

// endpointOrigin 取推送端点的 origin，作 VAPID 的 aud。
func endpointOrigin(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", errors.New("推送端点不是合法 URL")
	}
	return originOf(u)
}

// originOf 串行化 https URL 的 origin：scheme://host[:port]，主机小写，默认端口 443 省掉。
func originOf(u *url.URL) (string, error) {
	if u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", errors.New("推送端点必须是带主机的 https 地址")
	}
	host := strings.ToLower(u.Host)
	if p := u.Port(); p == "" || p == "443" {
		host = strings.TrimSuffix(strings.TrimSuffix(host, ":443"), ":")
	}
	return "https://" + host, nil
}

// validSubject 只收 mailto:local@domain 或带主机的 https:// 地址，且不含空白与控制字符。
// iOS 实验第 4.7 节：Apple 对不规范的 subject 回 BadJwtToken。
func validSubject(s string) bool {
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	if addr, ok := strings.CutPrefix(s, "mailto:"); ok {
		at := strings.LastIndexByte(addr, '@')
		return at > 0 && at < len(addr)-1
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}
