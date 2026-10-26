#!/bin/sh
# Fly.io 上的启动脚本：TLS 由 Fly 的边缘代理终止，hub 监听明文 8787，经 X-Forwarded-For 认来源 IP。
# 环境变量：RAILSE_PUBLIC_URL（必填，手机打开的 https 源）、RAILSE_HUB_INIT_PASSWORD（Fly secret，首次启动设网页密码）、
#          RAILSE_TRUSTED_PROXY（默认 fdaa::/16，Fly 代理的私网地址段）。
set -eu
: "${RAILSE_HUB_HOME:=/data}"
: "${RAILSE_PUBLIC_URL:?需要 RAILSE_PUBLIC_URL，例如 https://railse-hub.fly.dev}"
mkdir -p "$RAILSE_HUB_HOME"
chmod 700 "$RAILSE_HUB_HOME"
# 首次启动：用 secret 里的初始密码设置网页密码；设过一次就不再动（之后用 railse hub password 改）
if [ -n "${RAILSE_HUB_INIT_PASSWORD:-}" ] && [ ! -f "$RAILSE_HUB_HOME/.password-set" ]; then
  printf '%s\n' "$RAILSE_HUB_INIT_PASSWORD" | railse hub password
  touch "$RAILSE_HUB_HOME/.password-set"
fi
exec railse hub run --listen 0.0.0.0:8787 --public-url "$RAILSE_PUBLIC_URL" --secure-cookies \
  --trusted-proxy "${RAILSE_TRUSTED_PROXY:-fdaa::/16}" --foreground
