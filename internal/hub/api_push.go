package hub

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/xufei547/railse-opc/internal/hub/push"
	"github.com/xufei547/railse-opc/internal/hub/store"
)

const (
	pushBodyMax      = 8 << 10 // /api/push/* 的请求体：订阅 JSON 不到 1 KiB
	pushEndpointMax  = 2048    // 推送端点的字节上限（Apple、FCM、Mozilla 的端点都在 500 字节以内）
	pushUserAgentMax = 256
	// maxPushSubscriptions 是推送订阅的条数上限：每条新请求要推给全部订阅。
	maxPushSubscriptions = 20
)

// testPushMessage 是 POST /api/push/test 发的那一条。
var testPushMessage = []byte(`{"id":"test","kind":"test","title":"Railse","body":"通知正常","url":"/"}`)

// pushRoutes 注册 /api/push/*。全部要登录 cookie；写接口先过 sameOrigin（与 apiRoutes 一致）。
// 响应只用这里拼的 map，从不编码 store.PushSubscription：订阅密钥与 VAPID 私钥不经任何接口返回。
func (s *Server) pushRoutes() {
	read := s.requireClient
	write := func(h http.HandlerFunc) http.HandlerFunc { return sameOrigin(s.requireClient(h)) }
	s.mux.HandleFunc("GET /api/push/vapid", read(s.handlePushVapid))
	s.mux.HandleFunc("POST /api/push/subscribe", write(s.handlePushSubscribe))
	s.mux.HandleFunc("DELETE /api/push/subscribe", write(s.handlePushUnsubscribe))
	s.mux.HandleFunc("POST /api/push/check", write(s.handlePushCheck))
	s.mux.HandleFunc("POST /api/push/test", write(s.handlePushTest))
}

// handlePushVapid 回 {"enabled","public_key","reason"}：推送停用时 public_key 为空、reason 说明原因。
// 公钥每次从库里读（另一个进程换过钥时网页拿到的就是新的）。
func (s *Server) handlePushVapid(w http.ResponseWriter, r *http.Request) {
	if !s.push.enabled {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "public_key": "", "reason": pushDisabledReason})
		return
	}
	v, err := s.push.key()
	if err != nil {
		s.internalErr(w, "读 VAPID 密钥", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "public_key": v.PublicKeyB64(), "reason": ""})
}

// subscribeBody 是浏览器 PushSubscription.toJSON() 的形状（多出的 expirationTime 等字段忽略）。
type subscribeBody struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

type endpointBody struct {
	Endpoint string `json:"endpoint"`
}

// handlePushSubscribe 登记订阅：先过 push.ParseSubscription（https 端点、P-256 点、16 字节 auth）再入库，
// 同一端点再登记是更新（幂等）。新端点在已有 maxPushSubscriptions 条时回 409（同一端点再登记不算新增）。
// 推送停用 503，坏订阅 400，成功 204。
func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if !s.push.enabled {
		writeErr(w, http.StatusServiceUnavailable, pushDisabledReason)
		return
	}
	var body subscribeBody
	if err := decodeJSON(w, r, pushBodyMax, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON，或超过 8 KiB")
		return
	}
	if len(body.Endpoint) > pushEndpointMax {
		writeErr(w, http.StatusBadRequest, "推送端点太长")
		return
	}
	// ParseSubscription 的错误只说哪一项不对，不带端点与密钥的内容
	if _, err := push.ParseSubscription(body.Endpoint, body.Keys.P256dh, body.Keys.Auth); err != nil {
		writeErr(w, http.StatusBadRequest, "订阅无效："+err.Error())
		return
	}
	// 查上限与入库串行进行，并发的登记不会越过上限
	s.push.subMu.Lock()
	defer s.push.subMu.Unlock()
	if _, err := s.store.GetPushSubscription(body.Endpoint); errors.Is(err, store.ErrNotFound) {
		n, err := s.store.CountPushSubscriptions()
		if err != nil {
			s.internalErr(w, "统计推送订阅", err)
			return
		}
		if n >= maxPushSubscriptions {
			writeErr(w, http.StatusConflict, fmt.Sprintf("推送订阅已达上限 %d 个，请先在别的设备上关闭通知", maxPushSubscriptions))
			return
		}
	} else if err != nil {
		s.internalErr(w, "查推送订阅", err)
		return
	}
	sub, err := s.store.UpsertPushSubscription(body.Endpoint, body.Keys.P256dh, body.Keys.Auth, truncate(r.UserAgent(), pushUserAgentMax))
	if err != nil {
		s.internalErr(w, "登记推送订阅", err)
		return
	}
	s.audit("", "user", "", "push_subscribed", map[string]string{"id": sub.ID, "origin": pushOrigin(sub.Endpoint)})
	w.WriteHeader(http.StatusNoContent)
}

// handlePushUnsubscribe 删订阅，没有也 204（幂等）。推送停用时也能删。
func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var body endpointBody
	if err := decodeJSON(w, r, pushBodyMax, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON，或超过 8 KiB")
		return
	}
	removed, err := s.store.DeletePushSubscription(body.Endpoint)
	if err != nil {
		s.internalErr(w, "删推送订阅", err)
		return
	}
	if removed {
		s.audit("", "user", "", "push_unsubscribed", map[string]string{"origin": pushOrigin(body.Endpoint)})
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePushCheck 回 {"active":bool}：hub 有这条订阅（且密钥完好）时为 true。网页用它发现订阅已被 hub 清掉、需要重新开启。
func (s *Server) handlePushCheck(w http.ResponseWriter, r *http.Request) {
	var body endpointBody
	if err := decodeJSON(w, r, pushBodyMax, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON，或超过 8 KiB")
		return
	}
	sub, err := s.store.GetPushSubscription(body.Endpoint)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]bool{"active": !sub.Broken})
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusOK, map[string]bool{"active": false})
	default:
		s.internalErr(w, "查推送订阅", err)
	}
}

// handlePushTest 同步给一条订阅发测试通知（与新请求的推送同一个客户端、同一套失败处理）：成功 204；没有这条订阅 404；
// 端点没收下 502（订阅已失效时已删掉）；推送停用 503。
func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	if !s.push.enabled {
		writeErr(w, http.StatusServiceUnavailable, pushDisabledReason)
		return
	}
	var body endpointBody
	if err := decodeJSON(w, r, pushBodyMax, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON，或超过 8 KiB")
		return
	}
	sub, err := s.store.GetPushSubscription(body.Endpoint)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "hub 没有这条订阅，请重新开启通知")
		return
	}
	if err != nil {
		s.internalErr(w, "查推送订阅", err)
		return
	}
	if _, err := s.push.key(); err != nil {
		s.internalErr(w, "读 VAPID 密钥", err)
		return
	}
	out, _ := s.push.sendOne(r.Context(), sub, testPushMessage, push.Options{TTL: pushTestTTL, Urgency: "high"})
	s.audit("", "user", "", "push_test", map[string]any{"ok": out == pushSent, "origin": pushOrigin(sub.Endpoint)})
	switch out {
	case pushSent:
		w.WriteHeader(http.StatusNoContent)
	case pushGone:
		writeErr(w, http.StatusBadGateway, "推送服务说这条订阅已失效，hub 已删掉它；请重新开启通知")
	default:
		writeErr(w, http.StatusBadGateway, "推送没有送到（原因见 hub 日志），请稍后再试；一直不行就关掉再重新开启通知")
	}
}
