// Package push 实现 Web Push 的发送端：RFC 8291 的 aes128gcm 加密与 RFC 8292 的 VAPID 签名，只用标准库。
package push

import (
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
)

// Subscription 是浏览器给的一条推送订阅：端点、浏览器侧 P-256 公钥（65 字节未压缩点）与 16 字节认证密钥。
type Subscription struct {
	Endpoint string
	P256dh   []byte
	Auth     []byte
}

// authLen 是 RFC 8291 第 3.2 节规定的认证密钥长度。
const authLen = 16

// decodeB64 解 base64url，有无 = 补位都收。
func decodeB64(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// validEndpoint 要求推送端点是带主机的 https 地址（RFC 8030 的推送服务只走 HTTPS）。
func validEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// ParseSubscription 校验并解码浏览器 PushSubscription.toJSON() 里的 endpoint 与 keys：
// endpoint 是 https 地址；p256dh 是 P-256 曲线上的点、65 字节未压缩编码；auth 是 16 字节。
func ParseSubscription(endpoint, p256dh, auth string) (Subscription, error) {
	if !validEndpoint(endpoint) {
		return Subscription{}, errors.New("推送端点必须是 https 地址")
	}
	p, err := decodeB64(p256dh)
	if err != nil || len(p) != 65 || p[0] != 4 {
		return Subscription{}, errors.New("p256dh 不是 65 字节的未压缩 P-256 公钥")
	}
	if _, err := ecdh.P256().NewPublicKey(p); err != nil {
		return Subscription{}, errors.New("p256dh 不是 P-256 曲线上的点")
	}
	a, err := decodeB64(auth)
	if err != nil || len(a) != authLen {
		return Subscription{}, errors.New("auth 不是 16 字节")
	}
	return Subscription{Endpoint: endpoint, P256dh: p, Auth: a}, nil
}
