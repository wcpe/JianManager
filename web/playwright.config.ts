import { defineConfig, devices } from '@playwright/test'

/**
 * Playwright E2E（FR-211）：跑 mock 模式整站（VITE_MOCK，无需真后端），验关键跨页流。
 * 与 vitest 组件测互补——E2E 抓登录/导航/实例生命周期等真浏览器端到端，逐页细节归 jsdom。
 */
export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: 'list',
  use: {
    baseURL: 'http://localhost:5173',
    trace: 'on-first-retry',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  // 自动起 mock 模式 dev server（dev:mock=VITE_MOCK=1）；本地复用已开的，CI 新起。
  webServer: {
    command: 'npm run dev:mock',
    url: 'http://localhost:5173',
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
})
