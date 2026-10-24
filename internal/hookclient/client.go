// Package hookclient 是钩子进程这一侧：连本地 socket，发一行，等一行。
package hookclient

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/railse/railse-opc/internal/proto"
)

const (
	// defaultConnectTimeout 是 ConnectTimeout 为零时的连接超时。
	defaultConnectTimeout = 2 * time.Second
	// eventTimeout 是一条事件从连接到收到确认的总时限。Gemini 的 Notification 会同步阻塞原生确认框，不能多等。
	eventTimeout = 2 * time.Second
	// controlTimeout 是一条管理命令的总时限。
	controlTimeout = 5 * time.Second
	// 等审批答复期间看门狗的默认参数：每 15 秒探测一次，单次限 3 秒，连续 2 次失败就放弃。
	defaultWatchInterval = 15 * time.Second
	defaultWatchTimeout  = 3 * time.Second
	defaultWatchFailures = 2
)

// ErrUnresponsive 是看门狗判定守护进程活着但不响应、放弃等待审批答复时的原因。
var ErrUnresponsive = errors.New("守护进程无响应")

// Client 指向守护进程的 socket。
type Client struct {
	SocketPath     string
	ConnectTimeout time.Duration // 为零时用 2 秒
	// 以下三项控制 Permission 等答复期间的看门狗，零值用默认值，测试里调小。
	WatchInterval time.Duration // 探测间隔，默认 15 秒
	WatchTimeout  time.Duration // 单次探测时限，默认 3 秒
	WatchFailures int           // 连续失败几次就放弃，默认 2
}

// dial 连 socket，受 ConnectTimeout 与 ctx 双重限制。
func (c Client) dial(ctx context.Context) (net.Conn, error) {
	timeout := c.ConnectTimeout
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("连接守护进程: %w", err) // err 里已带 socket 路径
	}
	return conn, nil
}

// roundTrip 连接、发一行 msg、读一行解到 out，整个过程以 ctx 为限。
// ctx 取消（比如钩子收到 SIGTERM）时立即关掉连接：阻塞中的读写马上返回，守护进程读到 EOF 会把请求标为 cancelled。
func (c Client) roundTrip(ctx context.Context, msg, out any) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl) // 只在连接已关时失败，随后的写会报错；ctx 到期时下面的 AfterFunc 也会关连接
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := proto.WriteLine(conn, msg); err != nil {
		return stepErr(ctx, "发送请求", err)
	}
	line, err := proto.ReadLine(bufio.NewReader(conn))
	if err != nil {
		return stepErr(ctx, "等待答复", err)
	}
	return proto.Decode(line, out)
}

// stepErr 优先报 ctx 结束的原因（超时、收到信号），其次才是底层的连接错误。
func stepErr(ctx context.Context, step string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", step, context.Cause(ctx))
	}
	return fmt.Errorf("%s: %w", step, err)
}

// Permission 发审批请求并等答复，直到 ctx 结束。
// 守护进程被 SIGSTOP、死锁或卡在写盘时，连接和发送都会成功（内核照收），读答复却会一直阻塞；
// 所以等答复期间起一个看门狗，定期用新连接发 status，连续失败就以 ErrUnresponsive 取消这次请求。
// 收到答复（或出错）后看门狗随即退出，Permission 返回前等它退出，不留 goroutine。
func (c Client) Permission(ctx context.Context, req proto.Request) (proto.Response, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	wctx, stopWatch := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.watch(wctx, cancel)
	}()
	var resp proto.Response
	err := c.roundTrip(ctx, req, &resp)
	stopWatch() // 进行中的探测随 wctx 取消立即关连接返回
	wg.Wait()
	if err != nil {
		return proto.Response{}, err
	}
	return resp, nil
}

// watch 每隔 WatchInterval 用新连接探测一次 status，连续 WatchFailures 次失败就以 ErrUnresponsive 取消请求。
// ctx 结束（收到答复、调用方取消）时返回。
func (c Client) watch(ctx context.Context, cancel context.CancelCauseFunc) {
	interval, timeout, limit := c.WatchInterval, c.WatchTimeout, c.WatchFailures
	if interval <= 0 {
		interval = defaultWatchInterval
	}
	if timeout <= 0 {
		timeout = defaultWatchTimeout
	}
	if limit <= 0 {
		limit = defaultWatchFailures
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, pcancel := context.WithTimeout(ctx, timeout)
		var reply proto.ControlReply
		err := c.roundTrip(pctx, proto.Control{V: proto.Version, Kind: "control", Op: "status"}, &reply)
		pcancel()
		if ctx.Err() != nil {
			return // 探测期间已收到答复或被取消，这次结果不算
		}
		if err == nil && reply.OK {
			fails = 0
			continue
		}
		if fails++; fails >= limit {
			cancel(ErrUnresponsive)
			return
		}
	}
}

// Event 发只展示的事件，从连接到收到确认整个过程不超过 2 秒。
func (c Client) Event(ev proto.Event) error {
	ctx, cancel := context.WithTimeout(context.Background(), eventTimeout)
	defer cancel()
	var ack proto.EventAck
	if err := c.roundTrip(ctx, ev, &ack); err != nil {
		return err
	}
	if !ack.OK {
		return fmt.Errorf("守护进程拒绝事件: %s", ack.Error)
	}
	return nil
}

// Control 发管理命令，整个过程不超过 5 秒。
func (c Client) Control(op string) (proto.ControlReply, error) {
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	var reply proto.ControlReply
	if err := c.roundTrip(ctx, proto.Control{V: proto.Version, Kind: "control", Op: op}, &reply); err != nil {
		return proto.ControlReply{}, err
	}
	return reply, nil
}
