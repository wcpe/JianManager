import { defineConfig, devices } from '@playwright/test'

const requestedE2EPort = process.env.PLAYWRIGHT_PORT ?? '5173'
const e2ePort = /^\d{2,5}$/.test(requestedE2EPort) ? requestedE2EPort : '5173'
const e2eBaseURL = process.env.PLAYWRIGHT_BASE_URL ?? `http://127.0.0.1:${e2ePort}`

/**
 * Playwright E2E（FR-211）：跑 mock 模式整站（VITE_MOCK，无需真后端），验关键跨页流。
 * 与 vitest 组件测互补——E2E 抓登录/导航/实例生命周期等真浏览器端到端，逐页细节归 jsdom。
 */
export default defineConfig({
  testDir: './e2e',
  // benchmark 与登录就绪依赖同一个 mock dev server，串行跑避免并发抢资源造成假性掉帧。
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  failOnFlakyTests: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: 1,
  reporter: 'list',
  use: {
    baseURL: e2eBaseURL,
    trace: process.env.CI ? 'retain-on-failure' : 'on-first-retry',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  // 自动起当前工作区的 mock dev server；默认禁止复用未知服务，避免本地旧进程造成假失败。
  webServer: {
    command: `npm run dev:mock -- --host 127.0.0.1 --port ${e2ePort}`,
    url: e2eBaseURL,
    reuseExistingServer: process.env.PLAYWRIGHT_REUSE_SERVER === '1',
    timeout: 120_000,
  },
})
