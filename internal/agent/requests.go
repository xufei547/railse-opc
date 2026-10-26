package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// Outcome 是引擎之外的决定来源（hub、手机）给出的结论。
type Outcome struct {
	Action    string // allow、deny、passthrough
	Reason    string
	RuleID    string
	DecidedBy string // user、no_hub、hub_unreachable、timeout…
	// Note 是用户在手机上写的备注，只进审计，永远不写回钩子（不交给适配器和模型）。
	Note string
}

// PendingRequest 是等待外部决定的请求。Redacted 是脱敏后的载荷，上报 hub 用它。
type PendingRequest struct {
	ID           string
	Req          proto.Request
	Risk         policy.Risk
	ProjectRoot  string
	Redacted     policy.Payload
	EngineReason string
	Created      time.Time
	Deadline     time.Time
}

// Decider 在引擎答 ask 之后给出最终结论。ctx 取消表示钩子已消失或守护进程正在停止，到期表示过期。
// 契约：实现必须尊重 ctx，取消或到期时及时返回（返回 ctx 的错误即可）；服务端不另起 goroutine 兜底，
// 不理会 ctx 的实现会让这条请求一直挂着，关停时也会卡住 Close。
type Decider interface {
	Decide(ctx context.Context, pr PendingRequest) (Outcome, error)
}

// LocalDecider 是没有 hub 时的实现：立即不作决定，让 CLI 弹原生框。
type LocalDecider struct{}

// Decide 实现 Decider。
func (LocalDecider) Decide(ctx context.Context, pr PendingRequest) (Outcome, error) {
	return Outcome{Action: "passthrough", Reason: "本机没有连接 hub，交回终端处理", DecidedBy: "no_hub"}, nil
}

// Pending 是未完成请求的表。
type Pending struct {
	mu sync.Mutex
	m  map[string]*PendingRequest
}

func newPending() *Pending { return &Pending{m: map[string]*PendingRequest{}} }

// Add 登记。
func (p *Pending) Add(pr *PendingRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[pr.ID] = pr
}

// Remove 注销。
func (p *Pending) Remove(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.m, id)
}

// Count 返回数量。
func (p *Pending) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.m)
}

// List 返回按创建时间排序（相同时按 ID）的副本，重连时上报 hub 用。
// MCP 参数（json.RawMessage）也复制一份，调用方改返回值不会影响表里正在等待的请求。
func (p *Pending) List() []PendingRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PendingRequest, 0, len(p.m))
	for _, pr := range p.m {
		c := *pr
		c.Req.Payload.Args = cloneRaw(pr.Req.Payload.Args)
		c.Redacted.Args = cloneRaw(pr.Redacted.Args)
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func cloneRaw(b json.RawMessage) json.RawMessage {
	if b == nil {
		return nil
	}
	return append(json.RawMessage(nil), b...)
}

// HasSession 判断某个 CLI 会话是否有未完成的审批，用于压掉随后的 question 事件。
func (p *Pending) HasSession(cliSessionID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pr := range p.m {
		if pr.Req.Session.CLISessionID == cliSessionID {
			return true
		}
	}
	return false
}

// NewID 生成 32 个十六进制字符的随机 id。
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("随机数不可用: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
