import { defineConfig } from '@playwright/test'

// 端到端用真实的 hub 与守护进程，由 e2e/global-setup.ts 起在临时目录与随机端口上；入口是 scripts/e2e-web.sh（make e2e）。
// hub 的地址在 setup 之后才知道（配置先于 setup 加载），所以不设 baseURL，用例从 RAILSE_E2E_ENV 读。
export default defineConfig({
  testDir: './e2e',
  timeout: 120_000,
  workers: 1,
  retries: 0,
  forbidOnly: true,
  reporter: [['list']],
  globalSetup: './e2e/global-setup.ts',
  globalTeardown: './e2e/global-teardown.ts',
  use: {
    viewport: { width: 390, height: 844 },
    isMobile: true,
    hasTouch: true,
    locale: 'zh-CN',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
})
