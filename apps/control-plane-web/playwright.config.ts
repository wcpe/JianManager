import { defineConfig, devices } from '@playwright/test'

const requestedE2EPort = process.env.PLAYWRIGHT_PORT ?? '5173'
const e2ePort = /^\d{2,5}$/.test(requestedE2EPort) ? requestedE2EPort : '5173'
const e2eBaseURL = process.env.PLAYWRIGHT_BASE_URL ?? `http://127.0.0.1:${e2ePort}`

/** 
 * Playwright E2E：跑 mock 模式整站（VITE_MOCK，无需真后端），验关键跨页流。
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
   * 断言预算 30 秒（原默认 5 → 15）。
   *
   * 【为何是 30 而不是 15】本仓 mock large 规模下（1200 实例 / 12000 日志），
   * 首屏本身就要 10~20 秒才把内容挂上 DOM——实测（同一台机、同一版本，多次采样）：
   *   `/instances/1` 12s ~ 19.8s、`/instances?...q=server` 搜索框 11.1s、
   *   `/nodes` 6.6s、`/logs` ≈ 15s、`/bots` ≈ 9s。
   * 15 秒预算卡在这些数值中间，于是「机器忙一点就挂」且报成「element(s) not found」。
   *
   * 【为何不是无限放宽】真正的缺陷（元素根本没渲染、选择器写错）仍会在 30 秒内失败；
   * 放宽的只是「等多久算超时」，不是「什么算通过」。
   * 超过 30 秒才就绪的页面应用例自行 `test.setTimeout` 并注明实测值。
   */
  expect: { timeout: 30_000 },
  /**
   * 单用例预算 60 秒（默认 30）。
   *
   * 【为何提高】mock large 规模（1200 实例 / 12000 日志）下，单个页面的首屏就绪本身
   * 可达十几秒（实测 /instances/1 约 12s、/nodes 约 6.6s、/bots 约 9s），而多个用例要
   * 走「登录 → 导航 → 切视图 → 多次断言 + 截图」。默认 30 秒下，慢机器上失败点往往
   * 落在「元素 not found」，看不出是预算问题——本仓已有多处用例各自写
   * `test.setTimeout(120_000)` 来绕这个坑（instance-clone / bot-overview / render-cost 等）。
   *
   * 提到 60 秒后，单个断言仍由 `expect.timeout`（15s）守住，**真正的缺陷依旧会失败**，
   * 只是不再把「页面慢」报成「元素不存在」。个别确实多步且含多张截图的用例
   * 可以继续在用例内上调（如 120s）。
   */
  timeout: 60_000,
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
