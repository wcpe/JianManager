import { test, expect, type Page, type Locator } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-211 E2E：实例生命周期跨页流（mock 模式整站）。
 * 登录 → SPA 导航进实例页 → 看到 FR-201 种子实例 → 真 UI 启/停一个实例、断言状态联动；
 * 另含创建实例走通对话框后列表出现。
 *
 * mock 状态机是直达的（POST …/start→RUNNING、…/stop→STOPPED，无 STARTING/STOPPING 过渡，
 * 见 src/mocks/handlers/domains/instance.ts），点击后 ['instances'] query 失效重取即翻牌，
 * 无需等过渡轮询。用 SPA 点侧栏链接进页（保留会话内存库联动），不 page.goto 以免重置内存库。
 */

/** 实例页默认卡片视图：每张工作台卡是含实例名按钮的卡片容器（带 bg-card）。 */
function instanceCard(page: Page, name: string): Locator {
  return page.locator('div.bg-card').filter({
    has: page.getByRole('button', { name, exact: true }),
  })
}

/** 卡内状态徽章文案（data-slot=status-badge），与启停按钮的 aria-label 文案隔离避免歧义。 */
function cardStatus(card: Locator): Locator {
  return card.locator('[data-slot="status-badge"]')
}

/**
 * 登录后经侧栏「全部实例」链接 SPA 进入实例管理页（保留会话内状态联动）。
 * 「全部实例」在可折叠的「集群」域下，默认展开；若被折叠则先点组头展开再点链接。
 */
async function gotoInstances(page: Page): Promise<void> {
  const link = page.getByRole('link', { name: '全部实例', exact: true })
  if (!(await link.isVisible())) {
    await page.getByRole('button', { name: '集群' }).click()
  }
  await link.click()
  await expect(page.getByRole('heading', { name: '实例管理' })).toBeVisible()
}

test.describe('实例生命周期（mock 模式，FR-211）', () => {
  test.beforeEach(async ({ page }) => {
    await login(page)
  })

  test('进实例页 → 看到 FR-201 种子实例', async ({ page }) => {
    await gotoInstances(page)
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

    // 联动：状态翻到「运行」，主操作出现「停止」（启动按钮消失）。
    await expect(cardStatus(card)).toHaveText('运行', { timeout: 10_000 })
    await expect(card.getByRole('button', { name: '停止', exact: true })).toBeVisible()
    await expect(card.getByRole('button', { name: '启动', exact: true })).toHaveCount(0)
  })

  test('创建实例 → 列表出现新卡片', async ({ page }) => {
    await gotoInstances(page)
    const name = `e2e-srv-${Date.now()}`

    // 打开创建对话框（页眉「创建实例」按钮）。
    await page.getByRole('button', { name: '创建实例', exact: true }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: '创建实例' })).toBeVisible()

    // 名称（必填）。对话框内文本输入框用 placeholder 定位最稳。
    await dialog.getByPlaceholder('Survival Server').fill(name)

    // 节点（必填，Combobox allowCustom=false）：点触发器展开 → 选种子在线节点 alpha。
    await dialog.getByText('选择节点', { exact: true }).click()
    await page.getByRole('button', { name: 'alpha', exact: true }).click()

    // 启动命令（必填）。
    await dialog.getByPlaceholder('java -Xmx2G -jar paper.jar nogui').fill('java -jar server.jar nogui')

    // 提交并等对话框关闭。
    await dialog.getByRole('button', { name: '创建', exact: true }).click()
    await expect(dialog).toHaveCount(0, { timeout: 10_000 })

    // 列表失效重取后新实例卡片出现（创建后默认 STOPPED）。
    await expect(page.getByRole('button', { name, exact: true })).toBeVisible({ timeout: 10_000 })
  })
})
