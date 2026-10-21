package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/railse/railse-opc/internal/policy"
	"github.com/railse/railse-opc/internal/proto"
)

// Outcome 是引擎之外的决定来源（hub、手机）给出的结论。
type Outcome struct {
	Action    string // allow、deny、passthrough
	Reason    string
	RuleID    string
	DecidedBy string // user、no_hub、timeout…
}

// PendingRequest 是等待外部决定的请求。
type PendingRequest struct {
	ID       string
	Req      proto.Request
	Risk     policy.Risk
	Created  time.Time
	Deadline time.Time
}

// Decider 在引擎答 ask 之后给出最终结论。ctx 取消表示钩子已消失，到期表示过期。
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
