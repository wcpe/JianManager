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
  /**
   * 禁止用重试掩盖不稳定（2026-10-20 用户裁定）。
   *
   * 原配置是 `retries: 1` + `failOnFlakyTests: false`——重试一次后恢复仍记为通过，
   * 于是真正的时序竞态被藏起来，只在运气差时才红一次，下次重跑又绿。这既不解决问题，
   * 也让「CI 绿」失去意义。现在两者都关掉：任何一次失败都必须让门禁红。
   */
  failOnFlakyTests: true,
  retries: 0,
  workers: 1,
  reporter: process.env.CI ? [['list'], ['github']] : 'list',
  /**
   * 统一超时基准（2026-10-20）。
   *
   * 为什么必须统一：`e2e/helpers.ts` 的 `login()` 用一个默认 5s 的断言等首屏
   * `[data-page="overview"]`，全仓 93 处用例都经它登录。本地健康机器这一步实测 7s 级，
   * CI 上 4 个分片并行（每片各起一个 vite dev server）会再放大一倍 ⇒ 5s 必然偶发超时，
   * 表现成「不同的用例轮流 flaky」，实际根因是同一个短基准。
   *
   * 取值依据（不是拍脑袋）：本地最慢断言约 7s，CI 并行下约 2 倍 ⇒ 15s 留 2 倍余量。
   * 单用例 60s 是为了容纳多步交互（登录 + 多次导航 + 表单），避免「每步都过但总时长不够」。
   *
   * 副作用是受欢迎的：全仓 68 处逐处打补丁的 `timeout: 10_000 / 15_000` 可以收敛掉，
   * 新用例不写超时也能稳定通过；若哪天又出现裸的 15s 补丁，说明基准被改坏了。
   */
  expect: { timeout: 15_000 },
  timeout: 60_000,
  use: {
    baseURL: e2eBaseURL,
    // retries 归零后 'on-first-retry' 永不触发（本地失败会一点 trace 都没有，
    // 而 retries:0 恰恰让本地更容易看到失败）。统一用 retain-on-failure。
    trace: 'retain-on-failure',
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
