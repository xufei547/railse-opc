#!/usr/bin/env bash
# 真实 Claude Code 经 hub 审批的冒烟：守护进程把引擎定不了的 git push 送到 hub，脚本用 REST 当“手机”批准，钩子放行。
# 需要本机已装 claude 且已登录，会消耗模型额度。不进 CI，发布前手动跑。
#
# 不碰真实的 ~/.railse、~/.railse-hub 与 ~/.claude 下的配置：hub 与守护进程的数据目录都在临时目录里（RAILSE_HUB_HOME、
# RAILSE_HOME），钩子写在临时项目的 .claude/settings.local.json。claude 用你自己的登录态（HOME 不变），
# --setting-sources project,local 不读用户级设置（免得用户级的 defaultMode 替人审批）。
# 临时项目没有 git 远端，git push 本身会失败，这是预期的：要验证的是审批链路（hub 里批准、决定送回钩子）。
# 成功时删掉临时目录；失败时保留，并打印 hub.log、agent.log 的末尾。
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# Unix socket 路径有长度上限（macOS 104 字节）：临时目录放在 /tmp 下、名字取短
WORK="$(mktemp -d /tmp/rs-smoke.XXXX)" || { echo "FAIL: 建不了临时目录"; exit 1; }
export RAILSE_HUB_HOME="$WORK/hub" RAILSE_HOME="$WORK/home"
PW="smoke-password-1"
CMD="git push origin main"
HUB_PID="" AGENT_PID="" APPROVER_PID=""
PASSED=0

# 只停这次起的进程：逐个判空再 kill（变量为空时绝不能变成 kill 0，那会杀掉整个进程组）
cleanup() {
  local p
  for p in "$APPROVER_PID" "$AGENT_PID" "$HUB_PID"; do
    if [ -n "$p" ]; then kill "$p" 2>/dev/null || true; fi
  done
  for p in "$APPROVER_PID" "$AGENT_PID" "$HUB_PID"; do
    if [ -n "$p" ]; then wait "$p" 2>/dev/null || true; fi
  done
  if [ "$PASSED" = 1 ]; then
    rm -rf "$WORK"
  else
    echo "临时目录保留在 $WORK"
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

show_logs() {
  local f
  for f in hub/hub.log hub.stderr home/agent.log agent.stderr; do
    if [ -s "$WORK/$f" ]; then
      echo "--- $f 末尾 ---"
      tail -n 30 "$WORK/$f"
    fi
  done
}
fail() {
  echo "FAIL: $1"
  show_logs
  exit 1
}

command -v claude >/dev/null || fail "没有找到 claude"
command -v python3 >/dev/null || fail "没有找到 python3（选端口、解析 JSON 用）"
command -v curl >/dev/null || fail "没有找到 curl"
# 超时包在 env -i 外层（shell 函数经 env 调用不起作用）：timeout、gtimeout（coreutils），都没有时用 macOS 自带的 perl
if command -v timeout >/dev/null 2>&1; then
  TO=(timeout 120)
elif command -v gtimeout >/dev/null 2>&1; then
  TO=(gtimeout 120)
else
  TO=(perl -e 'alarm shift; exec @ARGV or die "exec $ARGV[0]: $!\n"' 120)
fi

(cd "$ROOT" && go build -o "$WORK/railse" ./cmd/railse) || fail "构建失败"
R="$WORK/railse"
mkdir -p "$WORK/proj" && (cd "$WORK/proj" && git init -q) || fail "建不了临时项目"

# hub 第一次启动时导入的就是内置规则包（与 railse policy eval 不给 --rules 时用的同一套），接入后整套下发给守护进程。
# 内置规则对 git push 答 ask 才会送到 hub；不是 ask 时这条冒烟测不到 hub 链路，直接报错而不是悄悄通过。
verdict="$("$R" policy eval --cwd "$WORK/proj" "$CMD" | cut -f1)"
[ "$verdict" = ask ] || fail "内置规则对 $CMD 给的是 ${verdict:-（空）}，不是 ask：请求不会送到 hub"

port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')" || fail "选不到端口"
HUB="http://127.0.0.1:$port"
printf '%s\n' "$PW" | "$R" hub password >/dev/null || fail "railse hub password 失败"
"$R" hub run --listen "127.0.0.1:$port" >"$WORK/hub.stderr" 2>&1 &
HUB_PID=$!
for _ in $(seq 1 100); do curl -fs "$HUB/api/version" >/dev/null 2>&1 && break; sleep 0.1; done
curl -fs "$HUB/api/version" >/dev/null 2>&1 || fail "hub 没起来"

TOKEN="$("$R" hub token | sed -n 's/^接入码: //p')"
[ -n "$TOKEN" ] || fail "railse hub token 没有给出接入码"
"$R" agent enroll --hub "$HUB" --token "$TOKEN" --name smoke >"$WORK/enroll.out" 2>&1 || { cat "$WORK/enroll.out"; fail "接入失败"; }
"$R" agent run >"$WORK/agent.stderr" 2>&1 &
AGENT_PID=$!
for _ in $(seq 1 100); do [ -S "$RAILSE_HOME/agent.sock" ] && break; sleep 0.1; done
[ -S "$RAILSE_HOME/agent.sock" ] || fail "守护进程没起来"
# 等守护进程连上 hub（收到 welcome 与规则），免得请求先到、在 30 秒的断线宽限里空等
for _ in $(seq 1 100); do "$R" agent status 2>/dev/null | grep -q '^hub: connected' && break; sleep 0.1; done
"$R" agent status 2>/dev/null | grep -q '^hub: connected' || fail "守护进程没连上 hub（$("$R" agent status 2>&1 | grep '^hub:')）"

"$R" setup hooks --project "$WORK/proj" --claude --bin "$R" >/dev/null || fail "装 Claude 钩子失败"

# “手机”：登录（cookie 文件 0600）后轮询收件箱。只批准 $CMD；别的待审批一律拒绝，免得替真实的 claude 放行意料之外的操作。
# 批准前先把 id 写下：claude 拿到决定后很快结束，最后的断言以 hub 里该请求的状态为准。
CK="$WORK/cookie"
(umask 077 && : >"$CK")
printf '{"password":"%s"}' "$PW" | curl -fs -c "$CK" -H 'Content-Type: application/json' --data-binary @- "$HUB/api/login" || fail "登录 hub 失败"
(
  for _ in $(seq 1 600); do
    next="$(curl -fs -b "$CK" "$HUB/api/inbox" 2>/dev/null | CMD="$CMD" python3 -c '
import json, os, sys
p = json.load(sys.stdin)["pending"]
if p:
    r = p[-1]  # 按创建时间倒序，取最早的一条
    ok = r.get("tool_name") == "Bash" and (r.get("payload") or {}).get("command", "").strip() == os.environ["CMD"]
    print(("allow " if ok else "deny ") + r["id"])
' 2>/dev/null)"
    case "$next" in
      "allow "*)
        id="${next#allow }"
        echo "$id" >"$WORK/approved-id"
        curl -fs -b "$CK" -H 'Content-Type: application/json' -H "Origin: $HUB" -d '{"action":"allow","note":"冒烟自动批准"}' \
          "$HUB/api/requests/$id/decide" >/dev/null || echo "批准 $id 失败" >&2
        exit 0
        ;;
      "deny "*)
        id="${next#deny }"
        echo "拒绝了意料之外的待审批 $id" >&2
        echo "$id" >>"$WORK/denied-ids"
        curl -fs -b "$CK" -H 'Content-Type: application/json' -H "Origin: $HUB" -d '{"action":"deny","note":"冒烟只批准 git push origin main"}' \
          "$HUB/api/requests/$id/decide" >/dev/null || echo "拒绝 $id 失败" >&2
        ;;
    esac
    sleep 0.2
  done
) &
APPROVER_PID=$!

# env -i 隔离掉宿主应用注入的变量，但 USER/LOGNAME/TMPDIR/SHELL 必须保留，否则 claude 找不到钥匙串里的登录态。
# --setting-sources project,local：钩子在 .claude/settings.local.json（local 来源）；不加 user（见文件开头）。
(cd "$WORK/proj" && "${TO[@]}" env -i HOME="$HOME" PATH="$PATH" USER="${USER:-$(id -un)}" LOGNAME="${LOGNAME:-$(id -un)}" \
  TMPDIR="${TMPDIR:-/tmp}" SHELL="${SHELL:-/bin/sh}" LANG="${LANG:-C}" TERM=dumb RAILSE_HOME="$RAILSE_HOME" \
  claude -p "Run exactly this shell command with the Bash tool and nothing else: $CMD . Then reply DONE." \
  --setting-sources project,local --output-format json </dev/null >"$WORK/claude.json")
CLAUDE_EXIT=$?
echo "== claude 输出（退出码 $CLAUDE_EXIT）=="
cat "$WORK/claude.json"
echo

# claude 已结束：钩子只在拿到决定后才放行，还没批准的这时也不会再来了
if kill -0 "$APPROVER_PID" 2>/dev/null; then
  kill "$APPROVER_PID" 2>/dev/null || true
fi
wait "$APPROVER_PID" 2>/dev/null || true
APPROVER_PID=""
if [ -s "$WORK/denied-ids" ]; then
  echo "== 拒绝了意料之外的待审批: $(tr '\n' ' ' <"$WORK/denied-ids")=="
fi
id="$(cat "$WORK/approved-id" 2>/dev/null || true)"
[ -n "$id" ] || fail "hub 里没出现 $CMD 的待审批（claude 退出码 $CLAUDE_EXIT）"

# 送达回执是守护进程异步报给 hub 的：最多等 5 秒
state=""
for _ in $(seq 1 50); do
  state="$(curl -fs -b "$CK" "$HUB/api/requests/$id" | python3 -c '
import json, sys
r = json.load(sys.stdin)["request"]
print(r["state"], r["decided_by"], "delivered" if r["delivered_at"] else "undelivered")
' 2>/dev/null)"
  [ "$state" = "approved user delivered" ] && break
  sleep 0.1
done
echo "== hub 里的请求 $id: ${state:-（读不到）} =="
audit="$(ID="$id" python3 -c '
import json, os, sys
for line in open(sys.argv[1], encoding="utf-8"):
    e = json.loads(line)
    if e.get("request_id") == os.environ["ID"] and e.get("state") == "approved":
        print(e.get("state"), e.get("decided_by"))
        break
' "$RAILSE_HOME/audit.jsonl" 2>/dev/null)"
echo "== 本机审计: ${audit:-（没有这条请求的 approved）} =="
if [ "$state" = "approved user delivered" ] && [ "$audit" = "approved user" ]; then
  PASSED=1
  echo "OK: 请求经 hub 批准并送回钩子"
  exit 0
fi
fail "状态不对：hub 里应为 approved user delivered，本机审计应有该请求的 approved user"
