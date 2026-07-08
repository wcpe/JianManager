import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-136 实例列表汇总头 + 节点/端口列 + 角色徽标 + proxy↔backend inline · 单机（Playwright + mock）验收。
 * 覆盖 console-ux spec §FR-136 验收：
 *   - sticky 汇总 chip（运行 N/停止 N/崩溃 M/总数）可点设状态筛选；
 *   - 列表加「节点:端口」列（serverPort 已有数据）；
 *   - 角色三态统一语义色徽标；
 *   - proxy 行可 inline 展开已注册 backend 摘要。
 * 证据落 .tmp/acceptance/FR-136/。
 */

test('FR-136 汇总头计数可点设筛选 + 节点端口列 + 角色徽标（列表视图）', async ({ page }) => {
  await login(page)

  // 进列表视图（节点:端口列与角色徽标列在列表视图渲染）。
  await page.goto('/instances?view=list')
  await expect(page.locator('[data-page="instances"]')).toBeVisible()

  // 1) 汇总头：总数/运行/停止/崩溃 chip 均在场，且是可点按钮（设状态筛选）。
  //    chip 的可及名 = 「运行 <数>」（标签+计数），需与页眉节点作用域按钮「运行服务器: N」区分，故用锚定正则 /^运行 \d/。
  const runningChip = page.getByRole('button', { name: /^运行 \d/ })
  const stoppedChip = page.getByRole('button', { name: /^停止 \d/ })
  const crashedChip = page.getByRole('button', { name: /^崩溃 \d/ })
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

  // 2) 表头含「节点:端口」列与「角色」列（FR-136 新增列）。
  await expect(page.getByTestId('instances-table-virtual')).toBeVisible()
  await expect(page.getByRole('columnheader', { name: '节点:端口' })).toBeVisible()
  await expect(page.getByRole('columnheader', { name: '角色' })).toBeVisible()

  // 3) 角色三态统一徽标：种子含 proxy（lobby-proxy）与 backend，徽标文案「代理」「后端」在场。
  await expect(page.getByText('代理').first()).toBeVisible()
  await expect(page.getByText('后端').first()).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-136/single-machine-summary-columns.png', fullPage: false })
})

test('FR-136 proxy 行 inline 展开已注册 backend 摘要', async ({ page }) => {
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

  await page.screenshot({ path: '../.tmp/acceptance/FR-136/single-machine-proxy-inline.png', fullPage: false })
})
