//go:build ignore

// 端到端用的假守护进程（v0.3 角色）：机器侧还没实现角色时，用它按 hub 与守护进程的契约（internal/hub/wire）发一条越界的审批，
// 看手机上只能单次批准。不进 go build ./...（build ignore），web/e2e/role.spec.ts 用 go build 编它。
//
// 流程：用接入码在 hub 登记一台机器 → 连 /ws/agent，hello 声明 role 并带两个角色 → 发一条 BeyondRole 的 pending（会话角色“写作”）
// → 回 ping，等到这条的 decision 后在 stdout 打一行 JSON（{"machine_id","decision"}），回 delivered，退出 0。
// 收到针对这条的 error 帧、超时或连接断开时退出 1。
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/xufei547/railse-opc/internal/hub/wire"
	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

func main() {
	hub := flag.String("hub", "", "hub 地址，如 http://127.0.0.1:53123")
	token := flag.String("token", "", "接入码")
	name := flag.String("name", "e2e-role", "机器名")
	project := flag.String("project", "/tmp/e2e-docs", "请求的项目根")
	flag.Parse()
	if err := run(*hub, *token, *name, *project); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		os.Exit(1)
	}
}

func run(hubURL, token, name, project string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"token": token, "name": name, "home_dir": "/tmp"})
	resp, err := http.Post(hubURL+"/api/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	var enrolled struct {
		MachineID string `json:"machine_id"`
		Secret    string `json:"secret"`
	}
	err = json.NewDecoder(resp.Body).Decode(&enrolled)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		return fmt.Errorf("登记机器: %d %v", resp.StatusCode, err)
	}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hubURL, "http")+"/ws/agent",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + enrolled.Secret}}})
	if err != nil {
		return fmt.Errorf("连 hub: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(wire.MaxFrame)
	send := func(v any) error {
		b, err := wire.Marshal(v)
		if err != nil {
			return err
		}
		return conn.Write(ctx, websocket.MessageText, b)
	}
	roles := []proto.RoleSummary{
		{Name: "开发", Note: "写代码、跑测试", Ceiling: "network", ForbidCount: 1, TrustMaxMins: 1440, Dirs: []string{"~/RailseWork"}},
		{Name: "写作", Note: "只改文档", Ceiling: "write_in_project", ForbidCount: 2, TrustMaxMins: 120, Dirs: []string{project}},
	}
	if err := send(wire.Hello{T: wire.THello, Proto: wire.Proto, MachineID: enrolled.MachineID, AgentVersion: "e2e-fake", HomeDir: "/tmp",
		Pending: []wire.PendingSummary{}, Features: []string{wire.FeatureRole}, Roles: roles}); err != nil {
		return err
	}
	if _, b, err := conn.Read(ctx); err != nil {
		return err
	} else if k, _ := wire.Kind(b); k != wire.TWelcome {
		return fmt.Errorf("第一帧应是 welcome: %s", b)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	reqID := hex.EncodeToString(id)
	if err := send(wire.PendingRequest{T: wire.TPending, RequestID: reqID, Kind: "permission",
		Session:  proto.Session{Tool: "claude", CLISessionID: "e2e-role-session", Cwd: project, Role: "写作"},
		ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: "git push origin main", Cwd: project},
		Risk: "outward", ProjectRoot: project, EngineReason: "超出角色「写作」的边界（上限 write_in_project，这次是 outward）",
		DeadlineSeconds: 600, CreatedAt: time.Now().UTC(), BeyondRole: true}); err != nil {
		return err
	}
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("等决定: %w", err)
		}
		switch k, _ := wire.Kind(b); k {
		case wire.TPing:
			if err := send(wire.Pong{T: wire.TPong}); err != nil {
				return err
			}
		case wire.TError:
			var e wire.Error
			_ = wire.Unmarshal(b, &e)
			if e.RequestID == reqID {
				return fmt.Errorf("hub 拒收: %s", e.Message)
			}
		case wire.TDecision:
			var d wire.Decision
			if err := wire.Unmarshal(b, &d); err != nil {
				return err
			}
			if d.RequestID != reqID {
				continue
			}
			out, _ := json.Marshal(map[string]any{"machine_id": enrolled.MachineID, "decision": d})
			fmt.Println(string(out))
			_ = send(wire.Delivered{T: wire.TDelivered, RequestID: reqID})
			conn.Close(websocket.StatusNormalClosure, "done")
			return nil
		}
	}
}
