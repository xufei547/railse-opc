package push

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
)

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		t.Fatalf("base64url %q: %v", s, err)
	}
	return b
}

const (
	rfcPlaintext = "V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24"
	rfcASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcASPublic  = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	rfcUAPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcUAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcAuth      = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcSalt      = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcMessage   = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

// RFC 8291 附录 A 的中间值（原文里的空白已去掉）。
const (
	rfcECDHSecret = "kyrL1jIIOHEzg3sM2ZWRHDRB62YACZhhSlknJ672kSs"
	rfcKeyInfo    = "V2ViUHVzaDogaW5mbwAEJXGyvs3942BVGq8e0PTNNmwRzr5VX4m8t7GGpTM5FzFo7OLr4BhZe9MEebhuPI-OztV3ylkYfpJGmQ22ggCLDgT-M_SrDepxkU21WCP3O1SUj0EwbZIHMtu5pZpTKGSCIA5Zent7wmC6HCJ5mFgJkuk5cwAvMBKiiujwa7t45ewP"
	rfcIKM        = "S4lYMb_L0FxCeq0WhDx813KgSYqU26kOyzWUdsXYyrg"
	rfcCEKInfo    = "Q29udGVudC1FbmNvZGluZzogYWVzMTI4Z2NtAA"
	rfcCEK        = "oIhVW04MRdy2XN9CiKLxTg"
	rfcNonceInfo  = "Q29udGVudC1FbmNvZGluZzogbm9uY2UA"
	rfcNonce      = "4h_95klXJ5E_qnoN"
	rfcHeader     = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	rfcPadded     = "V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24C"
)

func TestEncryptMatchesRFC8291Vector(t *testing.T) {
	asPriv, err := ecdh.P256().NewPrivateKey(b64(t, rfcASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(asPriv.PublicKey().Bytes(), b64(t, rfcASPublic)) {
		t.Fatal("as_public 与私钥不对应，向量抄错了")
	}
	sub := Subscription{Endpoint: "https://push.example.net/p/x", P256dh: b64(t, rfcUAPublic), Auth: b64(t, rfcAuth)}
	got, err := encryptWith(b64(t, rfcPlaintext), sub, asPriv, b64(t, rfcSalt))
	if err != nil {
		t.Fatal(err)
	}
	want := b64(t, rfcMessage)
	if len(want) != 144 {
		t.Fatalf("向量长度 %d，应为 144", len(want))
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("密文与 RFC 8291 向量不符\n 得到 %s\n 想要 %s", base64.RawURLEncoding.EncodeToString(got), rfcMessage)
	}
	// 头部布局：盐 16、rs 4（4096）、idlen 1（65）、公钥 65
	if binary.BigEndian.Uint32(got[16:20]) != 4096 || got[20] != 65 || !bytes.Equal(got[21:86], b64(t, rfcASPublic)) {
		t.Fatal("aes128gcm 头部布局不对")
	}
}

// decrypt 是只在测试里用的接收端实现：按 RFC 8291 用浏览器侧私钥解开，验证随机盐与临时密钥的路径。
func decrypt(t *testing.T, body []byte, uaPriv *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	padded := openRecord(t, body, uaPriv, auth)
	i := bytes.LastIndexByte(padded, 0x02)
	if i < 0 {
		t.Fatal("没有填充分隔符")
	}
	return padded[:i]
}

// openRecord 按接收端的算法解开单记录消息，返回带分隔符的明文。
func openRecord(t *testing.T, body []byte, uaPriv *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	salt, idlen := body[:16], int(body[20])
	asPub, err := ecdh.P256().NewPublicKey(body[21 : 21+idlen])
	if err != nil {
		t.Fatal(err)
	}
	shared, err := uaPriv.ECDH(asPub)
	if err != nil {
		t.Fatal(err)
	}
	prkKey, _ := hkdf.Extract(sha256.New, shared, auth)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(uaPriv.PublicKey().Bytes())+string(asPub.Bytes()), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	padded, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	return padded
}

func TestEncryptRoundTripRandom(t *testing.T) {
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := b64(t, rfcAuth)
	sub := Subscription{Endpoint: "https://push.example.net/p/x", P256dh: uaPriv.PublicKey().Bytes(), Auth: auth}
	msg := []byte(`{"id":"r1","title":"Mac · blog","body":"git push origin main"}`)
	body, err := Encrypt(msg, sub)
	if err != nil {
		t.Fatal(err)
	}
	if got := decrypt(t, body, uaPriv, auth); !bytes.Equal(got, msg) {
		t.Fatalf("往返不一致: %q", got)
	}
	body2, _ := Encrypt(msg, sub)
	if bytes.Equal(body, body2) {
		t.Fatal("两次加密应因随机盐与临时密钥而不同")
	}
	if _, err := Encrypt(make([]byte, MaxPlaintext+1), sub); err != ErrTooLarge {
		t.Fatalf("超长应 ErrTooLarge: %v", err)
	}
}

func TestParseSubscription(t *testing.T) {
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	p := base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes())
	a := base64.URLEncoding.EncodeToString(b64(t, rfcAuth)) // 带 = 补位也要收
	sub, err := ParseSubscription("https://web.push.apple.com/QAbc", p, a)
	if err != nil || len(sub.P256dh) != 65 || len(sub.Auth) != 16 {
		t.Fatalf("解析: %+v %v", sub, err)
	}
	bad := []struct{ e, p, a string }{
		{"http://web.push.apple.com/QAbc", p, a},
		{"https://web.push.apple.com/QAbc", "AAAA", a},
		{"https://web.push.apple.com/QAbc", p, "AAAA"},
		{"", p, a},
		{"https://web.push.apple.com/QAbc", "not base64!", a},
	}
	for _, c := range bad {
		if _, err := ParseSubscription(c.e, c.p, c.a); err == nil {
			t.Errorf("应拒绝 %+v", c)
		}
	}
}

// 以下是对照 RFC 8291 / RFC 8188 逐项核对的补充测试。

// 密钥派生每一步都与附录 A 的中间值一致：key_info、IKM、cek_info、CEK、nonce_info、NONCE。
func TestKeyDerivationMatchesRFC8291AppendixA(t *testing.T) {
	asPriv, err := ecdh.P256().NewPrivateKey(b64(t, rfcASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	uaPub, err := ecdh.P256().NewPublicKey(b64(t, rfcUAPublic))
	if err != nil {
		t.Fatal(err)
	}
	if shared, _ := asPriv.ECDH(uaPub); !bytes.Equal(shared, b64(t, rfcECDHSecret)) {
		t.Fatal("ecdh_secret 与附录 A 不符，向量抄错了")
	}
	if got := keyInfo(b64(t, rfcUAPublic), b64(t, rfcASPublic)); !bytes.Equal(got, b64(t, rfcKeyInfo)) {
		t.Fatalf("key_info 应为 \"WebPush: info\\0\" || ua_public || as_public，得到 %s", base64.RawURLEncoding.EncodeToString(got))
	}
	ikm, err := webPushIKM(asPriv, uaPub, b64(t, rfcAuth))
	if err != nil || !bytes.Equal(ikm, b64(t, rfcIKM)) {
		t.Fatalf("IKM 与附录 A 不符: %s %v", base64.RawURLEncoding.EncodeToString(ikm), err)
	}
	if cekInfo != string(b64(t, rfcCEKInfo)) || nonceInfo != string(b64(t, rfcNonceInfo)) {
		t.Fatalf("cek_info / nonce_info 与 RFC 8188 不符: %q %q", cekInfo, nonceInfo)
	}
	cek, nonce, err := contentKeys(ikm, b64(t, rfcSalt))
	if err != nil || !bytes.Equal(cek, b64(t, rfcCEK)) || !bytes.Equal(nonce, b64(t, rfcNonce)) {
		t.Fatalf("CEK/NONCE 与附录 A 不符: %x %x %v", cek, nonce, err)
	}
}

// 附录 A 的头与填充后明文：头 86 字节；单记录、只追加 0x02 分隔符、不加零填充。
func TestRFC8291HeaderAndPadding(t *testing.T) {
	asPriv, _ := ecdh.P256().NewPrivateKey(b64(t, rfcASPrivate))
	uaPriv, err := ecdh.P256().NewPrivateKey(b64(t, rfcUAPrivate))
	if err != nil {
		t.Fatal(err)
	}
	sub := Subscription{Endpoint: "https://push.example.net/p/x", P256dh: b64(t, rfcUAPublic), Auth: b64(t, rfcAuth)}
	got, err := encryptWith(b64(t, rfcPlaintext), sub, asPriv, b64(t, rfcSalt))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[:headerLen], b64(t, rfcHeader)) || headerLen != 86 {
		t.Fatalf("头与附录 A 不符: %s", base64.RawURLEncoding.EncodeToString(got[:headerLen]))
	}
	if padded := openRecord(t, got, uaPriv, b64(t, rfcAuth)); !bytes.Equal(padded, b64(t, rfcPadded)) {
		t.Fatalf("填充后明文应为 明文 || 0x02: %x", padded)
	}
}

// 随机路径：每条消息新盐、新临时公钥（且是合法的 P-256 未压缩点）；单记录长度 = 86 + 明文 + 1 + 16。
func TestEncryptFreshSaltAndEphemeralKey(t *testing.T) {
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := b64(t, rfcAuth)
	sub := Subscription{Endpoint: "https://push.example.net/p/x", P256dh: uaPriv.PublicKey().Bytes(), Auth: auth}
	msg := []byte("hello")
	a, err := Encrypt(msg, sub)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Encrypt(msg, sub)
	if bytes.Equal(a[:16], b[:16]) {
		t.Fatal("两条消息的盐相同")
	}
	if bytes.Equal(a[21:86], b[21:86]) {
		t.Fatal("两条消息的临时公钥相同")
	}
	for _, body := range [][]byte{a, b} {
		if len(body) != headerLen+len(msg)+1+16 {
			t.Fatalf("长度 %d，应为单记录 %d", len(body), headerLen+len(msg)+1+16)
		}
		if binary.BigEndian.Uint32(body[16:20]) != recordSize || body[20] != 65 {
			t.Fatal("rs 或 idlen 不对")
		}
		if _, err := ecdh.P256().NewPublicKey(body[21:86]); err != nil || body[21] != 4 {
			t.Fatalf("keyid 不是未压缩 P-256 点: %v", err)
		}
		if padded := openRecord(t, body, uaPriv, auth); !bytes.Equal(padded, append(append([]byte{}, msg...), 0x02)) {
			t.Fatalf("填充后明文: %x", padded)
		}
	}
}

// 载荷上限：MaxPlaintext 正好能发，且整条消息不超过 4096（RFC 8291 第 4 节：明文至多 3993 字节）；超 1 字节拒绝。
func TestEncryptSizeLimit(t *testing.T) {
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	sub := Subscription{Endpoint: "https://push.example.net/p/x", P256dh: uaPriv.PublicKey().Bytes(), Auth: b64(t, rfcAuth)}
	if MaxPlaintext > 4096-headerLen-1-16 || MaxPlaintext+1+16 > recordSize {
		t.Fatalf("MaxPlaintext %d 装不进单条 4096 记录", MaxPlaintext)
	}
	body, err := Encrypt(bytes.Repeat([]byte("x"), MaxPlaintext), sub)
	if err != nil || len(body) != headerLen+MaxPlaintext+17 || len(body) > 4096 {
		t.Fatalf("上限明文: %d %v", len(body), err)
	}
	if _, err := Encrypt(make([]byte, MaxPlaintext+1), sub); err != ErrTooLarge {
		t.Fatalf("超长应 ErrTooLarge: %v", err)
	}
}

// 订阅密钥不合法时 Encrypt 报错，不 panic、不产出密文。
func TestEncryptRejectsBadKeys(t *testing.T) {
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	good := uaPriv.PublicKey().Bytes()
	offCurve := bytes.Clone(good)
	offCurve[64] ^= 1
	compressed := append([]byte{2 + good[64]&1}, good[1:33]...)
	cases := map[string]Subscription{
		"auth 15 字节": {P256dh: good, Auth: make([]byte, 15)},
		"auth 17 字节": {P256dh: good, Auth: make([]byte, 17)},
		"公钥不在曲线上":    {P256dh: offCurve, Auth: make([]byte, 16)},
		"压缩点":        {P256dh: compressed, Auth: make([]byte, 16)},
		"空公钥":        {P256dh: nil, Auth: make([]byte, 16)},
	}
	for name, sub := range cases {
		if out, err := Encrypt([]byte("x"), sub); err == nil || out != nil {
			t.Errorf("%s: 应报错，得到 %d 字节 %v", name, len(out), err)
		}
	}
	if _, err := encryptWith([]byte("x"), Subscription{P256dh: good, Auth: make([]byte, 16)}, mustKey(t), make([]byte, 15)); err == nil {
		t.Error("盐不是 16 字节应报错")
	}
}

func mustKey(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// ParseSubscription 校验 p256dh 是曲线上的点，而不只是看长度和首字节。
func TestParseSubscriptionRejectsInvalidPoints(t *testing.T) {
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	good := uaPriv.PublicKey().Bytes()
	offCurve := bytes.Clone(good)
	offCurve[64] ^= 1
	a := base64.RawURLEncoding.EncodeToString(b64(t, rfcAuth))
	enc := base64.RawURLEncoding.EncodeToString
	for name, p := range map[string]string{
		"不在曲线上": enc(offCurve),
		"压缩点":   enc(append([]byte{2 + good[64]&1}, good[1:33]...)),
		"无穷远点":  enc(append([]byte{4}, make([]byte, 64)...)),
	} {
		if _, err := ParseSubscription("https://web.push.apple.com/QAbc", p, a); err == nil {
			t.Errorf("%s: 应拒绝", name)
		}
	}
	for _, e := range []string{"https:///QAbc", "https://", "web.push.apple.com/QAbc", "ftp://web.push.apple.com/x"} {
		if _, err := ParseSubscription(e, enc(good), a); err == nil {
			t.Errorf("端点 %q 应拒绝", e)
		}
	}
	// 解出来的是拷贝出的字节，与 RFC 向量的 ua_public 一致
	sub, err := ParseSubscription("https://push.example.net/p/x", rfcUAPublic, rfcAuth+"==")
	if err != nil || !bytes.Equal(sub.P256dh, b64(t, rfcUAPublic)) || !bytes.Equal(sub.Auth, b64(t, rfcAuth)) {
		t.Fatalf("RFC 向量的订阅: %v", err)
	}
}
