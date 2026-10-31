import { test, expect } from '@playwright/test'
import { login } from './helpers'

/** 
 *实例批量操作 · 单机（Playwright + mock 模式）验收。
 * 覆盖：列表视图行内多选 → 浮现批量操作栏（已选计数）→ 直接动作（批量重启）落结果反馈；
 * 危险动作（批量强制关服）弹二次确认并要求输入 FORCE 关键字后才放行。
 * （批量下发 payload、部分失败保留重试、状态感知禁用由 InstanceBatchBar.dom.test.tsx 单测覆盖）
 *
 * 说明：起实例页默认为分组树表；批量多选复选框在列表视图（树表）行内，故用 `view=list` 深链；
 * mock 生成 1200 实例（server-XXXX），status = STATUS_POOL[id%5]，故 id%5==0 为 RUNNING。
 * 用 `q=` + `status=RUNNING` 深链把可见集合钉到已知运行中实例，行复选框 aria-label = 实例名。
 * 证据落 .tmp/acceptance。
 */

test('列表多选 → 批量操作栏 → 批量重启结果反馈 + 强制关服 FORCE 二次确认', async ({ page }) => {
  // CI runner 上单路由就绪就要 10~20s，本用例要 登录 → 实例列表(1200 种子) → 勾选行 → 批量栏 → 二次确认，
  // 默认 60s 预算在慢 runner 上会超时（是预算不够，不是断言失败）。
  test.setTimeout(120_000)
  await login(page)
  // 列表视图 + 仅看运行中 + 搜索钉到 server-0005（id=5，RUNNING）。
  await page.goto('/instances?view=list&status=RUNNING&q=server-0005')

  // 列表视图表格就位，目标运行中实例行可见（行复选框 aria-label = 实例名）。
  const rowCheckbox = page.getByRole('checkbox', { name: 'server-0005' }).first()
  await expect(rowCheckbox).toBeVisible({ timeout: 10_000 })
  await rowCheckbox.check()

  // 选中后浮现批量操作栏，展示已选计数。
  await expect(page.getByText('已选 1 个')).toBeVisible()

  // 批量重启以对应 action 的 POST 响应作为完成信号，避免旧 toast 造成误判。
  const restartResponsePromise = page.waitForResponse((response) => {
    const request = response.request()
    const pathname = new URL(response.url()).pathname
    return (
      request.method() === 'POST' &&
      pathname === '/api/v1/instances/batch' &&
      request.postDataJSON().action === 'restart'
    )
  })
  await page.getByRole('button', { name: '批量重启' }).click()
  const restartResponse = await restartResponsePromise
  expect(restartResponse.ok()).toBe(true)

  // 响应成功后再断言结果反馈与选择清理。
  await expect(page.locator('[data-sonner-toast]').filter({ hasText: /批量完成：成功 \d+/ }).first()).toBeVisible({
    timeout: 15_000,
  })

  // 【为何重新取行单选及并等可交互】重启会把状态改成 RUNNING，列表随后 invalidate 重取，
  // 整行 DOM 重建；上文的 rowCheckbox 句柄指向旧节点，直接 check() 在 CI 上会
  // 「一直等不到可操作」直至用例预算耗尽（表现为点了 120s 的 locator.click 超时）。
  const rowCheckbox2 = page.getByRole('checkbox', { name: 'server-0005' }).first()
  await expect(rowCheckbox2).toBeVisible({ timeout: 30_000 })
  await expect(rowCheckbox2).toBeEnabled()
  await expect(rowCheckbox2).not.toBeChecked()
  await rowCheckbox2.check()
  await expect(page.getByText('已选 1 个')).toBeVisible({ timeout: 15_000 })

  // 危险动作：批量强制关服需输入 FORCE 才放行。
  // 该按钮在「所选实例里有可 kill 状态（RUNNING/STARTING/STOPPING）」时才 enabled，
  // 所以先把 enabled 等出来再点，而不是拿一个 disabled 按钮去 click 等到超时。
  const killBtn = page.getByRole('button', { name: '批量强制关服' })
  await expect(killBtn).toBeEnabled({ timeout: 15_000 })
  await killBtn.click()
  const killDialog = page.getByRole('dialog', { name: '确认批量强制关服？' })
  await expect(killDialog).toBeVisible()
  const execute = killDialog.getByRole('button', { name: '执行' })
  await expect(execute).toBeDisabled()
  await killDialog.getByPlaceholder('FORCE').fill('FORCE')
  await expect(execute).toBeEnabled()

  await page.screenshot({ path: '../.tmp/acceptancesingle-machine-instance-batch.png', fullPage: true })

  // 强制关服同样按 kill action 的 POST 响应判定完成，旧的重启 toast 不参与同步。
  const killResponsePromise = page.waitForResponse((response) => {
    const request = response.request()
    const pathname = new URL(response.url()).pathname
    return (
      request.method() === 'POST' &&
      pathname === '/api/v1/instances/batch' &&
      request.postDataJSON().action === 'kill'
    )
  })
  await execute.click()
  const killResponse = await killResponsePromise
  expect(killResponse.ok()).toBe(true)

  // 响应成功后确认危险动作对话框关闭且选择栏已清理；实例状态变化后该行可能移出当前筛选结果。
  await expect(killDialog).not.toBeVisible()
  await expect(page.getByText('已选 1 个')).not.toBeVisible()
})
