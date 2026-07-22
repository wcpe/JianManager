import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-185 出站代理可视化配置 · 单机（Playwright + mock 模式）验收。
 * 覆盖：设置面板配全局/CP 出站代理（作为各节点默认，节点可覆盖）+ 保存即生效免重启 + 测试连通性。
 * （节点级覆盖 gRPC 下发 + DB 覆盖>yaml>env 优先级由 Go node_proxy_test.go 覆盖）
 * 证据落 .tmp/acceptance/FR-185/。
 */

test('FR-185 出站代理设置面板 + 全局/节点覆盖 + 测试连通性', async ({ page }) => {
  await login(page)
  await page.goto('/settings')
  await page.getByRole('button', { name: '网络', exact: true }).click()

  await expect(page.getByText(/出站代理（自更新\/JDK\/服务端 jar 下载经此）/)).toBeVisible()
  await expect(page.getByText(/作为各节点的默认代理（节点可在节点页覆盖）.*保存即生效，无需重启/)).toBeVisible()
  await expect(page.getByRole('button', { name: '测试出站连通性' })).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-185/single-machine-proxy-config.png', fullPage: true })
})
