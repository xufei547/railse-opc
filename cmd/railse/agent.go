package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/hookclient"
)

const agentUsage = `用法:
  railse agent run [--dir 目录] [--foreground]   启动守护进程（前台运行；开机自启在第 5 份计划）
  railse agent status|pause|resume|reload [--dir 目录]`

// runAgent 处理 agent 子命令：run 跑守护进程，其余经 socket 发本机管理命令。
func runAgent(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, agentUsage)
		return 2
	}
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "配置目录，默认 $RAILSE_HOME 或 ~/.railse")
	foreground := fs.Bool("foreground", false, "日志同时写到 stderr")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	var cfg agent.Config
	if *dir != "" {
		cfg = agent.ConfigIn(*dir)
	} else {
		c, err := agent.DefaultConfig()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		cfg = c
	}
	switch args[0] {
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if err := runAgentServe(ctx, cfg, *foreground, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "status", "pause", "resume", "reload":
		reply, err := hookclient.Client{SocketPath: cfg.SocketPath, ConnectTimeout: 2 * time.Second}.Control(args[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !reply.OK {
			fmt.Fprintln(stderr, "失败:", reply.Error)
			return 1
		}
		if reply.Status != nil {
			st := reply.Status
			fmt.Fprintf(stdout, "paused: %v\nrules: %d\npending: %d\nmachine: %s\nrules_path: %s\nuptime: %s\n", st.Paused, st.Rules, st.Pending, st.MachineID, st.RulesPath, st.Uptime)
		} else {
			fmt.Fprintln(stdout, "ok")
		}
		return 0
	default:
		fmt.Fprintf(stderr, "未知的 agent 子命令 %q\n\n%s\n", args[0], agentUsage)
		return 2
	}
}

// runAgentServe 跑守护进程直到 ctx 取消。SIGHUP 重载规则。
// 日志只写路径与计数，绝不打印环境变量。
func runAgentServe(ctx context.Context, cfg agent.Config, foreground bool, stderr io.Writer) error {
	// 家目录为空时 ~/ 开头的敏感路径规则会被跳过（fail open），宁可不启动。
	if cfg.HomeDir == "" {
		return errors.New("找不到家目录，拒绝启动：家目录为空会让 ~/ 开头的敏感路径规则失效")
	}
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}
	logFile, err := os.OpenFile(cfg.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	var w io.Writer = logFile
	if foreground {
		w = io.MultiWriter(logFile, stderr)
	}
	logger := log.New(w, "railse-agent ", log.LstdFlags)
	rules, err := agent.LoadRules(cfg.RulesPath)
	if err != nil {
		return err
	}
	audit, err := agent.OpenAudit(cfg.AuditPath)
	if err != nil {
		return err
	}
	defer audit.Close()
	srv := agent.New(cfg, rules, audit, agent.LocalDecider{}, logger)
	if err := srv.Listen(); err != nil {
		return err
	}
	// 只在 Listen 成功后才登记 Close：失败时 socket 文件可能属于已在运行的守护进程。
	defer srv.Close()
	if err := srv.LoadState(); err != nil {
		logger.Printf("读状态文件: %v", err)
	}
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	// 返回前先停掉并等完重载 goroutine，免得它在日志文件关闭后还在写。
	hupCtx, stopHup := context.WithCancel(ctx)
	hupDone := make(chan struct{})
	go func() {
		defer close(hupDone)
		for {
			select {
			case <-hup:
				if err := rules.Reload(); err != nil {
					logger.Printf("SIGHUP 重载规则失败，保留旧规则: %v", err)
				} else {
					logger.Printf("SIGHUP 重载规则：%d 条", rules.Count())
				}
			case <-hupCtx.Done():
				return
			}
		}
	}()
	defer func() {
		stopHup()
		<-hupDone
	}()
	logger.Printf("监听 %s，规则 %d 条，机器 %s", cfg.SocketPath, rules.Count(), cfg.MachineID)
	return srv.Serve(ctx)
}
