import { test, expect, type Page } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-122 经济汇聚与多区聚合 / FR-123 经济定制页（JBIS M2）· 单机（Playwright + mock 模式）验收。
 *
 * 经济定制台（EconomySegment）是可组合工作区的一张功能卡，只在超级工作台 `/super` 的画布上出现：
 * 从左侧「实例库」把实例 1 的「经济」功能卡拖到画布 → 卡内渲染四块（余额 / 排行 / 转账 / 流水）。
 *
 * mock 假后端（MSW）种子经济镜像：Steve coin 1000.00、Alex coin 250.50（node-a / zone 0），
 * 业务能力 manifest 含 economy 域（使定制台可用）。据此验：
 *   - FR-123：定制台四块视图渲染 + 余额查询命中镜像 + 排行 Top-N 数值倒序。
 *   - FR-122：余额多区聚合概览卡（同币种跨节点/区合并总额 + 分布区数）。
 *
 * 证据落 .tmp/acceptance/FR-122|123/。真机（真 mce 探针）由主控 chrome-devtools 补。
 */

/**
 * 在浏览器内手动模拟 HTML5 原生拖放：源触发 dragstart 写 dataTransfer，
 * 再对落点依次派发 dragenter / dragover / drop（共用同一 DataTransfer）。
 * Playwright 的 dragTo 对自定义 dataTransfer 载荷不可靠，故手动派发以确保 React onDragStart/onDrop 触发。
 * 落点用「画布为空」提示文案定位其可落容器（含 onDrop 的 flex-1 放置区）。
 */
async function dragEconomyToCanvas(page: Page): Promise<void> {
  await page.evaluate((emptyHint) => {
    // 源：标记过 data-e2e-econ-src 的「经济」功能拖拽源。
    const source = document.querySelector('li[data-e2e-econ-src="1"]')
    if (!source) throw new Error('经济功能拖拽源未找到')
    // 落点：含「画布为空」提示的节点，取其带 onDrop 的可滚动祖先（flex-1 放置区）。
    let hintEl: Element | null = null
    for (const el of Array.from(document.querySelectorAll('p'))) {
      if (el.textContent?.includes(emptyHint)) {
        hintEl = el
        break
      }
    }
    const target = (hintEl?.closest('.flex-1') ?? hintEl?.parentElement ?? document.body) as Element
    const dataTransfer = new DataTransfer()
    const fire = (el: Element, type: string) => {
      const rect = el.getBoundingClientRect()
      const ev = new DragEvent(type, {
        bubbles: true,
        cancelable: true,
        clientX: rect.left + rect.width / 2,
        clientY: rect.top + rect.height / 2,
      })
      Object.defineProperty(ev, 'dataTransfer', { value: dataTransfer })
      el.dispatchEvent(ev)
    }
    fire(source, 'dragstart')
    fire(target, 'dragenter')
    fire(target, 'dragover')
    fire(target, 'drop')
    fire(source, 'dragend')
  }, '画布为空')
}

/** 进入超级工作台，把实例 1 的「经济」功能卡拖到画布。 */
async function openEconomyCard(page: Page): Promise<void> {
  await login(page)
  await page.goto('/super')

  // 画布初始为空。
  await expect(page.getByText('画布为空').first()).toBeVisible()

  // 实例库懒加载 1200 实例，等首行出现（含选择复选框）。首行 = 某实例。
  const firstRow = page.locator('li:has(input[type="checkbox"])').first()
  await expect(firstRow).toBeVisible({ timeout: 15_000 })
  // 点其展开箭头（aria-expanded=false）露出功能子项（含「经济」拖拽源）。
  await firstRow.getByRole('button', { expanded: false }).click()

  // 「经济」功能拖拽源（FunctionDragItem，<li draggable> 文案取自 workspace.cardEconomy="经济"）。
  const economySource = page.locator('li[draggable="true"]').filter({ hasText: '经济' }).first()
  await expect(economySource).toBeVisible()
  await economySource.evaluate((el) => el.setAttribute('data-e2e-econ-src', '1'))

  await dragEconomyToCanvas(page)

  // 卡落位后，经济定制台正文标题渲染（EconomySegment，economy.title）。
  await expect(page.getByText('经济定制台').first()).toBeVisible({ timeout: 10_000 })
}

test('FR-123 经济定制页: 四块视图 + 余额查询命中镜像', async ({ page }) => {
  await openEconomyCard(page)

  // 定制台标题 + 四个子 Tab 全部呈现（余额 / 排行 / 转账 / 流水）。
  await expect(page.getByText('经济定制台').first()).toBeVisible()
  await expect(page.getByRole('tab', { name: '余额' }).first()).toBeVisible()
  await expect(page.getByRole('tab', { name: '排行' }).first()).toBeVisible()
  await expect(page.getByRole('tab', { name: '转账' }).first()).toBeVisible()
  await expect(page.getByRole('tab', { name: '流水' }).first()).toBeVisible()

  // 余额子页（默认 tab）：填玩家 Steve → 查询 → 命中镜像余额 1000.00。
  await page.getByLabel('玩家').first().fill('Steve')
  await page.getByRole('button', { name: '查询' }).first().click()
  await expect(page.getByText('1000.00').first()).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-123/single-machine-economy-balance.png', fullPage: false })
})

test('FR-122 多区聚合: 余额聚合概览 + 排行数值倒序', async ({ page }) => {
  await openEconomyCard(page)

  // 余额查询不填玩家，返回全部镜像行（Steve 1000.00 + Alex 250.50，同 coin 币种跨行）。
  await page.getByRole('button', { name: '查询' }).first().click()
  await expect(page.getByText('1000.00').first()).toBeVisible()
  await expect(page.getByText('250.50').first()).toBeVisible()

  // 多区聚合概览卡：同币种 coin 合并总额 = 1000.00 + 250.50 = 1250.5（sumDecimalStrings 精确十进制）。
  await expect(page.getByText('1250.5', { exact: false }).first()).toBeVisible()

  // 切到排行子页，按货币 coin 取 Top-N；数值倒序 → Steve(1000) 名次 1、Alex(250.5) 名次 2。
  await page.getByRole('tab', { name: '排行' }).first().click()
  await page.getByLabel('货币').first().fill('coin')
  await page.getByRole('button', { name: '查询' }).first().click()

  // 排行表出现两名玩家，且 Steve 在 Alex 之上（DOM 顺序 = 名次顺序）。
  const rankTable = page.locator('table', { hasText: 'Steve' }).first()
  await expect(rankTable).toBeVisible()
  await expect(rankTable.getByText('Alex').first()).toBeVisible()

  const bodyText = await rankTable.innerText()
  expect(bodyText.indexOf('Steve')).toBeLessThan(bodyText.indexOf('Alex'))

  await page.screenshot({ path: '../.tmp/acceptance/FR-122/single-machine-economy-aggregate.png', fullPage: false })
})
