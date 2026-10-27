import { defineConfig } from '@playwright/test'

// 录 README 里的演示（make demo → scripts/demo.sh）：只跑 record-demo.ts，不碰 e2e/ 里的用例；
// hub 与守护进程照端到端的办法由 e2e/global-setup.ts 起在临时目录与随机端口上。
// 录像与中间文件写到 scripts/demo.sh 给的临时目录 RAILSE_DEMO_OUT，跑完由它转成 gif 后删掉。
const out = process.env.RAILSE_DEMO_OUT
if (!out) throw new Error('没有 RAILSE_DEMO_OUT：演示要经 scripts/demo.sh（make demo）录制')

const size = { width: 390, height: 844 }

export default defineConfig({
  testDir: '.',
  testMatch: /record-demo\.ts$/,
  outputDir: out,
  timeout: 120_000,
  globalTimeout: 10 * 60 * 1000,
  workers: 1,
  retries: 0,
  forbidOnly: true,
  reporter: 'list',
  globalSetup: '../e2e/global-setup.ts',
  globalTeardown: '../e2e/global-teardown.ts',
  use: {
    viewport: size,
    isMobile: true,
    hasTouch: true,
    locale: 'zh-CN',
    colorScheme: 'light',
    // 不设 size 时 Playwright 把录像缩到 800×800 以内
    video: { mode: 'on', size },
  },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
})
