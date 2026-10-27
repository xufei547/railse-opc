// 用 Playwright 的 Chromium 把 assets/social-preview.svg 渲染成 GitHub 社交预览图（1280×640）：
//   cd web && node scripts/render-social.mjs
// 渲染出的 PNG 要在 GitHub 仓库的 Settings → Social preview 里手动上传，没有接口。
import { chromium } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const svg = readFileSync(resolve(import.meta.dirname, '../../assets/social-preview.svg'), 'utf8')
const out = resolve(import.meta.dirname, '../../assets/social-preview.png')
const browser = await chromium.launch()
const page = await browser.newPage({ viewport: { width: 1280, height: 640 }, deviceScaleFactor: 1 })
await page.setContent(`<!doctype html><html><body style="margin:0">${svg}</body></html>`)
await page.screenshot({ path: out, clip: { x: 0, y: 0, width: 1280, height: 640 } })
await browser.close()
console.log('写出', out)
