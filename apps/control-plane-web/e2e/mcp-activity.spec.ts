import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * MCP 活动页（FR-391 / ADR-096）真浏览器 smoke。
 * 覆盖此前为零的两件事：① mock 模式下该端点有 handler、页面能正常渲染出数据行；
 * ② 窗口档位切到第二档时请求能被后端接受（HTTP 200）。
 *
 * 档位值一旦写成后端不认的形态（如 Go duration 不支持的 `7d`），
 * devmock 会照真后端语义回 400，本用例随之变红——这是该缺陷唯一的自动守卫。
 * 逐列细节归 McpActivityPage.dom.test.tsx，这里只验「能渲染 + 切档能成功」。
 */

const ACTIVITY_PATH = '/api/v1/agent/mcp/activity'

const isActivityGet = (method: string, url: string) =>
  method === 'GET' && new URL(url).pathname === ACTIVITY_PATH

test('MCP 活动页：渲染 mock 活动行 + 切档请求成功', async ({ page }) => {
  await login(page)

  // 首屏请求在导航前挂监听，避免慢速运行时抢在响应回来前断言。
  const initialPromise = page.waitForResponse((r) => isActivityGet(r.request().method(), r.url()))
  await page.goto('/mcp-activity')
  const initial = await initialPromise
  expect(initial.status()).toBe(200)

  await expect(page.getByRole('heading', { name: 'MCP 活动' })).toBeVisible()
  // 断言表格里的真实内容（token 名 / 调用数 / 来源 IP），不是「有 table 就算过」。
  await expect(page.getByRole('cell', { name: /claude-code-dev/ })).toBeVisible()
  await expect(page.getByRole('cell', { name: /ci-nightly/ })).toBeVisible()
  await expect(page.getByRole('cell', { name: '428', exact: true })).toBeVisible()
  await expect(page.getByRole('cell', { name: '203.0.113.24', exact: true })).toBeVisible()
  // 默认 24h 窗口看不到 72 小时前的活动，且没有落进错误态。
  await expect(page.getByText('ops-laptop')).toHaveCount(0)
  await expect(page.getByText('加载 MCP 活动失败')).toHaveCount(0)

  // 档位按钮文案来自 i18n（键名与档位值同名），按分组内顺序定位第二档，免得断言绑死在文案上。
  const presets = page.getByRole('group', { name: '统计窗口' }).getByRole('button')
  await expect(presets).toHaveCount(2)
  await expect(presets.nth(0)).toHaveAttribute('aria-pressed', 'true')

  // 以初始档位值做排除，避免 10s 轮询的旧窗口响应被误捕。
  const initialWindow = new URL(initial.url()).searchParams.get('window')
  const switchedPromise = page.waitForResponse((r) => {
    const url = new URL(r.url())
    return isActivityGet(r.request().method(), r.url()) && url.searchParams.get('window') !== initialWindow
  })
  await presets.nth(1).click()
  const switched = await switchedPromise

  // 200 才说明第二档的 window 真能被后端解析并落在允许区间内（非法/越界一律 400）。
  expect(switched.status(), `切档响应体：${await switched.text()}`).toBe(200)
  await expect(presets.nth(1)).toHaveAttribute('aria-pressed', 'true')
  // 切档后表格仍渲染：168h 窗口能看到 24h 窗口之外更早的活动。
  await expect(page.getByText('ops-laptop')).toBeVisible()
  await expect(page.getByText('加载 MCP 活动失败')).toHaveCount(0)
})
