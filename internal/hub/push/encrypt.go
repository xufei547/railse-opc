package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// MaxPlaintext 是推送载荷上限：Apple 与各推送服务收 4 KiB 的 body，这里给加密头与填充留余量。
const MaxPlaintext = 3 * 1024

// ErrTooLarge 表示载荷超过 MaxPlaintext。
var ErrTooLarge = errors.New("推送载荷超过 3 KiB")

const (
	// recordSize 是 aes128gcm 头里的 rs。消息只有一条记录，记录长度 = 明文 + 1 字节分隔符 + 16 字节标签，必须不超过 rs。
	recordSize = 4096
	// headerLen 是 aes128gcm 头长：salt(16) || rs(4) || idlen(1) || keyid(65，应用服务器的临时公钥)。
	headerLen = 16 + 4 + 1 + 65
	// gcmTagLen 是 AEAD_AES_128_GCM 的标签长。
	gcmTagLen = 16

	// keyInfoPrefix 是 RFC 8291 第 3.4 节 key_info 的前缀，后接 ua_public 与 as_public。
	keyInfoPrefix = "WebPush: info\x00"
	// cekInfo、nonceInfo 是 RFC 8188 第 2.2、2.3 节的 info 串。
	cekInfo   = "Content-Encoding: aes128gcm\x00"
	nonceInfo = "Content-Encoding: nonce\x00"
)

// 编译期检查：上限明文加分隔符与标签装得进一条记录，整条消息不超过 4096 字节（RFC 8291 第 4 节，明文至多 3993 字节）。
const (
	_ = uint(recordSize - (MaxPlaintext + 1 + gcmTagLen))
	_ = uint(4096 - (headerLen + MaxPlaintext + 1 + gcmTagLen))
)

// Encrypt 按 RFC 8291 加密：每条消息新的随机盐与临时 ECDH 密钥。
func Encrypt(plaintext []byte, sub Subscription) ([]byte, error) {
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encryptWith(plaintext, sub, asPriv, salt)
}

// encryptWith 是 Encrypt 的确定性核心，测试用 RFC 8291 附录 A 的密钥与盐核对输出。
func encryptWith(plaintext []byte, sub Subscription, asPriv *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plaintext) > MaxPlaintext {
		return nil, ErrTooLarge
	}
	if len(salt) != 16 {
		return nil, errors.New("盐应为 16 字节")
	}
	if len(sub.Auth) != authLen {
		return nil, errors.New("订阅的 auth 应为 16 字节")
	}
	uaPub, err := ecdh.P256().NewPublicKey(sub.P256dh)
	if err != nil {
		return nil, fmt.Errorf("订阅公钥不是 P-256 未压缩点: %w", err)
	}
	ikm, err := webPushIKM(asPriv, uaPub, sub.Auth)
	if err != nil {
		return nil, err
	}
	cek, nonce, err := contentKeys(ikm, salt)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// 单记录：明文后接 0x02（最后一条记录的分隔符），不加零填充
	padded := make([]byte, 0, len(plaintext)+1)
	padded = append(padded, plaintext...)
	padded = append(padded, 0x02)
	asPub := asPriv.PublicKey().Bytes()
	out := make([]byte, 0, headerLen+len(padded)+gcm.Overhead())
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(asPub)))
	out = append(out, asPub...)
	// 记录序号 0 的 nonce 就是 NONCE 本身（RFC 8188 第 2.3 节，与序号异或）
	return gcm.Seal(out, nonce, padded, nil), nil
}

// keyInfo 是 RFC 8291 第 3.4 节的 key_info = "WebPush: info" || 0x00 || ua_public || as_public。
func keyInfo(uaPub, asPub []byte) []byte {
	b := make([]byte, 0, len(keyInfoPrefix)+len(uaPub)+len(asPub))
	b = append(b, keyInfoPrefix...)
	b = append(b, uaPub...)
	return append(b, asPub...)
}

// webPushIKM 按 RFC 8291 第 3.3、3.4 节组合密钥：
// PRK_key = HKDF-Extract(salt=auth_secret, IKM=ecdh_secret)，IKM = HKDF-Expand(PRK_key, key_info, 32)。
func webPushIKM(asPriv *ecdh.PrivateKey, uaPub *ecdh.PublicKey, auth []byte) ([]byte, error) {
	shared, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	prkKey, err := hkdf.Extract(sha256.New, shared, auth)
	if err != nil {
		return nil, err
	}
	return hkdf.Expand(sha256.New, prkKey, string(keyInfo(uaPub.Bytes(), asPriv.PublicKey().Bytes())), 32)
}

// contentKeys 按 RFC 8188 第 2.2、2.3 节派生：PRK = HKDF-Extract(salt, IKM)，
// CEK = HKDF-Expand(PRK, cek_info, 16)，NONCE = HKDF-Expand(PRK, nonce_info, 12)。
func contentKeys(ikm, salt []byte) (cek, nonce []byte, err error) {
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, nil, err
	}
	if cek, err = hkdf.Expand(sha256.New, prk, cekInfo, 16); err != nil {
		return nil, nil, err
	}
	if nonce, err = hkdf.Expand(sha256.New, prk, nonceInfo, 12); err != nil {
		return nil, nil, err
	}
	return cek, nonce, nil
}
