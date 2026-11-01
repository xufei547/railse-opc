#!/bin/sh
# 英文冒烟：RAILSE_LANG=en 下跑一批不需要配置、不改系统的命令，输出里不得有中日韩字符。
# 用法：scripts/english-smoke.sh <railse 二进制路径>
# 环境全部封在临时目录里（RAILSE_HOME、RAILSE_HUB_HOME、HOME、工作目录），PATH 只留 /usr/bin:/bin，
# 免得 doctor 碰到开发机上真的 tailscale、claude、codex。这里只放"只打印用法或只读状态"的命令：
# 不跑 setup all/hooks/autostart 的执行路径，不跑 agent run、hub run。
set -eu

bin="${1:?usage: english-smoke.sh <railse binary>}"
case "$bin" in /*) ;; *) bin="$PWD/$bin" ;; esac
[ -x "$bin" ] || { echo "not an executable: $bin" >&2; exit 1; }

# 临时目录用短路径：守护进程的 socket 路径超过 104 字节（macOS 的 $TMPDIR 很长）会让 doctor 报 "invalid argument"，
# 而不是英文用户真会看到的"连不上守护进程"
tmp="$(mktemp -d /tmp/rs-smoke.XXXXXX 2>/dev/null || mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
trap 'exit 130' INT TERM
mkdir -p "$tmp/home" "$tmp/cwd"

# has_cjk：输入里有中日韩字符或全角符号就返回 0。按字节查，不依赖 locale
# （UTF-8 首字节 E3-E9 即 U+3000-U+9FFF，EF BC/BD 即全角符号 U+FF00-U+FF7F；═ → … 的首字节是 E2，不会命中）
has_cjk() { LC_ALL=C grep -q -e "$(printf '[\343-\351]')" -e "$(printf '\357[\274\275]')"; }

# 反向自检：检查函数自己失灵的话，下面的"无中文"断言会静默通过
printf '%s\n' '未知子命令（x）' | has_cjk || { echo "self-check failed: Chinese sample not detected" >&2; exit 1; }
printf '%s\n' '：' | has_cjk || { echo "self-check failed: fullwidth sample not detected" >&2; exit 1; }
if printf '%s\n' 'plain ═══ a → b … done' | has_cjk; then
  echo "self-check failed: English sample with box/arrow/ellipsis was flagged" >&2
  exit 1
fi

unset LC_ALL LC_MESSAGES LANG XDG_CONFIG_HOME XDG_STATE_HOME XDG_DATA_HOME XDG_RUNTIME_DIR
# 开发机上可能留着的 RAILSE_*（RAILSE_CODEX_NOTIFY、RAILSE_DOCTOR_SLOW_CLI、RAILSE_TEST_* 等）一律清掉
for v in $(env | sed -n 's/^\(RAILSE_[A-Za-z0-9_]*\)=.*/\1/p'); do unset "$v"; done
export PATH=/usr/bin:/bin HOME="$tmp/home" RAILSE_LANG=en RAILSE_HOME="$tmp/railse" RAILSE_HUB_HOME="$tmp/hub"
cd "$tmp/cwd"

# run <允许的退出码，空格分隔> <期望的英文子串> <railse 的参数…>：stdin 不接终端。
# 失败（打印输出与命令，退出 1）：退出码不在允许列表；输出里有 Go panic 的痕迹（panic 的退出码正好是 2）；
# 输出里没有期望子串（证明走到了那条命令自己的输出，不是被当成未知命令）；输出里有中日韩字符
run() {
  allowed="$1"; want="$2"; shift 2
  if out="$("$bin" "$@" 2>&1 </dev/null)"; then code=0; else code=$?; fi
  case " $allowed " in
    *" $code "*) ;;
    *) printf '%s\n' "$out"; echo "FAIL: exit $code not in [$allowed]: railse $*" >&2; exit 1 ;;
  esac
  case "$out" in
    *"panic:"*|*"goroutine "*) printf '%s\n' "$out"; echo "FAIL: panic in the output of: railse $*" >&2; exit 1 ;;
  esac
  case "$out" in
    *"$want"*) ;;
    *) printf '%s\n' "$out"; echo "FAIL: expected \"$want\" in the output of: railse $*" >&2; exit 1 ;;
  esac
  if printf '%s\n' "$out" | has_cjk; then
    printf '%s\n' "$out"; echo "FAIL: Chinese in the output of: railse $*" >&2; exit 1
  fi
}

run 0 "Usage: railse <command>" --help
run 0 "railse " version
run 0 "Current language: en" lang
run 2 "Usage: railse <command>"               # 无参数：打印用法
run 2 "Unknown command" bogus
run 2 "railse setup" setup                    # stdin 不是终端：只打印用法
run 2 "railse setup all" setup all --help
run 2 "Usage of setup hooks" setup hooks --help
run 2 "railse setup autostart" setup autostart --help
run 2 "railse setup codex-notify" setup codex-notify --help
run 2 "railse agent run" agent
run 1 "can't connect to the daemon" agent status   # 守护进程没跑：连不上
run 2 "railse hub run" hub
run 2 "railse policy eval" policy
run 0 "railse hook claude" hook               # 无参数：只打印用法
run 1 "passed," doctor
run 1 '"checks"' doctor --json

echo "english smoke ok"
