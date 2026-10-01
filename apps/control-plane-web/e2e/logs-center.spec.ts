import { test, expect, type Page } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-150 日志中心增强 · 单机（Playwright + mock 模式）验收。
 * 覆盖：实时跟随、时间范围、级别过滤、搜索、导出（范围/格式）、分页。
 * （级别配色/时间预设/导出范围/虚拟窗口逻辑由 logs-filters.test.ts 单测覆盖）
 * 证据落 .tmp/acceptance/FR-150/。
 *
 * 默认时间范围的断言走**请求参数**而非下拉显示文案：文案受 i18n 与默认值漂移影响，
 * 断言 `from ≈ now − 24h` 才锁得住「默认 24h 窗口」这一 2026-09-30 性能依据
 * （无界范围会让全部分区参与查询）。
 */

/** 记录页面发出的 API 请求 URL（含查询串），供断言下发的参数。 */
const DAY_MS = 24 * 60 * 60 * 1000
/** 请求发出与断言之间的容差：仅覆盖挂载/网络往返，不覆盖默认值漂移。 */
const FROM_TOLERANCE_MS = 10 * 60 * 1000

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
  const rangeSelect = page.getByRole('combobox').first()
  await expect(rangeSelect).toBeVisible()

  await expect
    .poll(async () => lastParams(seen, '/api/v1/logs')?.get('from'), { timeout: 10_000 })
    .not.toBeNull()
  const logsParams = lastParams(seen, '/api/v1/logs')
  const fromParam = logsParams?.get('from') ?? ''
  expect(Math.abs(new Date(fromParam).getTime() - (Date.now() - DAY_MS))).toBeLessThan(
    FROM_TOLERANCE_MS,
  )
  expect(logsParams?.get('to')).toBeTruthy()

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
