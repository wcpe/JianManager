import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-170 进程粒度监控采集 · 单机（Playwright + mock 模式）验收。
 * 覆盖：监控页 进程 TOP10（每进程 CPU/内存/IO）。
 * （Worker 每进程采样 + CP 存储/TTL/每实例最新查询由 Go process_metrics_test.go / metric_heartbeat_test.go 覆盖；
 *  真实 OS 进程采集待整机联调）
 * 证据落 .tmp/acceptance/FR-170/。
 */

test('FR-170 监控页 进程 TOP10 渲染', async ({ page }) => {
  await login(page)
  await page.goto('/monitor')

  await expect(page.getByText('进程 TOP10')).toBeVisible()
  // 进程条目（PID + 进程名 java）
  await expect(page.getByText('24512').first()).toBeVisible()
  await expect(page.getByText('进程内存').first()).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-170/single-machine-process-top.png', fullPage: true })
})
