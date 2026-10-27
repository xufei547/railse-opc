#!/usr/bin/env bash
# 录 README 里的演示（make demo）：经 scripts/e2e-web.sh 从头构建网页与二进制，起真实的 hub 与守护进程（临时目录、随机端口、临时 HOME），
# 用 web/scripts/record-demo.config.ts 只跑录制脚本 web/scripts/record-demo.ts（不跑 web/e2e 的用例），
# 再用 ffmpeg 把录像转成 assets/demo.gif：两遍调色板，宽 390，10 fps，循环播放。
# 录像、调色板等中间文件都在临时目录里，退出时（成功、失败或被中断）删掉；进程与它们的临时目录由 e2e-web.sh 收拾。
# ffmpeg 不在 PATH 上时用 FFMPEG=<路径> 指定（make demo FFMPEG=...）。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FFMPEG="${FFMPEG:-ffmpeg}"

if ! command -v "$FFMPEG" >/dev/null 2>&1; then
  echo "找不到 ffmpeg（FFMPEG=$FFMPEG）：先装上（macOS 上 brew install ffmpeg），或用 make demo FFMPEG=<ffmpeg 的路径> 指定" >&2
  exit 1
fi

OUT="$(mktemp -d "${TMPDIR:-/tmp}/railse-demo.XXXXXX")"
cleanup() {
  local code=$?
  trap - EXIT INT TERM
  rm -rf "$OUT"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# TMPDIR=/tmp：hub、守护进程与模拟项目的临时目录放在短路径下，演示里请求详情的「目录」「项目」一行放得下
TMPDIR=/tmp RAILSE_DEMO_OUT="$OUT/pw" "$ROOT/scripts/e2e-web.sh" --config scripts/record-demo.config.ts

video="$(find "$OUT/pw" -name video.webm -print -quit)"
if [ -z "$video" ]; then
  echo "录制没有留下录像（$OUT/pw 下没有 video.webm）" >&2
  exit 1
fi
# 录制脚本写的截取起点与时长（秒）：去掉开页前的空白帧和收尾时多录的几秒
read -r start dur <"$(dirname "$video")/trim.txt"

# 先按 10 fps、宽 390 取帧；mpdecimate 去掉与上一帧几乎一样的帧（录像的压缩噪点会让静止画面每帧都略有不同，gif 因此变大），
# 再补回 10 fps（补的是上一帧的原样拷贝，gif 里几乎不占空间），末尾用最后一帧补足到原时长
filters="fps=10,scale=390:-1:flags=lanczos,mpdecimate,fps=10,tpad=stop_mode=clone:stop_duration=10,trim=duration=$dur"
"$FFMPEG" -v error -y -ss "$start" -t "$dur" -i "$video" -vf "${filters},palettegen" "$OUT/palette.png"
"$FFMPEG" -v error -y -ss "$start" -t "$dur" -i "$video" -i "$OUT/palette.png" \
  -lavfi "${filters}[v];[v][1:v]paletteuse=dither=bayer:bayer_scale=5:diff_mode=rectangle" -loop 0 "$OUT/demo.gif"

mkdir -p "$ROOT/assets"
mv "$OUT/demo.gif" "$ROOT/assets/demo.gif"
echo "已写 assets/demo.gif：$(wc -c <"$ROOT/assets/demo.gif" | tr -d ' ') 字节，${dur} 秒"
