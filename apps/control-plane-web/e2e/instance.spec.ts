import { test, expect, type Page, type Locator } from '@playwright/test'
import { login } from './helpers'

/** 
 *E2E：实例生命周期跨页流（mock 模式整站）。
 * 登录 → SPA 导航进实例页 → 看到种子实例 → 真 UI 启/停一个实例、断言状态联动；
 * 另含创建实例走通向导页后列表出现。
 *
 * mock 状态机是直达的（POST …/start→RUNNING、…/stop→STOPPED，无 STARTING/STOPPING 过渡，
 * 见 src/mocks/handlers/domains/instance.ts），确认后 ['instances'] query 失效重取即翻牌，
 * 无需等过渡轮询。用 SPA 点侧栏链接进页（保留会话内存库联动），不 page.goto 以免重置内存库。
 *
 * 【启停要过二次确认】卡片上的「启动/停止」会先开一个 DangerConfirm 弹窗
 * （「停止实例「xxx」？」+ 取消/停止），确定后才发请求。故每次点完主操作都要再点一次
 * 弹窗里的同名确认按钮——旧写法点完就断言，会停在“弹窗还在、状态未变”而超时。
 */

/** 
 * 工作台卡：每张卡直接带 `data-testid="instance-worktable-card"`。
 *
 * 【为何不再用 `div.bg-card`】卡片底色已收敛到 ui 包的 `Card`（`cardSurface` +
 * `cardSurfaceTone.solid`，类名是 `bg-card/95` 而非 `bg-card`）——原先按 `bg-card`
 * 这个**类名**定位的选择器会整体落空，且这类「以样式类当结构契约」的定位每次换肤都可能再挂一次。
 * 卡片自己给的 testid 才是稳定契约。
 *
 * 起 `/instances` 默认走分组树表，故 gotoInstances 会显式切到卡片视图再断言。
 */
function instanceCard(page: Page, name: string): Locator {
  return page.locator('[data-testid="instance-worktable-card"]').filter({
    has: page.getByRole('button', { name, exact: true }),
  })
}

/** 卡内状态徽章文案（data-slot=status-badge），与启停按钮的 aria-label 文案隔离避免歧义。 */
function cardStatus(card: Locator): Locator {
  return card.locator('[data-slot="status-badge"]')
}

/** 
 * 登录后经侧栏「服务器」入口 SPA 进入实例管理页（保留会话内状态联动）。
 *
 * 导航重构后的两处变化：
 * - 入口文案已随 i18n 统一由「全部服务器」改为「服务器」，且**仍带计数**
 *   （「服务器 1,200」），计数是同一链接可访问名的一部分，故不能再用 `exact: true`；
 * - 入口已提升为一级快捷项，不再藏在「服务器」分组头之下，于是没有「先展开分组」这一步。
 *   （`getByRole('button', { name: '服务器' })` 在顶栏会解析到 5 个元素——工作区切换、
 *   搜索、刷新等都含该词——属 strict mode 冲突，不能再用。）
 *
 *起默认视图是分组树表（view=list），本 spec 断言工作台卡，故进页后显式切到卡片视图。
 */
async function gotoInstances(page: Page): Promise<void> {
  await page.getByRole('link', { name: /^服务器/ }).click()
  await expect(page.locator('[data-page="instances"]')).toBeVisible()
  // 显式切卡片视图（ViewToggle aria-label = grouping.viewCard「卡片视图」）。
  // 卡片视图现为单一容器 + 单一虚拟窗口的行流（组头行与卡片行同属一个列表），
  // 故页面上只有一个 instances-card-virtual；此处仍以切换按钮的 aria-pressed 作为
  // 「已进入卡片视图」的就绪信号（与容器数量无关，切维度时同样成立）。
  await page.getByRole('button', { name: '卡片视图' }).click()
  await expect(page.getByRole('button', { name: '卡片视图' })).toHaveAttribute('aria-pressed', 'true')
}

test.describe('实例生命周期（mock 模式）', () => {
  test.beforeEach(async ({ page }) => {
    // 实例页在 mock large 规模下（1200 实例）首屏就绪可达十几秒；本文件每个用例都要
    // 先经侧栏进页、再点视图切换，叠起来很容易顶穿默认 30 秒上限——失败点却落在
    // 「元素 not found」，看不出是预算问题。与 instance-clone / bot-overview 同一口径。
    test.setTimeout(120_000)
    await login(page)
  })

  test('进实例页 → 看到种子实例', async ({ page }) => {
    await gotoInstances(page)
    // 卡片视图是**单一虚拟窗口**：只挂载首屏（改前按分组分段，每组各挂一个窗口，
    // 于是「每组第一屏」都在 DOM 里）。分组后 CRASHED 的 creative-1 属 region r2，
    // 落在首屏之外；本用例要一次看全三个种子，故显式进「无分组卡片视图」——
    // 种子 id 为 1/2/3 且默认按创建时间升序，三个必在首屏内。
    // 这一步是整页加载（不是 SPA 跳转），故放在用例最开始、任何内存态联动之前。
    await page.goto('/instances?view=card&groupBy=none')
    // 就绪等待给到 30s：mock large 规模（1200 实例）下「搜取 + 首屏渲染」实测临界 ~16s，
    // 而 `expect` 默认只有 15s。这不是冗余容错——「能看到实例」本就是这个用例要验的事。
    await expect(page.getByTestId('instances-card-virtual')).toBeVisible({ timeout: 30_000 })
    // 三个种子实例（RUNNING / STOPPED / CRASHED）均应在卡片视图出现。
    await expect(page.getByRole('button', { name: 'survival-1', exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: 'lobby-proxy', exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: 'creative-1', exact: true })).toBeVisible()
  })

  test('对 RUNNING 实例点「停止」→ 状态联动为停止', async ({ page }) => {
    await gotoInstances(page)
    const card = instanceCard(page, 'survival-1')
    await expect(card).toBeVisible()
    // 初始 RUNNING：状态徽章显「运行」，且有「停止」操作按钮（无「启动」）。
    await expect(cardStatus(card)).toHaveText('运行')
    const stopBtn = card.getByRole('button', { name: '停止', exact: true })
    await expect(stopBtn).toBeVisible()

    await stopBtn.click()
    // 二次确认弹窗（危险操作）：再点弹窗里的「停止」。
    const dialog = page.getByRole('dialog').filter({ hasText: '停止实例' })
    await expect(dialog).toBeVisible()
    await dialog.getByRole('button', { name: '停止', exact: true }).click()

    // 联动：状态翻到「停止」，主操作改为「启动」（停止按钮消失）。
    await expect(cardStatus(card)).toHaveText('停止', { timeout: 10_000 })
    await expect(card.getByRole('button', { name: '启动', exact: true })).toBeVisible()
    await expect(card.getByRole('button', { name: '停止', exact: true })).toHaveCount(0)
  })

  test('对 STOPPED 实例点「启动」→ 状态联动为运行', async ({ page }) => {
    await gotoInstances(page)
    const card = instanceCard(page, 'lobby-proxy')
    await expect(card).toBeVisible()
    // 初始 STOPPED：状态徽章显「停止」，且有「启动」操作按钮。
    await expect(cardStatus(card)).toHaveText('停止')
    const startBtn = card.getByRole('button', { name: '启动', exact: true })
    await expect(startBtn).toBeVisible()

    await startBtn.click()
    // 二次确认弹窗：再点弹窗里的「启动」。
    const startDialog = page.getByRole('dialog').filter({ hasText: '启动实例' })
    await expect(startDialog).toBeVisible()
    await startDialog.getByRole('button', { name: '启动', exact: true }).click()

    // 联动：状态翻到「运行」，主操作出现「停止」（启动按钮消失）。
    await expect(cardStatus(card)).toHaveText('运行', { timeout: 10_000 })
    await expect(card.getByRole('button', { name: '停止', exact: true })).toBeVisible()
    await expect(card.getByRole('button', { name: '启动', exact: true })).toHaveCount(0)
  })

  test('创建实例 → 列表出现新卡片', async ({ page }) => {
    await gotoInstances(page)
    const name = `e2e-srv-${Date.now()}`

    // 打开创建向导页（页眉「创建实例」按钮）。
    await page.getByRole('button', { name: '创建实例', exact: true }).click()
    await expect(page.getByRole('heading', { name: '创建实例' })).toBeVisible()

    // 基本信息：名称 + 节点。
    await page.getByPlaceholder('Survival Server').fill(name)
    await page.getByRole('button', { name: '选择节点' }).click()
    await page.getByRole('button', { name: /alpha/ }).click()
    await page.getByRole('button', { name: '下一步' }).click()

    // 启动命令有默认值，直接进入确认页。
    await page.getByRole('button', { name: '下一步' }).click()
    await page.getByRole('button', { name: '创建', exact: true }).click()

    // 提交成功后回到列表。
    await expect(page.locator('[data-page="instances"]')).toBeVisible({ timeout: 10_000 })

    // 1000+ mock 下列表只渲染可视窗口；用搜索收敛到新建项，避免按全集滚动找卡片。
    await page.getByRole('searchbox', { name: '搜索实例' }).fill(name)

    // 列表失效重取后新实例卡片出现（创建后默认 STOPPED）。
    await expect(page.getByRole('button', { name, exact: true })).toBeVisible({ timeout: 10_000 })
  })
})
