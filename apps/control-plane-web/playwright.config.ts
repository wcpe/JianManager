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
  // CI 对瞬时波动重试一次；重试恢复记为 flaky，但仅持续失败才阻断门禁。
  failOnFlakyTests: false,
  retries: process.env.CI ? 1 : 0,
  workers: 1,
  reporter: process.env.CI ? [['list'], ['github']] : 'list',
  /**
   * 断言默认 5 秒对本站偏紧：mock 种子是约 1200 实例 / 12000 日志的量级，
   * 而导航重构后首屏还要按路由分块多下载几个 chunk——实测若干页面从 goto 到页头出现
   * 需要 8 秒以上。于是「goto 后 expect(标题).toBeVisible()」这个全站到处都在用的写法
   * 会成片超时，且失败信息显示为「元素(s) not found」，极易被误判成选择器写错或页面没渲染
   * （本次排查中确实一度如此误判）。
   *
   * 放宽到 15 秒：真正的缺陷仍会失败，只是不再把「慢」报成「没有」。
   * test timeout 保持默认 30 秒不变——单个断言慢不该演变成整例无限期挂起。
   */
  expect: { timeout: 15_000 },
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
