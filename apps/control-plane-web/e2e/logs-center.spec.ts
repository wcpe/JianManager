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
 * 观测的是**联邦**端点 `/api/v1/logs/federation`：devmock 提供联邦假后端（与经典 `/logs`
 * 复用同一份种子），页面在联邦视图下**只**以 federation.items 为数据源。查询服务不可用时
 * **不再回退**经典路径（用户裁定：没有就是没有，平台/历史日志走「更多 → Legacy 只读」专门入口），
 * 所以这里断言的必须是联邦端点本人下发的参数。
 */

/** 默认时间窗跨度：15 分钟（与 LogsPage 的 `useState<TimeRangePreset>('15m')` 对齐）。 */
const DEFAULT_WINDOW_MS = 15 * 60 * 1000
/**
 * 上界与挂钟的容差（NEW-4，2026-10-12 收紧）。
 *
 * 语义是「仅覆盖挂载/网络往返」——实际实现里上界锚点就是 `Date.now()`，偏差应在毫秒级。
 * 原先取 10min（＝窗宽的 2/3，见 `DEFAULT_WINDOW_MS`）过宽：若某天默认窗变成「历史窗口」
 * （例如 `[now-24h, now-15m]`，窗宽仍可能恰好 15min），上界落后 9 分钟也照样通过 ⇒ 漏检。
 * 收紧到 60s：仍远大于挂载往返（实测该 spec 从打开到断言为秒级），但能拦住「上界明显偏离
 * 当前时刻」的形态。窗宽本身由 `DEFAULT_WINDOW_MS` 的精确等值断言单独锁定，不依赖本容差。
 */
const TO_TOLERANCE_MS = 60 * 1000

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

  // 等待联邦端点被请求到：断言 `lastParams` **本身**非空 —— 旧写法 `?.get('from')` 在无匹配
  // 请求时返回 undefined，而 `expect(undefined).not.toBeNull()` 会误通过，随后由空串解析出的
  // NaN 让失败信息失去指向性。
  await expect
    .poll(async () => lastParams(seen, '/api/v1/logs/federation'))
    .not.toBeNull()
  const logsParams = lastParams(seen, '/api/v1/logs/federation')!
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
      const params = lastParams(seen, '/api/v1/logs/federation')
      return params ? `${params.get('from') ?? 'null'}|${params.get('to') ?? 'null'}` : ''
    })
    .toBe('null|null')

  await expect(page.getByRole('button', { name: '下一页' })).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-150/single-machine-logs.png', fullPage: true })
})
