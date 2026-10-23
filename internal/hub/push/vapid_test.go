package push

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestVAPIDKeyRoundTrip(t *testing.T) {
	v, err := GenerateVAPID()
	if err != nil {
		t.Fatal(err)
	}
	raw := v.Raw()
	if len(raw) != 32 {
		t.Fatalf("私钥 %d 字节", len(raw))
	}
	v2, err := ParseVAPID(raw)
	if err != nil || v2.PublicKeyB64() != v.PublicKeyB64() {
		t.Fatalf("解析回来的公钥不同: %v", err)
	}
	pub := v.PublicKey()
	if len(pub) != 65 || pub[0] != 4 || strings.ContainsAny(v.PublicKeyB64(), "+/=") {
		t.Fatalf("公钥形状: %d %x %s", len(pub), pub[0], v.PublicKeyB64())
	}
	if _, err := ParseVAPID(raw[:31]); err == nil {
		t.Fatal("错长私钥应报错")
	}
}

func TestVAPIDAuthorization(t *testing.T) {
	v, _ := GenerateVAPID()
	now := time.Unix(1_800_000_000, 0)
	hdr, err := v.Authorization("https://web.push.apple.com", "https://hub.example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hdr, "vapid t=") || !strings.Contains(hdr, ", k="+v.PublicKeyB64()) {
		t.Fatalf("头格式: %s", hdr)
	}
	jwt := strings.TrimPrefix(strings.SplitN(hdr, ",", 2)[0], "vapid t=")
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 || parts[0] != "eyJ0eXAiOiJKV1QiLCJhbGciOiJFUzI1NiJ9" {
		t.Fatalf("JWT 头: %v", parts)
	}
	claimsB, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(claimsB, &claims); err != nil || claims.Aud != "https://web.push.apple.com" || claims.Sub != "https://hub.example.com" || claims.Exp != now.Add(12*time.Hour).Unix() {
		t.Fatalf("claims: %s %v", claimsB, err)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if len(sig) != 64 {
		t.Fatalf("签名应为 r‖s 64 字节，得到 %d", len(sig))
	}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&v.priv.PublicKey, h[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("签名校验失败")
	}
	// RFC 8292 图 2 的 claims 串形：字段顺序 aud、exp、sub
	if !strings.HasPrefix(string(claimsB), `{"aud":"`) || !strings.Contains(string(claimsB), `","exp":`) || !strings.HasSuffix(string(claimsB), `"}`) {
		t.Fatalf("claims 顺序: %s", claimsB)
	}
	if _, err := v.Authorization("push.example.net", "https://hub.example.com", now); err == nil {
		t.Fatal("audience 不是 origin 应报错")
	}
}

// 以下是对照 RFC 8292 逐项核对的补充测试。

// 整个头严格是 `vapid t=<JWT>, k=<公钥>`：各段 base64url 无补位，k 解出 65 字节未压缩点且等于 PublicKey()，
// exp 不超过请求时刻 24 小时，签名是 r‖s 而不是 ASN.1 DER。
func TestVAPIDHeaderShape(t *testing.T) {
	v, err := GenerateVAPID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	hdr, err := v.Authorization("https://push.example.net", "mailto:push@example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`^vapid t=([A-Za-z0-9_-]+)\.([A-Za-z0-9_-]+)\.([A-Za-z0-9_-]+), k=([A-Za-z0-9_-]+)$`).FindStringSubmatch(hdr)
	if m == nil {
		t.Fatalf("头不是 `vapid t=<b64url>.<b64url>.<b64url>, k=<b64url>`: %s", hdr)
	}
	head, _ := base64.RawURLEncoding.DecodeString(m[1])
	if string(head) != `{"typ":"JWT","alg":"ES256"}` {
		t.Fatalf("JWT 头: %s", head)
	}
	k, err := base64.RawURLEncoding.DecodeString(m[4])
	if err != nil || len(k) != 65 || k[0] != 4 || !bytes.Equal(k, v.PublicKey()) {
		t.Fatalf("k 应为 65 字节未压缩公钥: %x %v", k, err)
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(v.priv.Curve, k)
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	cb, _ := base64.RawURLEncoding.DecodeString(m[2])
	if err := json.Unmarshal(cb, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Exp <= now.Unix() || claims.Exp > now.Add(24*time.Hour).Unix() {
		t.Fatalf("exp %d 应在 (now, now+24h] 内", claims.Exp)
	}
	if claims.Sub != "mailto:push@example.com" || claims.Aud != "https://push.example.net" {
		t.Fatalf("claims: %s", cb)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(m[3])
	h := sha256.Sum256([]byte(m[1] + "." + m[2]))
	if len(sig) != 64 || !ecdsa.Verify(pub, h[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("签名不是用 k 对应私钥签的 r‖s")
	}
	if ecdsa.VerifyASN1(pub, h[:], sig) {
		t.Fatal("签名不应是 ASN.1 DER")
	}
}

// aud 必须是推送端点 origin 的串行化：https、小写主机、有端口才带端口、不带路径查询片段与用户信息。
func TestVAPIDAudienceMustBeOrigin(t *testing.T) {
	v, _ := GenerateVAPID()
	now := time.Now()
	for _, aud := range []string{"https://push.example.net", "https://push.example.net:8443", "https://127.0.0.1:61234", "https://[::1]:8443"} {
		if _, err := v.Authorization(aud, "https://hub.example.com", now); err != nil {
			t.Errorf("%q 应接受: %v", aud, err)
		}
	}
	for _, aud := range []string{
		"", "push.example.net", "https://push.example.net/", "https://push.example.net/p/x",
		"https://push.example.net?x=1", "https://push.example.net#f", "https://u@push.example.net",
		"http://push.example.net", "https://", "https://Push.Example.net", "https://push.example.net:443",
	} {
		if _, err := v.Authorization(aud, "https://hub.example.com", now); err == nil {
			t.Errorf("%q 不是规范 origin，应拒绝", aud)
		}
	}
}

// sub 只收 mailto:（带 @ 的地址）或 https:// 源（iOS 实验第 4.7 节：Apple 对不规范 subject 回 BadJwtToken）。
func TestVAPIDSubjectValidation(t *testing.T) {
	v, _ := GenerateVAPID()
	now := time.Now()
	for _, sub := range []string{"mailto:push@example.com", "https://hub.example.com", "https://hub.example.com:8443"} {
		if _, err := v.Authorization("https://push.example.net", sub, now); err != nil {
			t.Errorf("%q 应接受: %v", sub, err)
		}
	}
	for _, sub := range []string{
		"", "hub.example.com", "http://hub.example.com", "https://", "mailto:", "mailto:push",
		"mailto: push@example.com", "mailto:push@example.com ", "https://hub.example.com/a b", "mailto:@example.com", "mailto:push@",
		"https://u:p@hub.example.com",
	} {
		if _, err := v.Authorization("https://push.example.net", sub, now); err == nil {
			t.Errorf("%q 应拒绝", sub)
		}
	}
}

// 从已知私钥恢复出的公钥与 RFC 8291 附录 A 的 as_public 一致（两边都是 P-256 未压缩点）。
func TestParseVAPIDKnownKey(t *testing.T) {
	v, err := ParseVAPID(b64(t, rfcASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(v.PublicKey(), b64(t, rfcASPublic)) || v.PublicKeyB64() != rfcASPublic || !bytes.Equal(v.Raw(), b64(t, rfcASPrivate)) {
		t.Fatalf("公钥 %s", v.PublicKeyB64())
	}
	// 零标量、不小于阶 n 的标量都不是合法私钥
	for name, raw := range map[string][]byte{"零": make([]byte, 32), "全 0xff": bytes.Repeat([]byte{0xff}, 32), "33 字节": make([]byte, 33)} {
		if _, err := ParseVAPID(raw); err == nil {
			t.Errorf("%s: 应拒绝", name)
		}
	}
	// 返回的切片是拷贝：改了不影响密钥
	r := v.Raw()
	r[0] ^= 0xff
	p := v.PublicKey()
	p[1] ^= 0xff
	if !bytes.Equal(v.Raw(), b64(t, rfcASPrivate)) || v.PublicKeyB64() != rfcASPublic {
		t.Fatal("Raw/PublicKey 不应暴露内部切片")
	}
}

// 打印 VAPID 不会把私钥带进日志。
func TestVAPIDFormatHidesPrivateKey(t *testing.T) {
	v, err := ParseVAPID(b64(t, rfcASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	raw := v.Raw()
	for _, s := range []string{fmt.Sprint(v), fmt.Sprintf("%+v", v), fmt.Sprintf("%#v", v), fmt.Sprintf("%v", *v), fmt.Sprintf("%+v", *v), fmt.Sprintf("%#v", *v), fmt.Sprintf("%x", v), fmt.Sprintf("%s", v)} {
		if strings.Contains(s, fmt.Sprint(raw)) || strings.Contains(s, fmt.Sprintf("%x", raw)) || strings.Contains(s, rfcASPrivate) || strings.Contains(s, fmt.Sprintf("%#v", raw)) {
			t.Fatalf("格式化输出带出了私钥: %s", s)
		}
	}
}
