package hub

import (
	"encoding/json"
	"sync"
)

// clientHub 把 hub 里发生的事推给已打开的网页。慢的订阅者丢帧，不阻塞主流程。
type clientHub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func newClientHub() *clientHub { return &clientHub{subs: map[chan []byte]struct{}{}} }

// subscribe 返回订阅通道（缓冲 64 帧）与取消函数。
func (h *clientHub) subscribe() (chan []byte, func()) {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// broadcast 发 {"type":kind,"data":payload}。编码失败不发；订阅者缓冲满时丢掉这一帧。
func (h *clientHub) broadcast(kind string, payload any) {
	b, err := json.Marshal(map[string]any{"type": kind, "data": payload})
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- b:
		default:
		}
	}
}
