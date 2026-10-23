//go:build ignore

// 生成 PWA 图标（只用标准库）：深蓝底、白色的“R”（竖杠 + 上半圆环 + 斜腿）。
//
// 在仓库根目录运行：go run web/scripts/gen-icons.go
// 或在 web/ 下运行：npm run icons（即 go run ./scripts/gen-icons.go -o public/icons）
//
// 生成的 PNG 入库；本文件不放在 public/ 下，免得被 Vite 原样拷进构建产物、嵌进 hub。
//
// 图标是不透明的满底方块，不自己画圆角：iOS 会把 apple-touch-icon 的透明像素填成黑色，
// 可遮罩（maskable）图标也要求满底；圆角由系统按各自的形状裁。字母落在中心 80% 的安全区内。
package main

import (
	"flag"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"path/filepath"
)

func draw(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	bg := color.RGBA{0x1f, 0x2a, 0x44, 0xff}
	fg := color.RGBA{0xff, 0xff, 0xff, 0xff}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, bg)
		}
	}
	// 字母 R 的简化画法：竖杠 + 上半圆环 + 斜腿，整体水平居中
	stem := size * 2 / 25
	outer := size * 7 / 50
	inner := outer - stem
	left := (size - (2*stem + outer)) / 2
	top, bottom := size/4, size*3/4
	for y := top; y < bottom; y++ {
		for x := left; x < left+stem; x++ {
			img.Set(x, y, fg)
		}
	}
	cxr, cyr := left+stem, top+outer
	for y := cyr - outer; y <= cyr+outer; y++ {
		for x := cxr - stem; x <= cxr+outer; x++ {
			if x < cxr {
				// 圆环上下两条横边接到竖杠
				if y < cyr-outer+stem || y > cyr+outer-stem {
					img.Set(x, y, fg)
				}
				continue
			}
			d := (x-cxr)*(x-cxr) + (y-cyr)*(y-cyr)
			if d <= outer*outer && d >= inner*inner {
				img.Set(x, y, fg)
			}
		}
	}
	legTop := cyr + outer
	n := bottom - legTop
	for i := 0; i < n; i++ {
		y := legTop + i
		x := left + stem + i*outer/n
		for w := 0; w < stem; w++ {
			img.Set(x+w, y, fg)
		}
	}
	return img
}

func main() {
	out := flag.String("o", filepath.Join("web", "public", "icons"), "输出目录")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	for _, it := range []struct {
		name string
		size int
	}{{"icon-192.png", 192}, {"icon-512.png", 512}, {"apple-touch-icon-180.png", 180}} {
		f, err := os.Create(filepath.Join(*out, it.name))
		if err != nil {
			log.Fatal(err)
		}
		if err := png.Encode(f, draw(it.size)); err != nil {
			f.Close()
			log.Fatal(err)
		}
		if err := f.Close(); err != nil {
			log.Fatal(err)
		}
	}
}
