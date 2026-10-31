import { test, expect } from '@playwright/test'
import { login } from './helpers'

/** 
 *实例列表汇总头 + 节点/端口列 + 角色徽标 + proxy↔backend inline · 单机（Playwright + mock）验收。
 * 覆盖 console-ux spec §验收：
 *   - sticky 汇总 chip（运行 N/停止 N/崩溃 M/总数）可点设状态筛选；
 *   - 列表加「节点:端口」列（serverPort 已有数据）；
 *   - 角色三态统一语义色徽标；
 *   - proxy 行可 inline 展开已注册 backend 摘要。
 * 证据落 .tmp/acceptance。
 */

test('汇总头计数可点设筛选 + 节点端口列 + 角色徽标（列表视图）', async ({ page }) => {
  await login(page)

  // 进列表视图（节点:端口列与角色徽标列在列表视图渲染）。
  await page.goto('/instances?view=list')
  await expect(page.locator('[data-page="instances"]')).toBeVisible()

  // 1) 汇总卡：全部/运行/停止/崩溃 均在场，且是可点按钮（设状态筛选）。
  //
  // 【为何正则是「数 字 标签」而不是「标签 数」】汇总控件已从 `SummaryChips`（一排 pill，
  // 结构是「标签 + 计数」）改为 `StatCard`（KPI 卡），后者的 DOM 顺序是 **先数值后标签**
  // （`<span>{value}</span><span>{label}</span>`）——于是可及名是「721 运行」而不是「运行 721」。
  // 旧写法 `/^运行 \d/` 一个也匹配不到（锚在开头） → 30 秒后报 element not found，
  // 容易被当成“汇总头没渲染”。
  const chipByLabel = (label: string) =>
    page.getByRole('button', { name: new RegExp(`^[\\d,]+ ${label}$`) })
  const runningChip = chipByLabel('运行')
  const stoppedChip = chipByLabel('停止')
  const crashedChip = chipByLabel('崩溃')
  await expect(runningChip).toBeVisible()
  await expect(stoppedChip).toBeVisible()
  await expect(crashedChip).toBeVisible()

  // 点「运行」chip → status=RUNNING 写入 URL（可点设状态筛选，可发现「不正常的」）。
  await runningChip.click()
  await expect.poll(() => new URLSearchParams(new URL(page.url()).search).get('status')).toBe('RUNNING')
  // chip 进入激活（aria-pressed=true）态后再点同项（避免两次程序化点击抢在 React 提交前 = 假性 race）。
  await expect(runningChip).toHaveAttribute('aria-pressed', 'true')
  await runningChip.click()
  await expect.poll(() => new URLSearchParams(new URL(page.url()).search).get('status')).toBeNull()

  // 2) 表头含「节点:端口」列与「身份」列。
  //
  // 【为何是「身份」而不是「角色」或「类型·标签」】角色不是独立列——它以 `RoleBadge`
  // 画在身份列内部（`InstanceTableParts` 的 `case 'identity'`）；而该列的列名取
  // `instances.identity`，其真实值是 **「身份」**（不是代码里那个 `defaultValue: '类型·标签'`，
  // 那个默认值只在缺少 i18n 键时生效）。实测列头：
  //   ["","区域","名称","身份","节点:端口","状态","健康度","操作"]
  //
  // 【为何用正则而不是 exact】该列头内嵌排序按钮，可及名 = 列名 + 排序提示文本，
  // 所以 `{ name: '身份', exact: true }` 解析到 0，`/身份/` 才能命中（实测：exact=0 / regex=1）。
  await expect(page.getByTestId('instances-table-virtual')).toBeVisible()
  await expect(page.getByRole('columnheader', { name: /节点:端口/ })).toBeVisible()
  await expect(page.getByRole('columnheader', { name: /身份/ })).toBeVisible()

  // 3) 角色三态统一徽标：种子含 proxy（lobby-proxy）与 backend，徽标文案「代理」「后端」在场。
  await expect(page.getByText('代理').first()).toBeVisible()
  await expect(page.getByText('后端').first()).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptancesingle-machine-summary-columns.png', fullPage: false })
})

test('proxy 行 inline 展开已注册 backend 摘要', async ({ page }) => {
  await login(page)

  // 收敛到 proxy 种子实例，避免 1200 规模下滚动找行。
  await page.goto('/instances?view=list&q=lobby-proxy')
  const row = page.getByRole('row').filter({ has: page.getByRole('button', { name: 'lobby-proxy', exact: true }) })
  await expect(row).toBeVisible()

  // proxy 行前的展开切换按钮（aria-label = proxy.manageBackends「管理后端」）。
  const toggle = row.getByRole('button', { name: '管理后端' })
  await expect(toggle).toBeVisible()
  await toggle.click()

  // 展开后 inline 出现「已注册后端 N」摘要标题或「暂无已注册后端」空态（BackendsInline / proxy.registeredBackends|noBackends）。
  await expect(page.getByText(/已注册后端|暂无已注册后端/).first()).toBeVisible({ timeout: 10_000 })

  await page.screenshot({ path: '../.tmp/acceptancesingle-machine-proxy-inline.png', fullPage: false })
})
