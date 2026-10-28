import { test, expect, type Page } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-513 主机级观测四处新版式的真机截图取证。
 *
 * ## 为何要单独一个 spec
 * FR-513 的实现（Worker `hostobs` 采集 → 心跳 → CP 入库 → 首页悬浮卡）已落地且有单测，
 * 但 spec 验收要求「在真实浏览器里肉眼看四处新版式」——单测只能证明组件在构造数据下渲染正确，
 * 证明不了整条链路（接口 → 前端 → 版式）在真实页面里是通的。
 *
 * ## 四处版式（对应 spec §6 四个待定项的最终形态）
 * 1. 负载三档（1m / 5m / 15m）——单值看不出「瞬时尖峰」还是「持续爬升」
 * 2. CPU 构成（用户态 / 内核态 / iowait …）——iowait 高是 IO 瓶颈，不是 CPU 不够
 * 3. 分挂载点磁盘——单值只覆盖根分区，`/var` 写满而 `/` 宽松时完全无感
 * 4. 整机 Top 进程 + 进程数——只看受管进程树会得出「我们都闲，为何机器卡」
 *
 * 截图落在 `test-results/fr513/`，作为验收证据附在交付里。
 */

const SHOT_DIR = 'test-results/fr513'

/**
 * 打开**经典**首页（三个资源球所在页），等外壳与页面就绪。
 *
 * 【为何必须带 `?home=classic`】自 FR-520 §9.1 起，`/` 默认渲染 **AI 首页**；
 * 经典仪表盘（就是带三个资源球与归因浮层的那个）需显式参数进入。
 * 不带参数等 `[data-page="overview"]` 会永远超时——这一步踩过一次。
 */
async function openOverview(page: Page): Promise<void> {
  await page.goto('/?home=classic')
  await expect(page.locator('[data-slot="console-shell"]'), '控制台外壳就绪').toBeVisible({ timeout: 30_000 })
  await expect(page.locator('[data-page="overview"]'), '经典首页就绪').toBeVisible({ timeout: 30_000 })
}

/** 点开某个资源球的归因浮层（负载球最能看到全部四处）。 */
async function openAttributionPopover(page: Page, label: string): Promise<void> {
  const trigger = page.getByRole('button', { name: new RegExp(`资源归因|${label}`) }).first()
  await expect(trigger, `${label} 归因触发器可见`).toBeVisible({ timeout: 20_000 })
  await trigger.click()
  await expect(page.getByText('负载三档'), '归因浮层已展开').toBeVisible({ timeout: 20_000 })
}

test.describe('FR-513 主机级观测版式取证（真浏览器）', () => {
  test.beforeEach(async ({ page }) => {
    test.setTimeout(180_000)
    await login(page)
  })

  test('四处新版式全部渲染并逐块截图', async ({ page }) => {
    await openOverview(page)
    await openAttributionPopover(page, 'CPU')

    // ── ① 负载三档 ──
    await expect(page.getByText('负载三档'), '① 负载三档标题').toBeVisible()
    // 三档都必须在：只显示 1m 等于没修。
    // 用 aria-label + `exact: true` 定位：
    // - 文案正则 `/5m /` 会同时命中 `15m `（子串）；
    // - `getByLabel` 默认也是**子串匹配**，`5 分钟负载` 会命中 `15 分钟负载`。
    // 两个坑都踩过，必须 exact。
    const popover = page.locator('[data-radix-popper-content-wrapper]').first()
    await expect(popover.getByLabel('1 分钟负载', { exact: true }), '① 1m 读数').toBeVisible()
    await expect(popover.getByLabel('5 分钟负载', { exact: true }), '① 5m 读数').toBeVisible()
    await expect(popover.getByLabel('15 分钟负载', { exact: true }), '① 15m 读数').toBeVisible()
    await page.screenshot({ path: `${SHOT_DIR}/01-load-tiers.png`, fullPage: false })

    // ── ② CPU 构成 ──
    await expect(page.getByText('CPU 构成'), '② CPU 构成标题').toBeVisible()
    await expect(page.getByText('用户态'), '② 用户态一行').toBeVisible()
    await expect(page.getByText('内核态'), '② 内核态一行').toBeVisible()
    // iowait 是这套版式存在的核心理由：必须有
    await expect(page.getByText(/iowait|IO 等待/), '② iowait 一行').toBeVisible()
    await page.screenshot({ path: `${SHOT_DIR}/02-cpu-breakdown.png`, fullPage: false })

    // ── ③ 分挂载点磁盘 ──
    await expect(page.getByText('分挂载点磁盘'), '③ 分挂载点磁盘标题').toBeVisible()
    await expect(page.getByText('/var'), '③ /var 挂载点').toBeVisible()
    await page.screenshot({ path: `${SHOT_DIR}/03-mounts.png`, fullPage: false })

    // ── ④ 整机 Top 进程 + 进程数 ──
    await expect(page.getByText('整机 Top 进程'), '④ 整机 Top 进程标题').toBeVisible()
    // 与「Top 受管进程」必须是两个不同标题（口径不同的证据）
    await expect(page.getByText(/Top 受管进程/), '④ 受管 Top 与整机 Top 可区分').toBeVisible()
    await expect(page.getByText(/进程数/), '④ 进程数一行').toBeVisible()
    await page.screenshot({ path: `${SHOT_DIR}/04-host-top-processes.png`, fullPage: false })

    // 整张浮层完整版（交付主图）
    await page.screenshot({ path: `${SHOT_DIR}/00-all-four-blocks.png`, fullPage: false })
  })

  test.skip('无主机级数据时四处版式整体不占位（老 Worker 兼容）', async ({ page }) => {
    /*
      【为何 skip：技术上做不到，且已有更合适的归属】
      这条断言本身很重要（老 Worker 不报主机级字段时不能渲染四个空壳），但它**已在
      dom 层被完整覆盖**：`OverviewHostObs.dom.test.tsx` 第 55 行
      「未上报时四处新版式都不渲染（不占位）」直接用构造数据断言四个标题全部为 null，
      那比经过整条链路的 e2e 更精确也更稳定。

      在 e2e 里重做这件事的技术障碍：mock 是 **MSW Service Worker**
      （`startMockWorker()`，见 main.tsx 注释），请求在 SW 线程里被拦截，
      `page.route()` **拦不到**——本用例首版就是因此失败（四个标题仍存在）。
      要绕过得先 unregister SW 再自己重建 mock，成本远大于它带来的额外价值。

      保留这个 skip 而非删掉，是为了把「为什么不在这里做」写清楚，
      避免以后有人以为是漏写而不停重试。
    */
    await page.goto('/?home=classic')
  })
})
