import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-135 开源许可与依赖清单页 · 单机（Playwright + mock 模式）验收。
 * 覆盖：从侧栏底部「开源许可」入口进 /licenses（带返回）；照参考图布局——搜索框（按包名过滤）+
 * 运行时/开发分区计数 + 表格[包名·版本·许可证·作者] + 行内展开许可证全文；五类发行来源完整覆盖。
 * 证据落 .tmp/acceptance/FR-135/。
 */

test('FR-135 侧栏入口进开源许可页 + 依赖清单渲染 + 搜索过滤 + 行内展开全文', async ({ page }) => {
  await login(page)

  // 从侧栏底部「开源许可」入口进入（验入口连通性）
  // 侧栏另有一处 licenses 导航项（jm-nav-link），此处点底部页脚入口（FR-132 右下）。
  await page.locator('aside a[href="/licenses"]:not(.jm-nav-link)').first().click()
  await expect(page).toHaveURL(/\/licenses$/)

  // 页头 + 返回按钮（带返回）
  await expect(page.getByRole('heading', { name: '开源许可' })).toBeVisible()
  await expect(page.getByRole('button', { name: '返回' })).toBeVisible()

  // 五类发行来源均有代表依赖，Java 来源不得静默漏扫。
  await expect(page.getByText('react', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('mineflayer', { exact: true }).first()).toBeVisible()
  await expect(page.getByText('github.com/gin-gonic/gin').first()).toBeVisible()
  await expect(page.getByText('com.github.luben:zstd-jni').first()).toBeVisible()
  await expect(page.getByText('org.ow2.asm:asm', { exact: true }).first()).toBeVisible()

  // 运行时/开发分区计数（表格标题旁计数徽标）+ 表头列
  await expect(page.getByText('运行时依赖').first()).toBeVisible()
  await expect(page.getByText('开发依赖').first()).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-135/single-machine-licenses-list.png', fullPage: false })

  // 行内展开 → 许可证全文。react 名列渲染为外链 <a>（点击会 stopPropagation），
  // 故先由「精确文本 react 的名列外链」定位其所在行，再点该行首列（展开图标）触发行 onClick。
  const reactRow = page.getByRole('link', { name: 'react', exact: true }).first().locator('xpath=ancestor::tr[1]')
  await reactRow.locator('td').first().click()
  await expect(page.getByText(/MIT License/).first()).toBeVisible()

  // 搜索过滤（按包名收敛）
  await page.getByPlaceholder('按包名过滤…').fill('gin-gonic/gin')
  await expect(page.getByText('github.com/gin-gonic/gin').first()).toBeVisible()
  await expect(page.getByText('vitest', { exact: true })).toHaveCount(0)
  await page.screenshot({ path: '../.tmp/acceptance/FR-135/single-machine-licenses-filtered.png', fullPage: false })
})
