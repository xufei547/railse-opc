#!/usr/bin/env bash
# 本地测试 install.sh：用当前代码造一个假发布（railse_0.0.1_<系统>_<架构>.tar.gz 与 checksums.txt），
# 在 127.0.0.1 的随机端口起 python3 -m http.server 提供下载，走完下载、校验、安装。再验证这些情况都失败退出、不装、不留空目录：
# 校验和不符、checksums.txt 里只有名字相近的行、checksums.txt 或包 404、最新版流程解析出非法版本号、包里的 railse 是
# 符号链接 / 目录 / 硬链接、没有 sha256sum 与 shasum、版本号不合法（含只有一个 v）、RAILSE_BASE_URL 不是 http(s)://、
# 安装目录不可写或不是目录、不支持的平台（PATH 里放假 uname）、环境里继承了 install.sh 清理用的变量名（stage、tmp）。
# 另外验证：不给版本时从 checksums.txt 本平台那一行取版本号、版本号前带 v、覆盖安装、明文 http 且不是本机时警告、
# /usr/local/bin 不可写时退到 ~/.local/bin（假 HOME；/usr/local/bin 可写时跳过，免得装进真目录）、
# 每次运行后 install.sh 的临时目录都删干净了、在工作目录里没留下任何东西。
# 用 sh 跑一遍，本机有 dash 再用 dash 跑一遍。只用 127.0.0.1 与临时目录；退出时（成功、失败或中断）停掉 HTTP 服务并删掉临时目录。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# 调用方环境里的 RAILSE_* 不能漏进被测的 install.sh；只访问 127.0.0.1，不走代理
unset RAILSE_REPO RAILSE_VERSION RAILSE_INSTALL_DIR RAILSE_BASE_URL
unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY all_proxy ALL_PROXY
export NO_PROXY="127.0.0.1,localhost" no_proxy="127.0.0.1,localhost"

tmp="$(mktemp -d)"
HTTP_PID=""
cleanup() {
  if [ -n "$HTTP_PID" ]; then
    kill "$HTTP_PID" 2>/dev/null || true
    wait "$HTTP_PID" 2>/dev/null || true
  fi
  chmod -R u+w "$tmp" 2>/dev/null || true
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
sum_of() { sha256 "$1" | awk '{print $1}'; }

# 假发布：当前代码编出的二进制（版本 0.0.1）打成本平台的包；checksums.txt 照 goreleaser 的格式，另两个平台的行是占位，
# 第一行另放一个别的平台、别的版本的行（不给版本时要取本平台那一行的版本号，不能取第一行）
VERSION=0.0.1
FILE="railse_${VERSION}_${os}_${arch}.tar.gz"
rel="$tmp/rel"
mkdir -p "$rel" "$tmp/pkg" "$tmp/log"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$tmp/pkg/railse" ./cmd/railse
cp LICENSE README.md "$tmp/pkg/"
COPYFILE_DISABLE=1 tar -czf "$rel/$FILE" -C "$tmp/pkg" railse LICENSE README.md
good_sum="$(sum_of "$rel/$FILE")"
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
cp "$tmp/checksums.good" "$rel/checksums.txt"

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

# 子目录里的坏发布（各自当作一个 RAILSE_BASE_URL）：
#   nochk/  只有包，没有 checksums.txt；notar/  只有 checksums.txt，没有包
#   latestbad/、latestdots/  checksums.txt 里本平台那一行的“版本号”是 shell 注入或路径穿越
#   symlink/、dir/、hardlink/  包里的 railse 是符号链接、目录、硬链接（checksums.txt 是对的）
mkdir -p "$rel/nochk" "$rel/notar" "$rel/latestbad" "$rel/latestdots"
cp "$rel/$FILE" "$rel/nochk/"
cp "$tmp/checksums.good" "$rel/notar/checksums.txt"
# shellcheck disable=SC2016 # ${IFS} 要原样写进文件
printf '%s  %s\n' "$dummy_sum" 'railse_0.0.1;touch${IFS}pwned_'"${os}_${arch}.tar.gz" >"$rel/latestbad/checksums.txt"
printf '%s  %s\n' "$dummy_sum" "railse_0.0.1/../../x_${os}_${arch}.tar.gz" >"$rel/latestdots/checksums.txt"
cat >"$tmp/mkbad.py" <<'EOF'
import io, sys, tarfile
out, kind = sys.argv[1], sys.argv[2]
with tarfile.open(out, "w:gz") as t:
    def add(name, typ, data=b"", link=""):
        ti = tarfile.TarInfo(name)
        ti.type, ti.mode, ti.linkname = typ, 0o755, link
        ti.size = len(data) if typ == tarfile.REGTYPE else 0
        t.addfile(ti, io.BytesIO(data) if typ == tarfile.REGTYPE else None)
    if kind == "symlink":
        add("railse", tarfile.SYMTYPE, link="/bin/sh")
    elif kind == "dir":
        add("railse", tarfile.DIRTYPE)
        add("railse/x", tarfile.REGTYPE, b"#!/bin/sh\necho railse 0.0.1\n")
    elif kind == "hardlink":
        add("railse", tarfile.LNKTYPE, link="/etc/hosts")
    add("LICENSE", tarfile.REGTYPE, b"x")
EOF
for kind in symlink dir hardlink; do
  mkdir -p "$rel/$kind"
  python3 "$tmp/mkbad.py" "$rel/$kind/$FILE" "$kind"
  printf '%s  %s\n' "$(sum_of "$rel/$kind/$FILE")" "$FILE" >"$rel/$kind/checksums.txt"
done

# HTTP 服务：随机端口，只绑 127.0.0.1；端口被抢（进程退出）就换一个重试，轮询到能取到 checksums.txt 为止
port=""
for _ in 1 2 3 4 5; do
  p="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
  python3 -m http.server "$p" --bind 127.0.0.1 --directory "$rel" >>"$tmp/log/http.log" 2>&1 &
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

# path_without <目录> <命令名…>：在 <目录> 里造一个去掉这些命令的 PATH，PATH 里其余的可执行文件都链接过来（同名取先出现的）
path_without() {
  local out="$1" d f n x skip
  shift
  mkdir -p "$out"
  IFS=: read -r -a path_dirs <<<"$PATH"
  for d in "${path_dirs[@]}"; do
    [ -n "$d" ] && [ -d "$d" ] || continue
    for f in "$d"/*; do
      n="${f##*/}"
      skip=0
      for x in "$@"; do [ "$n" != "$x" ] || skip=1; done
      [ "$skip" = 0 ] || continue
      if [ -e "$out/$n" ] || [ -L "$out/$n" ]; then continue; fi
      if [ -f "$f" ] && [ -x "$f" ]; then ln -s "$f" "$out/$n"; fi
    done
  done
}
path_without "$tmp/nosha" sha256sum shasum
# 本机有 wget 时再造一个没有 curl 的 PATH，走 install.sh 的 wget 分支（CI 的 ubuntu 有 wget）
has_wget=0
if command -v wget >/dev/null 2>&1; then
  has_wget=1
  path_without "$tmp/nocurl" curl
fi

shells="sh"
if command -v dash >/dev/null 2>&1; then shells="sh dash"; fi

for shell in $shells; do
  w="$tmp/$shell"
  itmp="$w/itmp" # install.sh 的 TMPDIR：每次跑完都应当是空的
  cwd="$w/cwd"   # install.sh 的工作目录：始终应当是空的（例如注入的 touch pwned 不能得逞）
  mkdir -p "$itmp" "$cwd"

  # run_install <名字> [变量=值 …]：在 $cwd 里用 $shell 跑 install.sh，标准输出与错误存到 $tmp/log/<shell>-<名字>.out/.err
  run_install() {
    local name="$1"
    shift
    (cd "$cwd" && env TMPDIR="$itmp" "$@" "$shell" "$ROOT/install.sh") >"$tmp/log/$shell-$name.out" 2>"$tmp/log/$shell-$name.err"
  }
  show() {
    echo "--- $shell $1 标准输出 ---" >&2
    cat "$tmp/log/$shell-$1.out" >&2
    echo "--- $shell $1 标准错误 ---" >&2
    cat "$tmp/log/$shell-$1.err" >&2
  }
  check_clean() {
    [ -z "$(ls -A "$itmp")" ] || fail "$shell $1：install.sh 的临时目录没删：$(ls -A "$itmp")"
    [ -z "$(ls -A "$cwd")" ] || fail "$shell $1：工作目录里多了东西：$(ls -A "$cwd")"
  }
  expect_ok() {
    local name="$1"
    shift
    run_install "$name" "$@" || { show "$name"; fail "$shell $name：install.sh 应当成功"; }
    grep -qF "sha256 校验通过" "$tmp/log/$shell-$name.out" || { show "$name"; fail "$shell $name：没有打印校验通过"; }
    check_clean "$name"
  }
  expect_fail() {
    local name="$1" want="$2"
    shift 2
    if run_install "$name" "$@"; then
      show "$name"
      fail "$shell $name：install.sh 应当失败"
    fi
    grep -qF -- "$want" "$tmp/log/$shell-$name.err" || { show "$name"; fail "$shell $name：错误信息里没有“$want”"; }
    check_clean "$name"
  }
  check_installed() {
    local bin="$1" out
    [ -f "$bin" ] && [ -x "$bin" ] || fail "$shell：$bin 没装上或不可执行"
    out="$("$bin" version)" || fail "$shell：$bin version 运行失败"
    [[ "$out" == *railse* && "$out" == *"$VERSION"* ]] || fail "$shell：$bin version 输出不对：$out"
    # 只该有 railse 本身，没有残留的临时文件
    [ "$(ls -A "$(dirname "$bin")")" = railse ] || fail "$shell：安装目录里有多余的文件：$(ls -A "$(dirname "$bin")")"
  }
  # 失败后：安装目录要么不存在，要么是空的
  check_nothing() {
    local d="$1" what="$2"
    if [ -e "$d" ]; then
      [ -z "$(ls -A "$d")" ] || fail "$shell $what：安装目录里有残留：$(ls -A "$d")"
    fi
  }

  # 1. 指定版本，安装目录不存在时新建；本机 http 不警告
  dir="$w/bin"
  expect_ok explicit RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  check_installed "$dir/railse"
  if grep -qF "警告" "$tmp/log/$shell-explicit.err"; then show explicit; fail "$shell explicit：127.0.0.1 不该警告"; fi
  # 下一步里装系统服务的命令用实际装好的路径；不在 /usr/local/bin 时提示 hub 机器用 | sudo sh 装到系统目录
  grep -qF "sudo $dir/railse setup autostart --hub --system -- " "$tmp/log/$shell-explicit.out" || { show explicit; fail "$shell explicit：下一步没用实际安装路径"; }
  grep -qF "install.sh | sudo sh" "$tmp/log/$shell-explicit.out" || { show explicit; fail "$shell explicit：没提示 hub 机器装到系统目录"; }
  # 安装路径带空格与单引号：照抄得了（单引号括起来，内部的 ' 写成 '\''）
  odd="$w/my bin's"
  expect_ok odd-dir RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$odd"
  check_installed "$odd/railse"
  grep -qF "sudo '$w/my bin'\\''s/railse' setup autostart --hub --system -- " "$tmp/log/$shell-odd-dir.out" || { show odd-dir; fail "$shell odd-dir：下一步里的路径没有正确加引号"; }
  rm -rf "$odd"
  # 2. 覆盖安装；版本号前带 v
  expect_ok v-prefix RAILSE_BASE_URL="$URL" RAILSE_VERSION="v$VERSION" RAILSE_INSTALL_DIR="$dir"
  check_installed "$dir/railse"
  # 3. 不给版本：从 checksums.txt 里本平台那一行的文件名取版本号
  rm -f "$dir/railse"
  expect_ok latest RAILSE_BASE_URL="$URL" RAILSE_INSTALL_DIR="$dir"
  check_installed "$dir/railse"
  # 4. 明文 http 且主机不是 127.0.0.1/localhost（127.1 也连到本机）：照装，但警告
  rm -f "$dir/railse"
  expect_ok http-warn RAILSE_BASE_URL="http://127.1:$port" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  check_installed "$dir/railse"
  grep -qF "警告：RAILSE_BASE_URL 是明文 http" "$tmp/log/$shell-http-warn.err" || { show http-warn; fail "$shell http-warn：没有警告"; }
  rm -f "$dir/railse"

  # 5. 校验和不符：拒装，不留二进制；不存在的安装目录不会被建出来
  cp "$tmp/checksums.tampered" "$rel/checksums.txt"
  expect_fail tampered "校验和不符" RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  check_nothing "$dir" tampered
  expect_fail tampered-newdir "校验和不符" RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$w/never/bin"
  [ ! -e "$w/never" ] || fail "$shell tampered-newdir：校验失败不应建出安装目录"
  # 6. 环境里继承的 stage、tmp（install.sh 清理时用的变量名）指向哨兵：失败时清理不能删到它们
  mkdir -p "$w/sentinel-dir"
  : >"$w/sentinel-file"
  : >"$w/sentinel-dir/keep"
  expect_fail inherited-vars "校验和不符" stage="$w/sentinel-file" tmp="$w/sentinel-dir" \
    RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  [ -e "$w/sentinel-file" ] || fail "$shell inherited-vars：失败时删掉了环境变量 stage 指向的文件"
  [ -e "$w/sentinel-dir/keep" ] || fail "$shell inherited-vars：失败时删掉了环境变量 tmp 指向的目录"
  # 7. checksums.txt 里只有名字相近的行：按文件名字段完全相等找不到，拒装
  cp "$tmp/checksums.decoy" "$rel/checksums.txt"
  expect_fail decoy "checksums.txt 里没有 $FILE" RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  check_nothing "$dir" decoy
  cp "$tmp/checksums.good" "$rel/checksums.txt"

  # 8. checksums.txt 404、包 404
  expect_fail no-checksums "下载 checksums.txt 失败" RAILSE_BASE_URL="$URL/nochk" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  expect_fail no-tarball "下载 $FILE 失败" RAILSE_BASE_URL="$URL/notar" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  check_nothing "$dir" 404
  # 9. 不给版本时 checksums.txt 里解析出的版本号是注入或穿越：拒装（也没执行 touch）
  expect_fail latest-inject "解析出的版本号不合法" RAILSE_BASE_URL="$URL/latestbad" RAILSE_INSTALL_DIR="$dir"
  expect_fail latest-dots "解析出的版本号不合法" RAILSE_BASE_URL="$URL/latestdots" RAILSE_INSTALL_DIR="$dir"
  check_nothing "$dir" latest-bad
  # 10. 包里的 railse 是符号链接、目录、硬链接（校验和是对的）：拒装
  expect_fail member-symlink "不是普通文件" RAILSE_BASE_URL="$URL/symlink" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  expect_fail member-dir "不是普通文件" RAILSE_BASE_URL="$URL/dir" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  expect_fail member-hardlink "安装失败" RAILSE_BASE_URL="$URL/hardlink" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  check_nothing "$dir" member
  # 10b. 没有 curl 时用 wget 下载（本机有 wget 才测）
  if [ "$has_wget" = 1 ]; then
    expect_ok wget PATH="$tmp/nocurl" RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
    check_installed "$dir/railse"
    rm -f "$dir/railse"
    cp "$tmp/checksums.tampered" "$rel/checksums.txt"
    expect_fail wget-tampered "校验和不符" PATH="$tmp/nocurl" RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
    cp "$tmp/checksums.good" "$rel/checksums.txt"
    check_nothing "$dir" wget-tampered
    echo "ok（$shell）：没有 curl 时用 wget"
  else
    echo "跳过（$shell）：本机没有 wget，不测 wget 分支"
  fi
  # 11. 没有 sha256sum 与 shasum：拒装
  expect_fail no-sha "需要 sha256sum 或 shasum" PATH="$tmp/nosha" RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  check_nothing "$dir" no-sha

  # 12. 版本号不合法（含去掉 v 之后为空）：在下载之前就失败
  for bad in "v" "0.0.1/../../x" "latest" "1.2" "0.0.1_x" "0.0.1 "; do
    expect_fail bad-version "RAILSE_VERSION 不合法" RAILSE_BASE_URL="$URL" RAILSE_VERSION="$bad" RAILSE_INSTALL_DIR="$dir"
  done
  # 13. RAILSE_BASE_URL 只接受 http:// 或 https:// 开头
  for bad in "file:///etc" "-o$w/evil" "ftp://127.0.0.1:$port" "https://" "127.0.0.1:$port" "$URL/a b"; do
    expect_fail bad-base-url "RAILSE_BASE_URL" RAILSE_BASE_URL="$bad" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  done
  [ ! -e "$w/evil" ] || fail "$shell bad-base-url：-o 被当成了 curl 的选项"
  check_nothing "$dir" bad-input

  # 14. 不支持的平台（假 uname）
  expect_fail freebsd "不支持的系统 FreeBSD" PATH="$tmp/fakebin:$PATH" FAKE_S=FreeBSD FAKE_M=x86_64 \
    RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  expect_fail armv7 "不支持的架构 armv7l" PATH="$tmp/fakebin:$PATH" FAKE_S=Linux FAKE_M=armv7l \
    RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  expect_fail darwin-intel "不提供 macOS Intel" PATH="$tmp/fakebin:$PATH" FAKE_S=Darwin FAKE_M=x86_64 \
    RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$dir"
  check_nothing "$dir" platform

  # 15. 显式给的安装目录不是目录、不可写（root 下 -w 恒真，跳过后者）
  : >"$w/a-file"
  expect_fail not-dir "不是目录" RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$w/a-file"
  if [ "$(id -u)" != 0 ]; then
    mkdir -p "$w/ro"
    chmod 555 "$w/ro"
    expect_fail readonly "不可写" RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION" RAILSE_INSTALL_DIR="$w/ro"
    [ -z "$(ls -A "$w/ro")" ] || fail "$shell readonly：不应写进任何文件"
    chmod 755 "$w/ro"
  fi

  # 16. 不给安装目录且 /usr/local/bin 不可写：装到假 HOME 的 ~/.local/bin（新建 0755），并提示它不在 PATH 里；
  #     校验失败时不建 ~/.local/bin
  if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    echo "跳过（$shell）：/usr/local/bin 可写，不测退到 ~/.local/bin，免得装进真目录"
  else
    home="$w/home"
    mkdir -p "$home"
    cp "$tmp/checksums.tampered" "$rel/checksums.txt"
    expect_fail fallback-tampered "校验和不符" HOME="$home" RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION"
    cp "$tmp/checksums.good" "$rel/checksums.txt"
    [ ! -e "$home/.local" ] || fail "$shell fallback-tampered：校验失败不应建出 ~/.local/bin"
    expect_ok fallback HOME="$home" RAILSE_BASE_URL="$URL" RAILSE_VERSION="$VERSION"
    check_installed "$home/.local/bin/railse"
    [ -n "$(find "$home/.local/bin" -maxdepth 0 -perm 755)" ] || fail "$shell fallback：~/.local/bin 不是 0755"
    grep -qF "，装到 $home/.local/bin" "$tmp/log/$shell-fallback.out" || { show fallback; fail "$shell fallback：没有说明装到哪里"; }
    grep -qF "不在 PATH 里" "$tmp/log/$shell-fallback.out" || { show fallback; fail "$shell fallback：没有提示加 PATH"; }
    echo "ok（$shell）：/usr/local/bin 不可写时退到 ~/.local/bin，校验失败时不建它"
  fi

  echo "ok（$shell）：正常安装、覆盖、取最新版本、明文 http 警告；校验和不符、相近文件名、继承的清理变量、404、注入的版本号、坏的包成员、没有 sha256 工具、非法版本号与地址、坏的安装目录、不支持的平台都拒装"
done

echo "OK: install.sh 下载、校验、安装与拒装都正确"
