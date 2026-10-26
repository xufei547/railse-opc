// 用 Playwright 的 Chromium 把 assets/logo.svg 渲染成 PWA 图标：
//   cd web && node scripts/render-icons.mjs
import { chromium } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const svg = readFileSync(resolve(import.meta.dirname, '../../assets/logo.svg'), 'utf8')
const targets = [
  ['public/icons/icon-512.png', 512],
  ['public/icons/icon-192.png', 192],
  ['public/icons/apple-touch-icon-180.png', 180],
]
const browser = await chromium.launch()
for (const [file, size] of targets) {
  const page = await browser.newPage({ viewport: { width: size, height: size }, deviceScaleFactor: 1 })
  await page.setContent(`<!doctype html><html><body style="margin:0;background:transparent">${svg.replace('width="1024" height="1024"', `width="${size}" height="${size}"`)}</body></html>`)
  await page.screenshot({ path: resolve(import.meta.dirname, '..', file), omitBackground: true, clip: { x: 0, y: 0, width: size, height: size } })
  await page.close()
  console.log('写出', file, size)
}
await browser.close()
