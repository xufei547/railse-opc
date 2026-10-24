#!/usr/bin/env bash
# 本地测试 install.sh：用当前代码造一个假发布（railse_0.0.1_<系统>_<架构>.tar.gz 与 checksums.txt），
# 在 127.0.0.1 的随机端口起 python3 -m http.server 提供下载，走完下载、校验、安装；再验证这些情况都失败退出且不装：
# 校验和不符、checksums.txt 里只有名字相近的行、版本号不合法、安装目录不可写、不支持的平台（PATH 里放假 uname）。
# 另外验证：不给版本时从 checksums.txt 取版本号、版本号前带 v、覆盖安装、/usr/local/bin 不可写时退到 ~/.local/bin
# （假 HOME；/usr/local/bin 可写时跳过这一项，免得装进真目录）、每次运行后 install.sh 的临时目录都删干净了。
# 用 sh 跑一遍，本机有 dash 再用 dash 跑一遍。只用临时目录；退出时（成功、失败或中断）停掉 HTTP 服务并删掉临时目录。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# 调用方环境里的 RAILSE_* 不能漏进被测的 install.sh
unset RAILSE_REPO RAILSE_VERSION RAILSE_INSTALL_DIR RAILSE_BASE_URL
# 本机设了 HTTP 代理时，访问 127.0.0.1 不走代理
export NO_PROXY="127.0.0.1,localhost${NO_PROXY:+,$NO_PROXY}" no_proxy="127.0.0.1,localhost${no_proxy:+,$no_proxy}"

tmp="$(mktemp -d)"
HTTP_PID=""
cleanup() {
  if [ -n "$HTTP_PID" ]; then
    kill "$HTTP_PID" 2>/dev/null || true
    wait "$HTTP_PID" 2>/dev/null || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "本机系统 $(uname -s) 不在 install.sh 支持的范围" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "本机架构 $(uname -m) 不在 install.sh 支持的范围" ;;
esac
[ "$os/$arch" != darwin/amd64 ] || fail "install.sh 不支持 macOS Intel，本机跑不了这个测试"

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

# 假发布：当前代码编出的二进制（版本 0.0.1）打成本平台的包；checksums.txt 照 goreleaser 的格式，另两个平台的行是占位，
# 第一行另放一个别的平台、别的版本的行（不给版本时要取本平台那一行的版本号，不能取第一行）
VERSION=0.0.1
FILE="railse_${VERSION}_${os}_${arch}.tar.gz"
mkdir -p "$tmp/rel" "$tmp/pkg" "$tmp/log"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$tmp/pkg/railse" ./cmd/railse
cp LICENSE README.md "$tmp/pkg/"
COPYFILE_DISABLE=1 tar -czf "$tmp/rel/$FILE" -C "$tmp/pkg" railse LICENSE README.md
good_sum="$(cd "$tmp/rel" && sha256 "$FILE" | awk '{print $1}')"
dummy_sum="$(printf 'placeholder' | sha256 | awk '{print $1}')"
{
  printf '%s  %s\n' "$dummy_sum" "railse_9.9.9_plan9_amd64.tar.gz"
  for p in darwin_arm64 linux_amd64 linux_arm64; do
    if [ "$p" = "${os}_${arch}" ]; then
      printf '%s  %s\n' "$good_sum" "$FILE"
    else
      printf '%s  %s\n' "$dummy_sum" "railse_${VERSION}_$p.tar.gz"
    fi
  done
} >"$tmp/checksums.good"
cp "$tmp/checksums.good" "$tmp/rel/checksums.txt"

# 改坏校验和：每行首字符确定地换掉（是 0 换成 1，否则换成 0）
awk '{ c = substr($0, 1, 1); printf "%s%s\n", (c == "0" ? "1" : "0"), substr($0, 2) }' "$tmp/checksums.good" >"$tmp/checksums.tampered"
cmp -s "$tmp/checksums.good" "$tmp/checksums.tampered" && fail "改坏的 checksums.txt 与原来一样"
# 只有名字相近的行（哈希都是对的）：按子串或正则找会误中，按文件名字段完全相等找不到
{
  printf '%s  %s\n' "$good_sum" "railse_0x0x1_${os}_${arch}xtarxgz"
  printf '%s  %s\n' "$good_sum" "railse_${VERSION}_${os}_${arch}.tar.gz.sig"
  printf '%s  other %s\n' "$good_sum" "$FILE"
  printf '%s  x%s\n' "$good_sum" "$FILE"
} >"$tmp/checksums.decoy"

# HTTP 服务：随机端口，只绑 127.0.0.1；端口被抢（进程退出）就换一个重试，轮询到能取到 checksums.txt 为止
port=""
for _ in 1 2 3 4 5; do
  p="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
  python3 -m http.server "$p" --bind 127.0.0.1 --directory "$tmp/rel" >>"$tmp/log/http.log" 2>&1 &
  HTTP_PID=$!
  for _ in $(seq 1 100); do
    if curl -fsS --max-time 2 -o /dev/null "http://127.0.0.1:$p/checksums.txt" 2>/dev/null; then
      port="$p"
      break
    fi
    kill -0 "$HTTP_PID" 2>/dev/null || break
    sleep 0.1
  done
  [ -z "$port" ] || break
  kill "$HTTP_PID" 2>/dev/null || true
  wait "$HTTP_PID" 2>/dev/null || true
  HTTP_PID=""
done
[ -n "$port" ] || { cat "$tmp/log/http.log" >&2; fail "本地 HTTP 服务起不来"; }
URL="http://127.0.0.1:$port"

# 假 uname：按 FAKE_S、FAKE_M 回答 -s、-m
mkdir -p "$tmp/fakebin"
cat >"$tmp/fakebin/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in
  -m) echo "$FAKE_M" ;;
  *) echo "$FAKE_S" ;;
esac
EOF
chmod 755 "$tmp/fakebin/uname"

shells="sh"
if command -v dash >/dev/null 2>&1; then shells="sh dash"; fi

for shell in $shells; do
  w="$tmp/$shell"
  itmp="$w/itmp" # install.sh 的 TMPDIR：每次跑完都应当是空的
  mkdir -p "$itmp"

  # run_install <名字> [变量=值 …]：用 $shell 跑 install.sh，标准输出与错误存到 $tmp/log/<shell>-<名字>.out/.err
  run_install() {
    local name="$1"
    shift
    env TMPDIR="$itmp" "$@" "$shell" "$ROOT/install.sh" >"$tmp/log/$shell-$name.out" 2>"$tmp/log/$shell-$name.err"
  }
  show() {
    echo "--- $shell $1 标准输出 ---" >&2
    cat "$tmp/log/$shell-$1.out" >&2
    echo "--- $shell $1 标准错误 ---" >&2
    cat "$tmp/log/$shell-$1.err" >&2
  }
  check_itmp() {
    [ -z "$(ls -A "$itmp")" ] || fail "$shell $1：install.sh 的临时目录没删：$(ls -A "$itmp")"
  }
  expect_ok() {
    local name="$1"
    shift
    run_install "$name" "$@" || { show "$name"; fail "$shell $name：install.sh 应当成功"; }
    grep -qF "sha256 校验通过" "$tmp/log/$shell-$name.out" || { show "$name"; fail "$shell $name：没有打印校验通过"; }
    check_itmp "$name"
  }
  expect_fail() {
    local name="$1" want="$2"
    shift 2
    if run_install "$name" "$@"; then
      show "$name"
      fail "$shell $name：install.sh 应当失败"
    fi
    grep -qF -- "$want" "$tmp/log/$shell-$name.err" || { show "$name"; fail "$shell $name：错误信息里没有“$want”"; }
    check_itmp "$name"
  }
  check_installed() {
    local bin="$1" out
    [ -f "$bin" ] && [ -x "$bin" ] || fail "$shell：$bin 没装上或不可执行"
    out="$("$bin" version)" || fail "$shell：$bin version 运行失败"
    [[ "$out" == *railse* && "$out" == *"$VERSION"* ]] || fail "$shell：$bin version 输出不对：$out"
    # 只该有 railse 本身，没有残留的临时文件
    [ "$(ls -A "$(dirname "$bin")")" = railse ] || fail "$shell：安装目录里有多余的文件：$(ls -A "$(dirname "$bin")")"
  }

  # 1. 指定版本，安装目录不存在时新建
  dir="$w/bin"
  expect_ok explicit RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  check_installed "$dir/railse"
  # 2. 覆盖安装；版本号前带 v
  expect_ok v-prefix RAILSE_BASE_URL="$URL" RAILSE_VERSION="v$VERSION" RAILSE_INSTALL_DIR="$dir"
  check_installed "$dir/railse"
  # 3. 不给版本：从 checksums.txt 里本平台那一行的文件名取版本号
  rm -f "$dir/railse"
  expect_ok latest RAILSE_BASE_URL="$URL" RAILSE_INSTALL_DIR="$dir"
  check_installed "$dir/railse"

  # 4. 校验和不符：拒装，不留二进制
  cp "$tmp/checksums.tampered" "$tmp/rel/checksums.txt"
  rm -f "$dir/railse"
  expect_fail tampered "校验和不符" RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  [ ! -e "$dir/railse" ] || fail "$shell tampered：校验失败不应留下二进制"
  [ -z "$(ls -A "$dir")" ] || fail "$shell tampered：安装目录里有残留：$(ls -A "$dir")"
  # 5. checksums.txt 里只有名字相近的行：按文件名字段完全相等找不到，拒装
  cp "$tmp/checksums.decoy" "$tmp/rel/checksums.txt"
  expect_fail decoy "checksums.txt 里没有 $FILE" RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  [ ! -e "$dir/railse" ] || fail "$shell decoy：不应留下二进制"
  cp "$tmp/checksums.good" "$tmp/rel/checksums.txt"

  # 6. 版本号不合法：在下载之前就失败
  for bad in "0.0.1/../../x" "latest" "1.2" "0.0.1_x" "0.0.1 "; do
    expect_fail bad-version "RAILSE_VERSION 不合法" RAILSE_BASE_URL="$URL" RAILSE_VERSION="$bad" RAILSE_INSTALL_DIR="$dir"
  done
  [ ! -e "$dir/railse" ] || fail "$shell bad-version：不应留下二进制"

  # 7. 不支持的平台（假 uname）
  expect_fail freebsd "不支持的系统 FreeBSD" PATH="$tmp/fakebin:$PATH" FAKE_S=FreeBSD FAKE_M=x86_64 \
    RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  expect_fail armv7 "不支持的架构 armv7l" PATH="$tmp/fakebin:$PATH" FAKE_S=Linux FAKE_M=armv7l \
    RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  expect_fail darwin-intel "不提供 macOS Intel" PATH="$tmp/fakebin:$PATH" FAKE_S=Darwin FAKE_M=x86_64 \
    RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  [ ! -e "$dir/railse" ] || fail "$shell 不支持的平台：不应留下二进制"

  # 8. 显式给的安装目录不可写（root 下 -w 恒真，跳过）
  if [ "$(id -u)" != 0 ]; then
    mkdir -p "$w/ro"
    chmod 555 "$w/ro"
    expect_fail readonly "不可写" RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$w/ro"
    [ -z "$(ls -A "$w/ro")" ] || fail "$shell readonly：不应写进任何文件"
    chmod 755 "$w/ro"
  fi

  # 9. 不给安装目录且 /usr/local/bin 不可写：装到假 HOME 的 ~/.local/bin（新建 0755），并提示它不在 PATH 里
  if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    echo "跳过（$shell）：/usr/local/bin 可写，不测退到 ~/.local/bin，免得装进真目录"
  else
    home="$w/home"
    mkdir -p "$home"
    expect_ok fallback HOME="$home" RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION"
    check_installed "$home/.local/bin/railse"
    [ -n "$(find "$home/.local/bin" -maxdepth 0 -perm 755)" ] || fail "$shell fallback：~/.local/bin 不是 0755"
    grep -qF "不在 PATH 里" "$tmp/log/$shell-fallback.out" || { show fallback; fail "$shell fallback：没有提示加 PATH"; }
    echo "ok（$shell）：/usr/local/bin 不可写时退到 ~/.local/bin"
  fi

  echo "ok（$shell）：正常安装、覆盖、取最新版本、校验和不符、相近文件名、非法版本号、不可写目录、不支持的平台"
done

echo "OK: install.sh 下载、校验、安装与拒装都正确"
