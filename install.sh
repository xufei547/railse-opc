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
#   RAILSE_BASE_URL     下载根地址（测试用，只接受 http:// 或 https://），默认 https://github.com/$RAILSE_REPO/releases/download/v<版本>；
#                       给了它而没给 RAILSE_VERSION 时，版本号从 $RAILSE_BASE_URL/checksums.txt 里取
#
# 升级就是重跑一遍；新文件先写到安装目录里的临时名再改名替换，正在运行的旧二进制不受影响。
set -eu

# 整个流程在 main 里，脚本最后一行才调用：经管道下载到一半断了时不会执行半截脚本。

fail() {
  printf '安装失败：%s\n' "$1" >&2
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
    */*/* | /* | */ | *[!A-Za-z0-9._/-]*) fail "RAILSE_REPO 应形如 owner/name：$repo" ;;
    */*) ;;
    *) fail "RAILSE_REPO 应形如 owner/name：$repo" ;;
  esac
  if [ -n "${RAILSE_VERSION:-}" ]; then
    version="${RAILSE_VERSION#v}"
    valid_version "$version" || fail "RAILSE_VERSION 不合法：$RAILSE_VERSION（应形如 0.1.0）"
  fi

  # 下载根地址：只接受 http:// 或 https://（不接受 file://、以 - 开头等）；明文 http 且不是本机时警告
  base_url="${RAILSE_BASE_URL:-}"
  base_url="${base_url%/}"
  https_only=1
  if [ -n "$base_url" ]; then
    case "$base_url" in
      *[[:space:][:cntrl:]]*) fail "RAILSE_BASE_URL 里有空白或控制字符" ;;
      https://?*) ;;
      http://?*)
        https_only=0
        host="${base_url#http://}"
        host="${host%%[/?#]*}"
        host="${host##*@}"
        case "$host" in
          127.0.0.1 | 127.0.0.1:* | localhost | localhost:* | '[::1]' | '[::1]:'*) ;;
          *) printf '警告：RAILSE_BASE_URL 是明文 http 且不是本机（%s）：包与 checksums.txt 都可能在路上被换掉，校验挡不住\n' "$host" >&2 ;;
        esac
        ;;
      *) fail "RAILSE_BASE_URL 只接受 http:// 或 https:// 开头的地址：$base_url" ;;
    esac
  fi

  # 平台
  os="$(uname -s)"
  arch="$(uname -m)"
  case "$os" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) fail "不支持的系统 $os（只支持 macOS 与 Linux）" ;;
  esac
  case "$arch" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) fail "不支持的架构 $arch（只支持 amd64 与 arm64）" ;;
  esac
  # Apple Silicon 上经 Rosetta 跑的终端里 uname -m 是 x86_64，按 arm64 装
  if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || true)" = 1 ]; then
    arch=arm64
  fi
  if [ "$os" = darwin ] && [ "$arch" = amd64 ]; then
    fail "不提供 macOS Intel（x86_64）的包，只支持 Apple Silicon"
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
    fail "需要 curl 或 wget"
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    sha="sha256sum"
  elif command -v shasum >/dev/null 2>&1; then
    sha="shasum -a 256"
  else
    fail "需要 sha256sum 或 shasum 来校验下载的包"
  fi

  # 安装目录：先定下来、已存在的先查可写（不可写就不必下载），不存在的等下载与校验都过了再建（失败不留空目录）。
  # 显式给的用它；否则 /usr/local/bin 可写就装那里，再不然装 ~/.local/bin（新建时 0755）。不用 sudo。
  if [ -n "${RAILSE_INSTALL_DIR:-}" ]; then
    dir="$RAILSE_INSTALL_DIR"
    if [ -e "$dir" ]; then
      [ -d "$dir" ] || fail "$dir 不是目录"
      [ -w "$dir" ] || fail "$dir 不可写（换一个 RAILSE_INSTALL_DIR）"
    fi
  elif [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    dir=/usr/local/bin
  else
    if [ -d /usr/local/bin ]; then why="/usr/local/bin 不可写"; else why="没有 /usr/local/bin"; fi
    [ -n "${HOME:-}" ] || fail "$why，也没有 HOME，请用 RAILSE_INSTALL_DIR 指定安装目录"
    dir="$HOME/.local/bin"
    if [ -e "$dir" ] && { [ ! -d "$dir" ] || [ ! -w "$dir" ]; }; then
      fail "$why，$dir 也不可写，请用 RAILSE_INSTALL_DIR 指定安装目录"
    fi
    note="$why，装到 $dir"
  fi
  dest="$dir/railse"
  [ ! -d "$dest" ] || fail "$dest 是个目录"

  # 显式给模板：macOS 的 mktemp -d 不带模板时不看 TMPDIR
  tmpdir="${TMPDIR:-/tmp}"
  tmp="$(mktemp -d "${tmpdir%/}/railse-install.XXXXXX" 2>/dev/null || mktemp -d /tmp/railse-install.XXXXXX)" || fail "建不了临时目录"

  # 版本：没指定时取最新发布（GitHub 的 latest/download 跳到最新正式版的产物），从 checksums.txt 的文件名里取版本号
  if [ -z "$version" ]; then
    if [ -n "$base_url" ]; then
      latest_url="$base_url/checksums.txt"
    else
      latest_url="https://github.com/$repo/releases/latest/download/checksums.txt"
    fi
    download "$latest_url" "$tmp/latest-checksums.txt" || fail "取不到最新版本：$latest_url（还没有发布过？）"
    version="$(latest_version "$tmp/latest-checksums.txt")"
    [ -n "$version" ] || fail "$latest_url 里没有 $os/$arch 的包"
    valid_version "$version" || fail "从 $latest_url 解析出的版本号不合法"
  fi
  [ -n "$base_url" ] || base_url="https://github.com/$repo/releases/download/v$version"
  file="railse_${version}_${os}_${arch}.tar.gz"

  # 下载与校验
  printf '下载 %s/%s\n' "$base_url" "$file"
  download "$base_url/checksums.txt" "$tmp/checksums.txt" || fail "下载 checksums.txt 失败：$base_url/checksums.txt"
  download "$base_url/$file" "$tmp/$file" || fail "下载 $file 失败：$base_url/$file（版本 $version 不存在？）"
  expected="$(lookup_sum "$tmp/checksums.txt" "$file")"
  [ -n "$expected" ] || fail "checksums.txt 里没有 $file"
  case "$expected" in *[!0-9a-f]*) fail "checksums.txt 里 $file 的校验和格式不对" ;; esac
  [ "${#expected}" -eq 64 ] || fail "checksums.txt 里 $file 的校验和格式不对"
  actual="$($sha "$tmp/$file" | awk '{ print tolower($1) }')"
  [ -n "$actual" ] || fail "算不出 $file 的 sha256"
  [ "$expected" = "$actual" ] || fail "校验和不符：期望 $expected，实际 $actual（包不完整或被篡改，没有安装）"
  printf 'sha256 校验通过\n'

  mkdir "$tmp/x"
  tar -xzf "$tmp/$file" -C "$tmp/x" railse || fail "解包 $file 失败"
  if [ ! -f "$tmp/x/railse" ] || [ -L "$tmp/x/railse" ]; then
    fail "$file 里的 railse 不是普通文件"
  fi

  # 校验都过了才建安装目录
  if [ ! -d "$dir" ]; then
    mkdir -p "$dir" || fail "建不了安装目录 $dir"
    if [ -n "$note" ]; then chmod 755 "$dir" || fail "设不了 $dir 的权限"; fi
  fi
  [ -w "$dir" ] || fail "$dir 不可写"
  if [ -n "$note" ]; then printf '%s\n' "$note"; fi

  # 先写临时名再改名：替换是原子的，正在运行的旧版本不受影响
  stage="$dir/.railse.install.$$"
  install -m 755 "$tmp/x/railse" "$stage" || fail "写入 $dir 失败"
  mv -f "$stage" "$dest" || fail "写入 $dest 失败"
  stage=""

  printf '已安装 %s：' "$dest"
  "$dest" version || fail "已写入 $dest，但运行 $dest version 失败"

  case ":${PATH:-}:" in
    *":$dir:"*)
      found="$(command -v railse 2>/dev/null || true)"
      if [ -n "$found" ] && [ "$found" != "$dest" ]; then
        printf '注意：PATH 里先找到的是 %s，不是刚装的 %s\n' "$found" "$dest"
      fi
      ;;
    *)
      printf '\n%s 不在 PATH 里：把下面这行加进 ~/.zshrc 或 ~/.profile（bash 用 ~/.bashrc），再开一个新终端：\n' "$dir"
      printf '  export PATH="%s:$PATH"\n' "$dir"
      ;;
  esac

  printf '\n下一步（详见 docs/setup.md）：\n'
  printf '  工作机：railse setup all --hub https://<hub 域名> --token <接入码>\n'
  printf '  hub 机器（macOS，或用反向代理）：railse hub password、railse hub token，再 railse hub run（macOS 直出 TLS 加 --acme-domain <域名>）\n'
  # 提示里写装好的绝对路径：sudo 的 secure_path 常常找不到 railse（RHEL 系连 /usr/local/bin 都没有）
  absdir="$(CDPATH='' cd -- "$dir" && pwd)" || absdir="$dir"
  printf '  hub 机器（Linux 直出 443/80）：装成系统服务，见 docs/setup.md 的“Linux：装成系统服务”：\n'
  printf '    sudo %s setup autostart --hub --system -- --acme-domain <域名> --acme-email <邮箱>\n' "$(shquote "$absdir/railse")"
  if [ "$absdir" != /usr/local/bin ]; then
    printf '    （这次装在 %s：hub 机器要以 root 装到 /usr/local/bin，curl -fsSL https://raw.githubusercontent.com/%s/main/install.sh | sudo sh；否则 --user 换成别的用户时，服务执行不了你目录里的 railse）\n' "$absdir" "$repo"
  fi
  printf '  体检：railse doctor\n'
  printf '完整说明：https://github.com/%s#readme\n' "$repo"
}

main "$@"
