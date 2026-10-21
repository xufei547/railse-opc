#!/usr/bin/env bash
# 真实 Claude Code 冒烟：起守护进程、装项目钩子、让 claude -p 跑一条 touch，看审计。
# 需要本机已装 claude 且已登录。不进 CI。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d /tmp/railse-smoke.XXXX)"
export RAILSE_HOME="$WORK/home"
cd "$ROOT" && go build -o "$WORK/railse" ./cmd/railse
"$WORK/railse" agent run --foreground &
AGENT=$!
trap 'kill $AGENT 2>/dev/null || true' EXIT
sleep 1
mkdir -p "$WORK/proj" && (cd "$WORK/proj" && git init -q)
"$WORK/railse" setup hooks --project "$WORK/proj" --claude --bin "$WORK/railse"
(cd "$WORK/proj" && env -i HOME="$HOME" PATH="$PATH" LANG="${LANG:-C}" TERM=dumb RAILSE_HOME="$RAILSE_HOME" \
  claude -p "Run exactly this shell command with the Bash tool and nothing else: touch smoke-proof.txt . Then reply DONE." \
  --setting-sources project --output-format json > "$WORK/claude.json")
echo "== claude 输出 =="; cat "$WORK/claude.json"; echo
echo "== 审计 =="; cat "$RAILSE_HOME/audit.jsonl"
test -f "$WORK/proj/smoke-proof.txt" && echo "OK: 文件已创建，钩子放行生效" || echo "FAIL: 文件未创建"
