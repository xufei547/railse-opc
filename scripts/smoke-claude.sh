#!/usr/bin/env bash
# 真实 Claude Code 冒烟：起守护进程、装项目钩子、让 claude -p 跑一条 touch，看审计。
# 需要本机已装 claude 且已登录。不进 CI。
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d /tmp/railse-smoke.XXXX)"
export RAILSE_HOME="$WORK/home"
command -v claude >/dev/null || { echo "FAIL: 没有找到 claude"; exit 1; }
cd "$ROOT" && go build -o "$WORK/railse" ./cmd/railse || exit 1
"$WORK/railse" agent run --foreground &
AGENT=$!
trap 'kill $AGENT 2>/dev/null || true' EXIT
for _ in $(seq 1 50); do [ -S "$RAILSE_HOME/agent.sock" ] && break; sleep 0.1; done
[ -S "$RAILSE_HOME/agent.sock" ] || { echo "FAIL: 守护进程没有起来"; exit 1; }
mkdir -p "$WORK/proj" && (cd "$WORK/proj" && git init -q)
"$WORK/railse" setup hooks --project "$WORK/proj" --claude --bin "$WORK/railse" || exit 1
# env -i 隔离掉宿主应用注入的变量，但 USER/LOGNAME/TMPDIR/SHELL 必须保留，否则 claude 找不到钥匙串里的登录态。
(cd "$WORK/proj" && env -i HOME="$HOME" PATH="$PATH" USER="${USER:-$(id -un)}" LOGNAME="${LOGNAME:-$(id -un)}" \
  TMPDIR="${TMPDIR:-/tmp}" SHELL="${SHELL:-/bin/sh}" LANG="${LANG:-C}" TERM=dumb RAILSE_HOME="$RAILSE_HOME" \
  claude -p "Run exactly this shell command with the Bash tool and nothing else: touch smoke-proof.txt . Then reply DONE." \
  --setting-sources project --output-format json < /dev/null > "$WORK/claude.json")
CLAUDE_EXIT=$?
echo "== claude 输出（退出码 $CLAUDE_EXIT）=="; cat "$WORK/claude.json"; echo
echo "== 审计 =="; cat "$RAILSE_HOME/audit.jsonl"
if [ -f "$WORK/proj/smoke-proof.txt" ] && grep -q '"state":"auto_allowed"' "$RAILSE_HOME/audit.jsonl"; then
  echo "OK: 文件已创建且审计有 auto_allowed，钩子放行生效"; exit 0
fi
echo "FAIL: 文件未创建或审计里没有 auto_allowed（临时目录保留在 $WORK）"; exit 1
