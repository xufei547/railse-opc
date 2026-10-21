// Package hookclient 是钩子进程这一侧：连本地 socket，发一行，等一行。
package hookclient

import (
	"bufio"
	"context"
	"fmt"
	"net"
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
)

// Client 指向守护进程的 socket。
type Client struct {
	SocketPath     string
	ConnectTimeout time.Duration // 为零时用 2 秒
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
		conn.SetDeadline(dl)
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
func (c Client) Permission(ctx context.Context, req proto.Request) (proto.Response, error) {
	var resp proto.Response
	if err := c.roundTrip(ctx, req, &resp); err != nil {
		return proto.Response{}, err
	}
	return resp, nil
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
