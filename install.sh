#!/bin/sh
# Railse 一行安装：
#   curl -fsSL https://raw.githubusercontent.com/xufei547/railse-opc/main/install.sh | sh
#
# 按系统下载 GitHub Release 里的 railse_<版本>_<系统>_<架构>.tar.gz，用同一发布的 checksums.txt 校验 sha256，
# 校验通过才安装。支持 macOS（Apple Silicon）、Linux amd64 与 arm64。不调用 sudo。
#
# 环境变量（经管道运行时写在 sh 前面，例如 curl -fsSL … | RAILSE_VERSION=0.1.0 sh）：
#   RAILSE_REPO         GitHub 仓库，默认 xufei547/railse-opc
#   RAILSE_VERSION      要装的版本（如 0.1.0，前面带 v 也行），默认最新正式版
#   RAILSE_INSTALL_DIR  安装目录（不存在就建）；不给时 /usr/local/bin 可写就装那里，否则装 ~/.local/bin
#   RAILSE_LANG         提示语言，zh 或 en；不给时按 LC_ALL、LC_MESSAGES、LANG 的顺序取第一个非空的，zh 开头算中文，其余（包括都没设）英文
#   RAILSE_BASE_URL     下载根地址（测试用，只接受 http:// 或 https://），默认 https://github.com/$RAILSE_REPO/releases/download/v<版本>；
#                       给了它而没给 RAILSE_VERSION 时，版本号从 $RAILSE_BASE_URL/checksums.txt 里取
#
# 升级就是重跑一遍；新文件先写到安装目录里的临时名再改名替换，正在运行的旧二进制不受影响。
set -eu

# 整个流程在 main 里，脚本最后一行才调用：经管道下载到一半断了时不会执行半截脚本。

# 语言：RAILSE_LANG -> LC_ALL -> LC_MESSAGES -> LANG，取第一个非空的；zh 开头算中文，其余（包括都没设）一律英文
lang=en
case "${RAILSE_LANG:-${LC_ALL:-${LC_MESSAGES:-${LANG:-}}}}" in zh* | ZH*) lang=zh ;; esac

# t <键>：把该键在当前语言下的 printf 格式串放进 $fmt（不带结尾换行）。不用 eval。
# 键名用英文小写加下划线，中英两行成对出现，缺一个就是 bug。格式串里的 %s 个数两种语言必须相同。
t() {
  case "$lang:$1" in
    zh:fail) fmt='安装失败：%s' ;;
    en:fail) fmt='Install failed: %s' ;;
    zh:bad_repo) fmt='RAILSE_REPO 应形如 owner/name：%s' ;;
    en:bad_repo) fmt='RAILSE_REPO must look like owner/name: %s' ;;
    zh:bad_version) fmt='RAILSE_VERSION 不合法：%s（应形如 0.1.0）' ;;
    en:bad_version) fmt='Invalid RAILSE_VERSION: %s (expected something like 0.1.0)' ;;
    zh:base_space) fmt='RAILSE_BASE_URL 里有空白或控制字符' ;;
    en:base_space) fmt='RAILSE_BASE_URL contains whitespace or control characters' ;;
    zh:warn_http) fmt='警告：RAILSE_BASE_URL 是明文 http 且不是本机（%s）：包与 checksums.txt 都可能在路上被换掉，校验挡不住' ;;
    en:warn_http) fmt='Warning: RAILSE_BASE_URL is plain http and not this machine (%s): the package and checksums.txt can both be swapped in transit, so the checksum cannot protect you' ;;
    zh:base_scheme) fmt='RAILSE_BASE_URL 只接受 http:// 或 https:// 开头的地址：%s' ;;
    en:base_scheme) fmt='RAILSE_BASE_URL must start with http:// or https://: %s' ;;
    zh:bad_os) fmt='不支持的系统 %s（只支持 macOS 与 Linux）' ;;
    en:bad_os) fmt='Unsupported system %s (only macOS and Linux are supported)' ;;
    zh:bad_arch) fmt='不支持的架构 %s（只支持 amd64 与 arm64）' ;;
    en:bad_arch) fmt='Unsupported architecture %s (only amd64 and arm64 are supported)' ;;
    zh:no_intel) fmt='不提供 macOS Intel（x86_64）的包，只支持 Apple Silicon' ;;
    en:no_intel) fmt='There is no package for macOS Intel (x86_64), only Apple Silicon' ;;
    zh:need_dl) fmt='需要 curl 或 wget' ;;
    en:need_dl) fmt='curl or wget is required' ;;
    zh:need_sha) fmt='需要 sha256sum 或 shasum 来校验下载的包' ;;
    en:need_sha) fmt='sha256sum or shasum is required to verify the download' ;;
    zh:not_dir) fmt='%s 不是目录' ;;
    en:not_dir) fmt='%s is not a directory' ;;
    zh:env_dir_ro) fmt='%s 不可写（换一个 RAILSE_INSTALL_DIR）' ;;
    en:env_dir_ro) fmt='%s is not writable (set a different RAILSE_INSTALL_DIR)' ;;
    zh:why_ro) fmt='/usr/local/bin 不可写' ;;
    en:why_ro) fmt='/usr/local/bin is not writable' ;;
    zh:why_none) fmt='没有 /usr/local/bin' ;;
    en:why_none) fmt='there is no /usr/local/bin' ;;
    zh:no_home) fmt='%s，也没有 HOME，请用 RAILSE_INSTALL_DIR 指定安装目录' ;;
    en:no_home) fmt='%s, and HOME is not set; set RAILSE_INSTALL_DIR to choose an install directory' ;;
    zh:home_ro) fmt='%s，%s 也不可写，请用 RAILSE_INSTALL_DIR 指定安装目录' ;;
    en:home_ro) fmt='%s, and %s is not writable either; set RAILSE_INSTALL_DIR to choose an install directory' ;;
    zh:note) fmt='%s，装到 %s' ;;
    en:note) fmt='%s, installing to %s' ;;
    zh:dest_is_dir) fmt='%s 是个目录' ;;
    en:dest_is_dir) fmt='%s is a directory' ;;
    zh:no_tmp) fmt='建不了临时目录' ;;
    en:no_tmp) fmt='cannot create a temporary directory' ;;
    zh:no_latest) fmt='取不到最新版本：%s（还没有发布过？）' ;;
    en:no_latest) fmt='cannot get the latest version: %s (no release published yet?)' ;;
    zh:latest_no_pkg) fmt='%s 里没有 %s/%s 的包' ;;
    en:latest_no_pkg) fmt='%s has no package for %s/%s' ;;
    zh:latest_bad_ver) fmt='从 %s 解析出的版本号不合法' ;;
    en:latest_bad_ver) fmt='the version parsed from %s is not valid' ;;
    zh:downloading) fmt='下载 %s/%s' ;;
    en:downloading) fmt='Downloading %s/%s' ;;
    zh:dl_sums) fmt='下载 checksums.txt 失败：%s' ;;
    en:dl_sums) fmt='failed to download checksums.txt: %s' ;;
    zh:dl_file) fmt='下载 %s 失败：%s（版本 %s 不存在？）' ;;
    en:dl_file) fmt='failed to download %s: %s (does version %s exist?)' ;;
    zh:no_sum) fmt='checksums.txt 里没有 %s' ;;
    en:no_sum) fmt='checksums.txt has no entry for %s' ;;
    zh:bad_sum) fmt='checksums.txt 里 %s 的校验和格式不对' ;;
    en:bad_sum) fmt='the checksum for %s in checksums.txt is malformed' ;;
    zh:no_actual) fmt='算不出 %s 的 sha256' ;;
    en:no_actual) fmt='cannot compute the sha256 of %s' ;;
    zh:mismatch) fmt='校验和不符：期望 %s，实际 %s（包不完整或被篡改，没有安装）' ;;
    en:mismatch) fmt='checksum mismatch: expected %s, got %s (the package is incomplete or was tampered with; nothing was installed)' ;;
    zh:verified) fmt='sha256 校验通过' ;;
    en:verified) fmt='sha256 verified' ;;
    zh:untar) fmt='解包 %s 失败' ;;
    en:untar) fmt='failed to unpack %s' ;;
    zh:not_regular) fmt='%s 里的 railse 不是普通文件' ;;
    en:not_regular) fmt='railse in %s is not a regular file' ;;
    zh:mkdir_fail) fmt='建不了安装目录 %s' ;;
    en:mkdir_fail) fmt='cannot create the install directory %s' ;;
    zh:chmod_fail) fmt='设不了 %s 的权限' ;;
    en:chmod_fail) fmt='cannot set permissions on %s' ;;
    zh:dir_ro) fmt='%s 不可写' ;;
    en:dir_ro) fmt='%s is not writable' ;;
    zh:write_dir_fail) fmt='写入 %s 失败' ;;
    en:write_dir_fail) fmt='failed to write to %s' ;;
    zh:installed) fmt='已安装 %s：' ;;
    en:installed) fmt='Installed %s: ' ;;
    zh:version_fail) fmt='已写入 %s，但运行 %s version 失败' ;;
    en:version_fail) fmt='%s was written, but running %s version failed' ;;
    zh:path_first) fmt='注意：PATH 里先找到的是 %s，不是刚装的 %s' ;;
    en:path_first) fmt='Note: PATH finds %s first, not the one just installed (%s)' ;;
    zh:path_missing) fmt='%s 不在 PATH 里：把下面这行加进 ~/.zshrc 或 ~/.profile（bash 用 ~/.bashrc），再开一个新终端：' ;;
    en:path_missing) fmt='%s is not in your PATH. Add this line to ~/.zshrc or ~/.profile (~/.bashrc for bash), then open a new terminal:' ;;
    zh:path_line) fmt='  export PATH="%s:$PATH"' ;;
    en:path_line) fmt='  export PATH="%s:$PATH"' ;;
    zh:next_steps) fmt='下一步（详见 docs/setup.md）：' ;;
    en:next_steps) fmt='Next steps (details in docs/setup.md):' ;;
    zh:next_worker) fmt='  工作机：railse setup all --hub https://<hub 域名> --token <接入码>，或直接运行 railse setup（终端里问答）' ;;
    en:next_worker) fmt='  Work machine: railse setup all --hub https://<hub-domain> --token <enroll-token>, or just run railse setup (it asks in the terminal)' ;;
    zh:next_hub) fmt='  hub 机器（macOS，或用反向代理）：railse hub password、railse hub token，再 railse hub run（macOS 直出 TLS 加 --acme-domain <域名>）' ;;
    en:next_hub) fmt='  Hub machine (macOS, or behind a reverse proxy): railse hub password, railse hub token, then railse hub run (on macOS, add --acme-domain <domain> to serve TLS directly)' ;;
    zh:next_hub_linux) fmt='  hub 机器（Linux 直出 443/80）：装成系统服务，见 docs/setup.md 的“Linux：装成系统服务”：' ;;
    en:next_hub_linux) fmt='  Hub machine (Linux, serving 443/80 directly): install it as a system service, see the Linux system service section of docs/setup.md:' ;;
    zh:next_sudo) fmt='    sudo %s setup autostart --hub --system -- --acme-domain <域名> --acme-email <邮箱>' ;;
    en:next_sudo) fmt='    sudo %s setup autostart --hub --system -- --acme-domain <domain> --acme-email <email>' ;;
    zh:next_root) fmt='    （这次装在 %s：hub 机器要以 root 装到 /usr/local/bin，curl -fsSL https://raw.githubusercontent.com/%s/main/install.sh | sudo sh；否则 --user 换成别的用户时，服务执行不了你目录里的 railse）' ;;
    en:next_root) fmt='    (This time it went to %s. On a hub machine, install as root into /usr/local/bin with curl -fsSL https://raw.githubusercontent.com/%s/main/install.sh | sudo sh; otherwise, if --user is another user, the service cannot run the railse in your directory)' ;;
    zh:next_doctor) fmt='  体检：railse doctor' ;;
    en:next_doctor) fmt='  Health check: railse doctor' ;;
    zh:readme) fmt='完整说明：https://github.com/%s#readme' ;;
    en:readme) fmt='Full docs: https://github.com/%s#readme' ;;
    *) fmt="$1" ;;
  esac
}
# str <键> [参数…]：按键取文案并代入参数，不换行
str() {
  t "$1"
  shift
  printf "$fmt" "$@"
}
# say <键> [参数…]：同 str，加换行
say() {
  str "$@"
  printf '\n'
}
# warn <键> [参数…]：同 say，写到标准错误
warn() {
  say "$@" >&2
}
# fail <键> [参数…]：失败退出，错误信息写到标准错误
fail() {
  msg="$(str "$@")"
  t fail
  printf "$fmt\n" "$msg" >&2
  exit 1
}

# 只删本脚本自己建的东西：stage、tmp 在 main 开头先置空、装 trap 之后才由本脚本赋值
cleanup() {
  if [ -n "$stage" ]; then rm -f "$stage"; fi
  if [ -n "$tmp" ]; then rm -rf "$tmp"; fi
}

# download <URL> <输出文件>：有 curl 用 curl，否则用 wget。https 地址用 curl 时只走 https（--proto '=https'，重定向也算）、
# TLS 1.2 起；用 wget 时加 --https-only（支持的话），但 GNU wget 的这个开关只管递归下载时跟的链接，管不了重定向
download() {
  if [ "$downloader" = curl ]; then
    if [ "$https_only" = 1 ]; then
      curl -fsSL --proto '=https' --tlsv1.2 "$1" -o "$2"
    else
      curl -fsSL "$1" -o "$2"
    fi
  elif [ "$https_only" = 1 ] && [ "$wget_https_only" = 1 ]; then
    wget -q --https-only -O "$2" "$1"
  else
    wget -q -O "$2" "$1"
  fi
}

# shquote <字符串>：给人照抄的命令里的一个词。只含安全字符时原样，否则用单引号括起来（内部的 ' 写成 '\''）
shquote() {
  case "$1" in
    '' | *[!A-Za-z0-9_./+:@%,=-]*) printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")" ;;
    *) printf '%s' "$1" ;;
  esac
}

# 版本号只接受 X.Y.Z 或 X.Y.Z-后缀 / X.Y.Z.后缀（后缀只含字母、数字与点）
valid_version() {
  case "$1" in '' | *[!0-9A-Za-z.-]*) return 1 ;; esac
  printf '%s\n' "$1" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.]+)?$'
}

# lookup_sum <checksums 文件> <文件名>：取文件名字段与它完全相等的那一行的 sha256（不按子串、不按正则）。
# 每行是“sha256  文件名”；文件名前的 *（二进制模式标记）去掉再比。
lookup_sum() {
  F="$2" awk '{ n = $2; sub(/^\*/, "", n); if (NF == 2 && n == ENVIRON["F"]) { print tolower($1); exit } }' "$1"
}

# latest_version <checksums 文件>：在 checksums.txt 里找 railse_<版本>_<系统>_<架构>.tar.gz 那一行，取出版本号
latest_version() {
  S="_${os}_${arch}.tar.gz" awk '{
    n = $2; sub(/^\*/, "", n); s = ENVIRON["S"]
    if (NF == 2 && substr(n, 1, 7) == "railse_" && length(n) > 7 + length(s) && substr(n, length(n) - length(s) + 1) == s) {
      print substr(n, 8, length(n) - 7 - length(s)); exit
    }
  }' "$1"
}

main() {
  # 先把清理与条件赋值用到的变量置空再装 trap：环境里可能继承了同名变量（例如 stage=某个文件），
  # 失败时不能删到它，也不能把它当成本脚本的值打印出来。
  stage=""
  tmp=""
  note=""
  version=""
  trap cleanup EXIT
  trap 'exit 1' HUP INT TERM

  repo="${RAILSE_REPO:-xufei547/railse-opc}"
  case "$repo" in
    */*/* | /* | */ | *[!A-Za-z0-9._/-]*) fail bad_repo "$repo" ;;
    */*) ;;
    *) fail bad_repo "$repo" ;;
  esac
  if [ -n "${RAILSE_VERSION:-}" ]; then
    version="${RAILSE_VERSION#v}"
    valid_version "$version" || fail bad_version "$RAILSE_VERSION"
  fi

  # 下载根地址：只接受 http:// 或 https://（不接受 file://、以 - 开头等）；明文 http 且不是本机时警告
  base_url="${RAILSE_BASE_URL:-}"
  base_url="${base_url%/}"
  https_only=1
  if [ -n "$base_url" ]; then
    case "$base_url" in
      *[[:space:][:cntrl:]]*) fail base_space ;;
      https://?*) ;;
      http://?*)
        https_only=0
        host="${base_url#http://}"
        host="${host%%[/?#]*}"
        host="${host##*@}"
        case "$host" in
          127.0.0.1 | 127.0.0.1:* | localhost | localhost:* | '[::1]' | '[::1]:'*) ;;
          *) warn warn_http "$host" ;;
        esac
        ;;
      *) fail base_scheme "$base_url" ;;
    esac
  fi

  # 平台
  os="$(uname -s)"
  arch="$(uname -m)"
  case "$os" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) fail bad_os "$os" ;;
  esac
  case "$arch" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) fail bad_arch "$arch" ;;
  esac
  # Apple Silicon 上经 Rosetta 跑的终端里 uname -m 是 x86_64，按 arm64 装
  if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || true)" = 1 ]; then
    arch=arm64
  fi
  if [ "$os" = darwin ] && [ "$arch" = amd64 ]; then
    fail no_intel
  fi

  # 工具
  wget_https_only=0
  if command -v curl >/dev/null 2>&1; then
    downloader=curl
  elif command -v wget >/dev/null 2>&1; then
    downloader=wget
    # GNU wget 有 --https-only，BusyBox 的没有
    if wget --help 2>&1 | grep -q -- '--https-only'; then wget_https_only=1; fi
  else
    fail need_dl
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    sha="sha256sum"
  elif command -v shasum >/dev/null 2>&1; then
    sha="shasum -a 256"
  else
    fail need_sha
  fi

  # 安装目录：先定下来、已存在的先查可写（不可写就不必下载），不存在的等下载与校验都过了再建（失败不留空目录）。
  # 显式给的用它；否则 /usr/local/bin 可写就装那里，再不然装 ~/.local/bin（新建时 0755）。不用 sudo。
  if [ -n "${RAILSE_INSTALL_DIR:-}" ]; then
    dir="$RAILSE_INSTALL_DIR"
    if [ -e "$dir" ]; then
      [ -d "$dir" ] || fail not_dir "$dir"
      [ -w "$dir" ] || fail env_dir_ro "$dir"
    fi
  elif [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    dir=/usr/local/bin
  else
    if [ -d /usr/local/bin ]; then why="$(str why_ro)"; else why="$(str why_none)"; fi
    [ -n "${HOME:-}" ] || fail no_home "$why"
    dir="$HOME/.local/bin"
    if [ -e "$dir" ] && { [ ! -d "$dir" ] || [ ! -w "$dir" ]; }; then
      fail home_ro "$why" "$dir"
    fi
    note="$(str note "$why" "$dir")"
  fi
  dest="$dir/railse"
  [ ! -d "$dest" ] || fail dest_is_dir "$dest"

  # 显式给模板：macOS 的 mktemp -d 不带模板时不看 TMPDIR
  tmpdir="${TMPDIR:-/tmp}"
  tmp="$(mktemp -d "${tmpdir%/}/railse-install.XXXXXX" 2>/dev/null || mktemp -d /tmp/railse-install.XXXXXX)" || fail no_tmp

  # 版本：没指定时取最新发布（GitHub 的 latest/download 跳到最新正式版的产物），从 checksums.txt 的文件名里取版本号
  if [ -z "$version" ]; then
    if [ -n "$base_url" ]; then
      latest_url="$base_url/checksums.txt"
    else
      latest_url="https://github.com/$repo/releases/latest/download/checksums.txt"
    fi
    download "$latest_url" "$tmp/latest-checksums.txt" || fail no_latest "$latest_url"
    version="$(latest_version "$tmp/latest-checksums.txt")"
    [ -n "$version" ] || fail latest_no_pkg "$latest_url" "$os" "$arch"
    valid_version "$version" || fail latest_bad_ver "$latest_url"
  fi
  [ -n "$base_url" ] || base_url="https://github.com/$repo/releases/download/v$version"
  file="railse_${version}_${os}_${arch}.tar.gz"

  # 下载与校验
  say downloading "$base_url" "$file"
  download "$base_url/checksums.txt" "$tmp/checksums.txt" || fail dl_sums "$base_url/checksums.txt"
  download "$base_url/$file" "$tmp/$file" || fail dl_file "$file" "$base_url/$file" "$version"
  expected="$(lookup_sum "$tmp/checksums.txt" "$file")"
  [ -n "$expected" ] || fail no_sum "$file"
  case "$expected" in *[!0-9a-f]*) fail bad_sum "$file" ;; esac
  [ "${#expected}" -eq 64 ] || fail bad_sum "$file"
  actual="$($sha "$tmp/$file" | awk '{ print tolower($1) }')"
  [ -n "$actual" ] || fail no_actual "$file"
  [ "$expected" = "$actual" ] || fail mismatch "$expected" "$actual"
  say verified

  mkdir "$tmp/x"
  tar -xzf "$tmp/$file" -C "$tmp/x" railse || fail untar "$file"
  if [ ! -f "$tmp/x/railse" ] || [ -L "$tmp/x/railse" ]; then
    fail not_regular "$file"
  fi

  # 校验都过了才建安装目录
  if [ ! -d "$dir" ]; then
    mkdir -p "$dir" || fail mkdir_fail "$dir"
    if [ -n "$note" ]; then chmod 755 "$dir" || fail chmod_fail "$dir"; fi
  fi
  [ -w "$dir" ] || fail dir_ro "$dir"
  if [ -n "$note" ]; then printf '%s\n' "$note"; fi

  # 先写临时名再改名：替换是原子的，正在运行的旧版本不受影响
  stage="$dir/.railse.install.$$"
  install -m 755 "$tmp/x/railse" "$stage" || fail write_dir_fail "$dir"
  mv -f "$stage" "$dest" || fail write_dir_fail "$dest"
  stage=""

  str installed "$dest"
  "$dest" version || fail version_fail "$dest" "$dest"

  case ":${PATH:-}:" in
    *":$dir:"*)
      found="$(command -v railse 2>/dev/null || true)"
      if [ -n "$found" ] && [ "$found" != "$dest" ]; then
        say path_first "$found" "$dest"
      fi
      ;;
    *)
      printf '\n'
      say path_missing "$dir"
      say path_line "$dir"
      ;;
  esac

  printf '\n'
  say next_steps
  say next_worker
  say next_hub
  # 提示里写装好的绝对路径：sudo 的 secure_path 常常找不到 railse（RHEL 系连 /usr/local/bin 都没有）
  absdir="$(CDPATH='' cd -- "$dir" && pwd)" || absdir="$dir"
  say next_hub_linux
  say next_sudo "$(shquote "$absdir/railse")"
  if [ "$absdir" != /usr/local/bin ]; then
    say next_root "$absdir" "$repo"
  fi
  say next_doctor
  say readme "$repo"
}

main "$@"
