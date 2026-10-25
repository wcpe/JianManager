import { test, expect, type Page, type Download } from '@playwright/test'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { login } from './helpers'

const evidenceRoot = process.env.PANEL_BROWSER_EVIDENCE_DIR ?? resolve(process.cwd(), '../../.tmp/vl-six-fix-31cf5b3d')
const dbModulePath = `/@fs${resolve(process.cwd(), '../../packages/devmock/src/db/index.ts')}`
const injectModulePath = `/@fs${resolve(process.cwd(), '../../packages/devmock/src/inject.ts')}`

async function downloadRows(download: Download, name: string): Promise<Record<string, unknown>[]> {
  const path = resolve(evidenceRoot, `panel-${name}.ndjson`)
  await download.saveAs(path)
  return (await readFile(path, 'utf8')).split('\n').filter(Boolean).map((line) => JSON.parse(line) as Record<string, unknown>)
}
async function exportFromMenu(page: Page, label: string) {
  await page.getByTestId('logs-export-trigger').click()
  await page.getByRole('menuitem', { name: label, exact: true }).click()
}
async function downloadFromMenu(page: Page, label: string, name: string) {
  const [download] = await Promise.all([page.waitForEvent('download'), exportFromMenu(page, label)])
  return downloadRows(download, name)
}

/**
 * FR-150 日志中心增强 · 单机（Playwright + mock 模式）验收。
 * 覆盖：实时跟随、时间范围、级别过滤、搜索、导出（范围/格式）、分页。
 * （级别配色/时间预设/导出范围/虚拟窗口逻辑由 logs-filters.test.ts 单测覆盖）
 * 本批证据仅落项目根 .tmp/vl-six-fix-31cf5b3d/panel-*。
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

  await page.screenshot({ path: resolve(evidenceRoot, 'panel-logs-smoke.png'), fullPage: true })
})

/** 每条有限流程独立建同样的161+2事件夹具，不靠延长全局超时塞进所有交互。 */
async function prepareBrowserLogs(page: Page) {
  const seen = watchAPIRequests(page)
  await login(page)
  await page.goto('/logs')
  await expect(page.getByRole('heading', { name: '日志中心' })).toBeVisible()
  const base = Math.floor(Date.now() / 1000) * 1000 - 1000
  const rows = Array.from({ length: 163 }, (_, index) => ({
    id: 10000 + index, event_id: `browser-event-${index}`, canonical_content_hash: `browser-hash-${index}`,
    source: 'instance', level: 'info', instanceId: 1, instanceUuid: 'instance-1', nodeId: 1,
    message: `浏览器六项-${index}`, time: new Date(base - (index < 161 ? index * 1000 : (16 * 60 + index - 161) * 1000)).toISOString(),
    ingest_position: String(9007199254740993n + BigInt(index)), fields: { original: `原始附件-${index}` },
  }))
  await page.evaluate(async ({ path, rows }) => {
    const { db } = await import(path)
    const logs = db('logs')
    for (const row of logs.list()) logs.remove(row.id)
    for (const row of rows) logs.insert(row)
  }, { path: dbModulePath, rows })
  // 改回相对窗口时刷新实际锚点，避免种子创建时刻晚于刚打开页面的上界。
  await page.getByTestId('logs-range-select').click()
  await page.getByRole('option', { name: '全部时间', exact: true }).click()
  await page.getByTestId('logs-range-select').click()
  await page.getByRole('option', { name: '最近 15 分钟', exact: true }).click()
  await expect(page.getByText('浏览器六项-0', { exact: true })).toBeVisible()
  expect(await page.getByTestId('log-row').count()).toBeLessThanOrEqual(24)
  return { rows, seen }
}

test('六项修复：真实浏览器附件逐字段核对当前页、跨页与整窗', async ({ page }) => {
  const { rows } = await prepareBrowserLogs(page)
  expect(await downloadFromMenu(page, '导出当前页', 'browser-current-page-1')).toEqual(rows.slice(0, 100))
  await page.getByRole('button', { name: '下一页' }).click()
  await expect(page.getByText('浏览器六项-100', { exact: true })).toBeVisible()
  expect(await downloadFromMenu(page, '导出当前页', 'browser-current-page-2')).toEqual(rows.slice(100, 161))
  expect(await downloadFromMenu(page, '导出全部匹配', 'browser-all-matches')).toEqual(rows.slice(0, 161))
})

test('六项修复：真实浏览器移窗与UTC范围使用正确scope，取消不下载', async ({ page }) => {
  const { rows, seen } = await prepareBrowserLogs(page)
  await page.getByRole('button', { name: '下一页' }).click()
  await expect(page.getByText('浏览器六项-100', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '下一页' }).click()
  await expect(page.getByText('浏览器六项-161', { exact: true })).toBeVisible()
  expect(await downloadFromMenu(page, '导出全部匹配', 'browser-shifted-window')).toEqual(rows.slice(161))

  await exportFromMenu(page, '导出所选时间段')
  const dialog = page.getByRole('dialog', { name: '选择导出时间段' })
  await expect(dialog).toBeVisible()
  await page.getByLabel('开始时间（UTC）').fill(rows[41].time.slice(0, 19))
  await page.getByLabel('结束时间（UTC）').fill(rows[40].time.slice(0, 19))
  await page.screenshot({ path: resolve(evidenceRoot, 'panel-browser-range-dialog.png'), fullPage: true })
  const [rangeDownload] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: '导出该时间段' }).click()])
  const selected = await downloadRows(rangeDownload, 'browser-selected-range')
  // 精确附件保持全字段相等；from 边界包含，恰好等于 to 的事件必须排除。
  expect(selected).toEqual([rows[41]])
  expect(selected).toHaveLength(1)
  expect(selected.map((row) => row.event_id)).toEqual(['browser-event-41'])
  expect(selected).not.toContainEqual(rows[40])
  const rangeParams = lastParams(seen, '/api/v1/logs/federation/export')!
  expect(rangeParams.get('from')).toBe(rows[41].time)
  expect(rangeParams.get('to')).toBe(rows[40].time)
  expect(rangeParams.has('viewId')).toBe(false)
  expect(rangeParams.has('cursor')).toBe(false)

  await page.evaluate(async (path) => {
    const { mockInject } = await import(path)
    mockInject('post', '/logs/federation/export', { kind: 'delay', ms: 5000, then: { kind: 'status', status: 403 } })
  }, injectModulePath)
  const downloaded: Download[] = []
  page.on('download', (download) => downloaded.push(download))
  await exportFromMenu(page, '导出全部匹配')
  await page.getByRole('button', { name: '取消', exact: true }).click()
  await expect(page.getByTestId('logs-export-trigger')).toBeEnabled()
  expect(downloaded).toHaveLength(0)
  await page.setViewportSize({ width: 1280, height: 720 })
  await page.screenshot({ path: resolve(evidenceRoot, 'panel-browser-logs-1280.png'), fullPage: true })
})

test('六项修复：真实浏览器迁移首击/取消零POST，固定UTC日确认恰好一次', async ({ page }) => {
  const migrations: string[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/v1/nodes/1/log-runtime/migrate' && request.method() === 'POST') migrations.push(request.postData() ?? '')
  })
  await login(page)
  await page.goto('/nodes?node=1&tab=runtime')
  const day = page.getByLabel('迁移日期（该日分区）', { exact: true })
  await expect(day).toBeVisible()
  await day.fill('2026-10-21')
  await page.getByRole('button', { name: '迁移分区', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '确认迁移日志分区' })
  await expect(dialog).toContainText('节点 1 · 存储命名空间 node:1 · UTC 日 2026-10-21')
  expect(migrations).toHaveLength(0)
  await page.getByRole('button', { name: '取消', exact: true }).click()
  expect(migrations).toHaveLength(0)
  await page.getByRole('button', { name: '迁移分区', exact: true }).click()
  await page.screenshot({ path: resolve(evidenceRoot, 'panel-browser-migration-confirm.png'), fullPage: true })
  await page.getByRole('button', { name: '确认迁移', exact: true }).click()
  await expect(dialog).not.toBeVisible()
  expect(migrations).toHaveLength(1)
  expect(JSON.parse(migrations[0])).toEqual({ storageNamespace: 'node:1', utcDay: '2026-10-21' })
})

test('六项修复：英文深色与窄视口仍可操作两个既有设计系统Dialog', async ({ page }) => {
  await login(page)
  await page.evaluate(() => { localStorage.setItem('language', 'en'); localStorage.setItem('theme', 'dark') })
  await page.goto('/logs')
  await expect(page.getByRole('heading', { name: 'Log Center' })).toBeVisible()
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.getByTestId('logs-export-trigger')).toBeEnabled()
  await exportFromMenu(page, 'Export selected range')
  const range = page.getByRole('dialog', { name: 'Choose export time range' })
  await expect(range).toBeVisible()
  await expect(page.getByLabel('Start time (UTC)')).toBeVisible()
  const bounds = await range.boundingBox()
  expect(bounds!.width).toBeLessThanOrEqual(390)
  expect(bounds!.height).toBeLessThanOrEqual(844)
  await expect(page.getByRole('button', { name: 'Export this time range', exact: true })).toBeVisible()
  await page.screenshot({ path: resolve(evidenceRoot, 'panel-browser-en-dark-range-390.png'), fullPage: true })
  await page.keyboard.press('Escape')
  await expect(range).not.toBeVisible()
  await page.goto('/nodes?node=1&tab=runtime')
  await page.getByLabel('Migration date (that day partition)', { exact: true }).fill('2026-10-21')
  await page.getByRole('button', { name: 'Migrate partition', exact: true }).click()
  const migration = page.getByRole('dialog', { name: 'Confirm log partition migration' })
  await expect(migration).toContainText('entire physical UTC day on this node')
  const migrationBounds = await migration.boundingBox()
  expect(migrationBounds!.width).toBeLessThanOrEqual(390)
  expect(migrationBounds!.height).toBeLessThanOrEqual(844)
  await expect(page.getByRole('button', { name: 'Confirm migration', exact: true })).toBeVisible()
  await page.screenshot({ path: resolve(evidenceRoot, 'panel-browser-en-dark-migration-390.png'), fullPage: true })
  await page.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(migration).not.toBeVisible()
})
