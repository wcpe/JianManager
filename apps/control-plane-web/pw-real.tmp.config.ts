import { defineConfig, devices } from '@playwright/test'

// FR-518 真实后端验收：直连已在运行的真实 CP + 非 mock 前端，不自动起 webServer。
export default defineConfig({
  testDir: './e2e',
  testMatch: /assistant-page-context-real\.spec\.ts/,
  timeout: 180_000,
  fullyParallel: false,
  workers: 1,
  reporter: [['list']],
  use: {
    baseURL: process.env.FR518_BASE_URL ?? 'http://127.0.0.1:5299',
    trace: 'off',
    screenshot: 'off',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
