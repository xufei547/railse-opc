#!/usr/bin/env bash
# 网页端到端：构建网页与二进制，Playwright 起真实的 hub 与守护进程（临时目录、随机端口）跑 web/e2e 里的用例。
# 不碰真实的 ~/.railse、~/.railse-hub 与任何 CLI 的配置：各进程的数据目录与 HOME 都在临时目录里，
# 钩子用 railse hook claude --socket 直接调用，不装进 ~/.claude。额外参数原样传给 playwright test（如 --headed、-g 关键字）。
# 退出时（成功、失败或被中断）停掉这次起的所有 railse 进程并删掉临时目录；失败时先打印 hub 与守护进程日志的末尾，
# 并把临时目录下的全部 *.log、*.stderr 按原相对路径复制到 web/test-results/logs/（CI 失败时连同 Playwright 的结果一起上传）。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Unix socket 路径有长度上限（macOS 104 字节）：TMPDIR 太长时退回 /tmp
parent="${TMPDIR:-/tmp}"
if [ "${#parent}" -gt 60 ]; then parent=/tmp; fi
WORK="$(mktemp -d "${parent%/}/railse-e2e.XXXXXX")"
BIN="$WORK/railse"

# 这次运行起的进程（hub、守护进程、钩子）命令行都以 $BIN 开头，别的进程不会带这个临时路径。
# 先 TERM，至多等 5 秒，还在的 KILL。
stop_children() {
  pkill -TERM -f -- "$BIN" 2>/dev/null || true
  for _ in $(seq 1 50); do
    pgrep -f -- "$BIN" >/dev/null 2>&1 || return 0
    sleep 0.1
  done
  pkill -KILL -f -- "$BIN" 2>/dev/null || true
}

# 失败时留下这次运行的日志：$WORK 下的 *.log、*.stderr 按相对路径复制到 web/test-results/logs/（先清掉上一次留下的）。
# 在 Playwright 跑完之后才复制：它开跑时会清空 web/test-results。复制失败不影响退出码与清理。
save_logs() {
  local dest="$ROOT/web/test-results/logs" f rel n=0
  rm -rf "$dest" 2>/dev/null || true
  while IFS= read -r -d '' f; do
    rel="${f#"$WORK"/}"
    mkdir -p "$dest/$(dirname "$rel")" && cp "$f" "$dest/$rel" && n=$((n + 1))
  done < <(find "$WORK" -type f \( -name '*.log' -o -name '*.stderr' \) -print0 2>/dev/null)
  if [ "$n" -gt 0 ]; then
    echo "--- 这次运行的 $n 个日志已复制到 web/test-results/logs/ ---" >&2
  else
    echo "--- 这次运行没有留下日志（失败在起 hub 之前） ---" >&2
  fi
}

cleanup() {
  local code=$?
  trap - EXIT INT TERM
  stop_children
  if [ "$code" -ne 0 ]; then
    for f in "$WORK/h/hub.log" "$WORK/hub.stderr" "$WORK/a/agent.log" "$WORK/agent.stderr"; do
      if [ -s "$f" ]; then
        echo "--- ${f#"$WORK"/} 末尾 ---" >&2
        tail -n 30 "$f" >&2
      fi
    done
    save_logs
  fi
  rm -rf "$WORK"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cd "$ROOT/web"
npm ci
npm run build # 产物进 internal/hub/webui/dist，下面的二进制把它嵌进去
(cd "$ROOT" && go build -o "$BIN" ./cmd/railse)
npx playwright install chromium
RAILSE_E2E_BIN="$BIN" RAILSE_E2E_DIR="$WORK" npx playwright test "$@"
