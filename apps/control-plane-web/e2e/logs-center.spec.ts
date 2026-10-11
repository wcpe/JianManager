import { test, expect, type Page } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-150 日志中心增强 · 单机（Playwright + mock 模式）验收。
 * 覆盖：实时跟随、时间范围、级别过滤、搜索、导出（范围/格式）、分页。
 * （级别配色/时间预设/导出范围/虚拟窗口逻辑由 logs-filters.test.ts 单测覆盖）
 * 证据落 .tmp/acceptance/FR-150/。
 *
 * 默认时间范围的断言走**请求参数**而非下拉显示文案：文案受 i18n 与默认值漂移影响，
 * 断言窗口宽度（== 默认预设跨度）＋ 上界≈now 才锁得住「默认最近 15 分钟」这一 2026-10-11
 * 快首屏依据（默认「全部时间」会让每个目标的全部分区参与查询）。
 *
 * 观测的是**经典**端点 `/api/v1/logs`：mock 模式没有联邦假后端（devmock 无 /logs 假实现），
 * 联邦探测必然不可用 ⇒ 页面降级经典路径，经典端点才是本环境下真实下发的请求。
 */

/** 默认时间窗跨度：15 分钟（与 LogsPage 的 `useState<TimeRangePreset>('15m')` 对齐）。 */
const DEFAULT_WINDOW_MS = 15 * 60 * 1000
/** 上界与挂钟的容差：仅覆盖挂载/网络往返，不覆盖默认值漂移（改成 24h 会差 85min 量级）。 */
const TO_TOLERANCE_MS = 10 * 60 * 1000

function watchAPIRequests(page: Page): string[] {
  const seen: string[] = []
  page.on('request', (request) => {
    const url = request.url()
    if (url.includes('/api/v1/')) seen.push(url)
  })
  return seen
}

/** 从已记录的请求里取某路径最后一次下发的查询参数。 */
function lastParams(seen: string[], path: string): URLSearchParams | null {
  const matched = seen.filter((url) => new URL(url).pathname === path)
  const last = matched.at(-1)
  return last ? new URL(last).searchParams : null
}

test('FR-150 日志中心 实时跟随 + 时间范围 + 级别过滤 + 导出 + 分页', async ({ page }) => {
  const seen = watchAPIRequests(page)
  await login(page)
  await page.goto('/logs')

  await expect(page.getByRole('heading', { name: '日志中心' })).toBeVisible()
  await expect(page.getByRole('button', { name: '实时跟随' })).toBeVisible()
  await expect(page.getByRole('button', { name: '导出' })).toBeVisible()
  await expect(page.getByRole('button', { name: '错误', exact: true })).toBeVisible()
  await expect(page.getByPlaceholder('搜索日志内容…')).toBeVisible()

  // 时间范围为 Radix Select（combobox）：断言**控件存在** + **下发参数**，
  // 不对默认选项的显示文案做断言（文案随 i18n / 默认值漂移即假红）。
  const rangeSelect = page.getByTestId('logs-range-select')
  await expect(rangeSelect).toBeVisible()

  // 等待经典端点被请求到：断言 `lastParams` **本身**非空 —— 旧写法 `?.get('from')` 在无匹配
  // 请求时返回 undefined，而 `expect(undefined).not.toBeNull()` 会误通过，随后由空串解析出的
  // NaN 让失败信息失去指向性。
  await expect
    .poll(async () => lastParams(seen, '/api/v1/logs'), { timeout: 10_000 })
    .not.toBeNull()
  const logsParams = lastParams(seen, '/api/v1/logs')!
  const fromMs = new Date(logsParams.get('from') ?? '').getTime()
  const toMs = new Date(logsParams.get('to') ?? '').getTime()
  // 窗宽是默认预设的直接函数（不受断言时刻影响）：默认窗改成 24h/全部时间即红。
  expect(toMs - fromMs, '默认时间窗跨度').toBe(DEFAULT_WINDOW_MS)
  // 上界≈now ⇒ 窗口确实是「最近 15 分钟」，而非任意一段 15 分钟宽的区间。
  expect(Math.abs(toMs - Date.now()), '默认时间窗上界贴近当前时刻').toBeLessThan(TO_TOLERANCE_MS)

  // 手动切到「全部时间」：筛选器可用，且下发的参数里不再带时间上/下界（走参数而非文案）。
  await rangeSelect.click()
  await page.getByRole('option', { name: '全部时间' }).click()
  await expect
    .poll(async () => {
      const params = lastParams(seen, '/api/v1/logs')
      return params ? `${params.get('from') ?? 'null'}|${params.get('to') ?? 'null'}` : ''
    })
    .toBe('null|null')

  await expect(page.getByRole('button', { name: '下一页' })).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-150/single-machine-logs.png', fullPage: true })
})
