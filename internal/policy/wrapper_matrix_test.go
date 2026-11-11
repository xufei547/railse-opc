package policy

import (
	"fmt"
	"strings"
	"testing"
)

// 包装命令的矩阵：包装种类 × 嵌套一到三层 × 四类检查（定级、禁用、配置目录、环境变量）。四类检查都走同一个逐层遍历器
// （walkLayers / shellViews），这里铺开确认没有哪一类、哪一层把状态丢了。

// matrixWrappers 是会出现在命令前面的包装写法；sudo、doas 自身就是 destructive。
var matrixWrappers = []string{"env", "/usr/bin/env", "sudo", "doas", "nice -n 5", "nohup", "timeout 5", "command", "exec", "xargs", "time", "caffeinate", "sudo -u bob", "env FOO=1"}

// matrixChains 是一到三层的全部组合。
func matrixChains() [][]string {
	var out [][]string
	for _, a := range matrixWrappers {
		out = append(out, []string{a})
		for _, b := range matrixWrappers {
			out = append(out, []string{a, b})
		}
	}
	for i, a := range matrixWrappers {
		for j, b := range matrixWrappers {
			c := matrixWrappers[(i+j)%len(matrixWrappers)]
			out = append(out, []string{a, b, c})
		}
	}
	return out
}

func hasPrivileged(chain []string) bool {
	for _, w := range chain {
		if strings.HasPrefix(w, "sudo") || w == "doas" {
			return true
		}
	}
	return false
}

func TestWrapperMatrix(t *testing.T) {
	e := mustEngine(t, roleEngineRules)
	dd := roleHome + "/dd"
	ctx := func(role string) Context {
		c := roleCtx(t, role)
		c.ProtectedDirs = []string{dd}
		return c
	}
	at := func(cmd string) Payload { return Payload{Type: PayloadShell, Command: cmd, Cwd: roleHome + "/proj"} }
	n := 0
	for _, chain := range matrixChains() {
		prefix := strings.Join(chain, " ") + " "
		// 一、定级：内层是推送，至少 outward；链上有 sudo、doas 时是 destructive（各层包装风险与最内层取最大）
		push := prefix + "git push origin main"
		want := RiskOutward
		if hasPrivileged(chain) {
			want = RiskDestructive
		}
		if r := Classify(at(push), ctx("")); r < want {
			t.Errorf("定级 %q = %s，至少 %s", push, r, want)
		}
		// 二、禁用：写作禁用 git push
		if d := e.Evaluate(at(push), ctx("写作")); !d.Forbidden {
			t.Errorf("禁用 %q: %+v", push, d)
		}
		// 三、配置目录：在链的每一个位置插入 env -C <守护进程目录>（或 sudo -D），内层按相对路径写 roles.yaml
		for pos := 0; pos <= len(chain); pos++ {
			for _, cd := range []string{"env -C " + dd, "sudo -D " + dd, "env --chdir=" + dd} {
				ws := append(append(append([]string{}, chain[:pos]...), cd), chain[pos:]...)
				cmd := strings.Join(ws, " ") + " cp " + roleHome + "/proj/replacement.yaml roles.yaml"
				if d := e.Evaluate(at(cmd), ctx("")); !d.ControlPlane || d.Action == ActionAllow {
					t.Errorf("配置目录 %q: %+v", cmd, d)
				}
				// 改了目录之后再经 sh -c：内层继承外层累积的工作目录
				shc := strings.Join(ws, " ") + " sh -c 'cp " + roleHome + "/proj/replacement.yaml roles.yaml'"
				if d := e.Evaluate(at(shc), ctx("")); !d.ControlPlane {
					t.Errorf("配置目录经 sh -c %q: %+v", shc, d)
				}
			}
		}
		// 四、环境变量：改、删、清空 Railse 与配置位置的变量
		for _, inner := range []string{"RAILSE_ROLE=x claude -p hi", "unset RAILSE_ROLE", "env -u HOME claude -p hi", "env -i claude -p hi", "export HOME=/tmp/x", "exec -c claude"} {
			cmd := prefix + inner
			if d := e.Evaluate(at(cmd), ctx("运维")); !d.BeyondRole || !strings.Contains(d.Reason, "试图更改") {
				t.Errorf("环境变量 %q: %+v", cmd, d)
			}
			if d := e.Evaluate(at(cmd), ctx("")); d.BeyondRole {
				t.Errorf("没有角色时照旧 %q: %+v", cmd, d)
			}
		}
		n++
	}
	if n < 200 {
		t.Fatalf("矩阵太小: %d", n)
	}
	t.Logf("包装链 %d 条", n)
}

// 收尾轮点名的三个复现。
func TestWrapperWalkerRepros(t *testing.T) {
	e := mustEngine(t, roleEngineRules)
	c := Context{ProjectRoot: "/项目", HomeDir: roleHome, ProtectedDirs: []string{"/tmp/rs"}}
	if d := e.Evaluate(Payload{Type: PayloadShell, Command: "env -C /tmp/rs sh -c 'cp /项目/replacement.yaml roles.yaml'", Cwd: "/项目"}, c); !d.ControlPlane {
		t.Errorf("env -C 之后的 sh -c: %+v", d)
	}
	if r := Classify(Payload{Type: PayloadShell, Command: "command sudo touch a.txt", Cwd: "/项目"}, c); r != RiskDestructive {
		t.Errorf("command sudo touch: %s", r)
	}
	for _, cmd := range []string{"command env -u HOME claude -p hi", "command unset RAILSE_ROLE", "env -i claude -p hi"} {
		if d := e.Evaluate(shellAt(cmd), roleCtx(t, "运维")); !d.BeyondRole || !strings.Contains(d.Reason, "试图更改") {
			t.Errorf("%s: %+v", cmd, d)
		}
	}
	_ = fmt.Sprint
}
