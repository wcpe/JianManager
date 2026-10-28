import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-520 §5.1 / §8 窄屏布局验收（真浏览器 + 真实小视口）。
 *
 * 为什么必须单列成 e2e：spec 明确要求「窄屏（<768px）单栏，摘要区**不折叠成横向滚动**」，
 * 而这两条都是**布局事实**，jsdom 没有 CSS 引擎、量不出列数与溢出；浏览器探针也改不了视口。
 * Playwright 能设任意 viewport，是唯一能真正判定它的地方。
 *
 * 断言的取法都很「硬」：
 * - **列数**取 `gridTemplateColumns` 计算值的**轨道个数**，不是读 class 名——
 *   class 写对了但被别的规则覆盖，读 class 会假绿。
 * - **横向滚动**用 `scrollWidth > clientWidth` 判定，这是浏览器自己算出来的溢出。
 * - **顺序**取各区块的 `getBoundingClientRect().top` 排序，而不是 DOM 顺序——
 *   CSS（如 flex order / grid area）可以在不改 DOM 的情况下改变视觉顺序。
 */

/** 桌面宽屏：摘要应在右栏（与对话区同一行）。 */
const WIDE = { width: 1280, height: 900 }
/** 窄屏：取 iPhone 竖屏宽度，远低于 768 断点。 */
const NARROW = { width: 375, height: 812 }

/** 取某个元素的 grid 轨道数（按计算值切分，`none` 视为 1 轨）。 */
async function gridTracks(locator: import('@playwright/test').Locator): Promise<number> {
  const raw = await locator.evaluate((el) => getComputedStyle(el).gridTemplateColumns)
  if (!raw || raw === 'none') return 1
  return raw.trim().split(/\s+/).filter(Boolean).length
}

test.describe('FR-520 首页响应式布局', () => {
  test('宽屏两栏、窄屏单栏，且窄屏摘要区不横向滚动', async ({ page }) => {
    await login(page)
    await page.goto('/')
    await expect(page.locator('[data-page="assistant-home"]')).toBeVisible({ timeout: 20_000 })

    // 首页两栏容器的网格：宽屏 [minmax(0,1fr) 20rem]、窄屏 grid-cols-1。
    const grid = page.locator('[data-page="assistant-home"] .grid').first()
    await expect(grid).toBeVisible()

    // ── 宽屏 ──
    await page.setViewportSize(WIDE)
    // 等一帧让媒体查询重算布局，否则读到的是上一次视口下的计算值。
    await page.waitForTimeout(120)
    expect(await gridTracks(grid), '宽屏应为两栏').toBe(2)

    // ── 窄屏 ──
    await page.setViewportSize(NARROW)
    await page.waitForTimeout(120)
    expect(await gridTracks(grid), '窄屏应为单栏').toBe(1)

    // 摘要区不横向滚动：这是 spec 点名的那条「不折叠成横向滚动」。
    const overflow = await page.evaluate(() => {
      const root = document.querySelector('[data-page="assistant-home"]') as HTMLElement | null
      if (!root) return null
      // 逐块检查：任何一块横向溢出都算不合格（横向滚动条正是这么来的）。
      const blocks = Array.from(root.querySelectorAll<HTMLElement>('*'))
      const bad = blocks
        .filter((el) => el.scrollWidth > el.clientWidth + 1)
        // 排除本来就有意横向滚动的容器（表格/代码块在窄屏横向滚动是合理的）。
        .filter((el) => !el.closest('table') && !el.matches('pre, code'))
        // 排除文本截断元素：`truncate` / `text-ellipsis` **按设计**就会让
        // scrollWidth > clientWidth（内容被省略号吃掉），它本身不产生滚动条、
        // 也不会把页面撑宽。不排掉它，这条断言会把「正常省略」误报成「横向滚动」。
        .filter((el) => {
          const cs = getComputedStyle(el)
          return cs.textOverflow !== 'ellipsis' && cs.overflowX !== 'hidden'
        })
        .map((el) => `${el.tagName}.${String(el.className).slice(0, 40)}`)
      return bad
    })
    expect(overflow, '窄屏下不应出现横向溢出').toEqual([])

    // 整页也不该横向滚动（更外层的兜底判定）。
    const pageOverflow = await page.evaluate(
      () => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
    )
    expect(pageOverflow, '窄屏整页不应横向滚动').toBe(false)

    await page.screenshot({
      path: '../.tmp/acceptance/FR-520/narrow-375.png',
      fullPage: true,
    })
  })

  test('窄屏顺序：问候 → 对话 → 建议 → 需要关注 → 近期任务 → 集群水位 → 最近会话', async ({ page }) => {
    await login(page)
    await page.setViewportSize(NARROW)
    await page.goto('/')
    const root = page.locator('[data-page="assistant-home"]')
    await expect(root).toBeVisible({ timeout: 20_000 })

    /**
     * 按 y 坐标取视觉顺序。
     *
     * 不按 DOM 顺序断言：grid/flex 可以不改 DOM 就改变视觉顺序，
     * 而用户看到的顺序才是 spec 约束的东西。
     */
    const order = await page.evaluate(() => {
      const root = document.querySelector('[data-page="assistant-home"]')
      if (!root) return []
      const marks: Array<[string, Element | null]> = [
        ['问候', root.querySelector('p.text-sm.font-medium')],
        ['对话', [...root.querySelectorAll('*')].find((el) => el.textContent?.trim() === '对话') ?? null],
        ['建议', [...root.querySelectorAll('*')].find((el) => el.textContent?.trim() === '可以这样问') ?? null],
        ['需要关注', [...root.querySelectorAll('*')].find((el) => el.textContent?.trim() === '需要关注') ?? null],
        ['近期任务', [...root.querySelectorAll('*')].find((el) => el.textContent?.trim() === '近期任务') ?? null],
        ['集群水位', [...root.querySelectorAll('*')].find((el) => el.textContent?.trim() === '集群水位') ?? null],
        ['最近会话', [...root.querySelectorAll('*')].find((el) => el.textContent?.trim() === '最近会话') ?? null],
      ]
      return marks
        .filter(([, el]) => el !== null)
        .map(([name, el]) => ({ name, top: Math.round((el as Element).getBoundingClientRect().top) }))
        .sort((a, b) => a.top - b.top)
        .map((x) => x.name)
    })

    // 这几块是**无条件渲染**的（见 AssistantHomePage：需要关注与近期任务不在任何条件分支里）。
    // 先断言它们都找得到，否则下面的 indexOf 会拿 -1 比较，用例会在「区块根本没渲染」时
    // 依然通过——第一版就是这样空转绿的（靠 filter 掉 null 掩盖了缺失）。
    for (const must of ['问候', '对话', '建议', '需要关注', '近期任务']) {
      expect(order, `窄屏下应能找到区块「${must}」`).toContain(must)
    }

    // 视觉顺序（按 y 坐标，不是 DOM 顺序）：问候 → 对话 → 建议 → 需要关注 → 近期任务 → 集群水位 → 最近会话。
    const idx = (name: string) => order.indexOf(name)
    expect(idx('问候')).toBeLessThan(idx('对话'))
    expect(idx('对话')).toBeLessThan(idx('建议'))
    expect(idx('建议')).toBeLessThan(idx('需要关注'))
    expect(idx('需要关注')).toBeLessThan(idx('近期任务'))
    // 有条件出现的区块：若存在，必须排在最后（spec 里它在顺序末端）。
    if (idx('集群水位') >= 0) expect(idx('近期任务')).toBeLessThan(idx('集群水位'))
    if (idx('最近会话') >= 0) expect(idx('近期任务')).toBeLessThan(idx('最近会话'))

    // 明确截全高：默认视口只有 812px，摘要区在折叠线下方，只截首屏会让人误以为它没渲染。
    await page.screenshot({ path: '../.tmp/acceptance/FR-520/narrow-full-375.png', fullPage: true })
  })
})
