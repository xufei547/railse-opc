import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

const hub = process.env.RAILSE_HUB ?? 'http://127.0.0.1:8787'
const outDir = resolve(__dirname, '../internal/hub/webui/dist')

export default defineConfig({
  plugins: [
    react(),
    {
      // vite build 会清空 outDir，这里把 .gitkeep 补回去，构建后工作树不脏
      name: 'railse-keep-gitkeep',
      closeBundle() {
        mkdirSync(outDir, { recursive: true })
        writeFileSync(resolve(outDir, '.gitkeep'), '')
      },
    },
  ],
  build: {
    outDir,
    emptyOutDir: true,
    // hub 的 CSP 是 img-src 'self'、不许内联：小资源也不内联成 data: URI
    assetsInlineLimit: 0,
    // 产物嵌进 hub 二进制、assets/ 长期缓存，不带 sourcemap
    sourcemap: false,
  },
  server: {
    // hub 的写接口与 /ws/client 校验 Origin 与 Host 相同，代理时把 Origin 改写成 hub 的源
    proxy: {
      '/api': { target: hub, changeOrigin: true, headers: { Origin: hub } },
      '/ws': { target: hub, ws: true, changeOrigin: true, headers: { Origin: hub } },
    },
  },
  test: { environment: 'node', include: ['src/**/*.test.ts'] },
})
