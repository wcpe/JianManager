import { test, expect, type Page, type Locator } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-211 E2E：实例生命周期跨页流（mock 模式整站）。
 * 登录 → 进入实例页 → 看到 FR-201 种子实例 → 真 UI 启/停一个实例、断言状态联动；
 * 另含 FR-028 创建实例走通独立向导后列表出现。
 *
 * mock 状态机是直达的（POST …/start→RUNNING、…/stop→STOPPED，无 STARTING/STOPPING 过渡，
 * 见 src/mocks/handlers/domains/instance.ts），点击后 ['instances'] query 失效重取即翻牌，
 * 无需等过渡轮询。
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

/** 登录后进入实例管理页。 */
async function gotoInstances(page: Page): Promise<void> {
  await page.goto('/instances')
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

    // 打开独立创建向导页。
    await page.getByRole('button', { name: '创建实例', exact: true }).click()
    await expect(page.getByRole('heading', { name: '创建实例' })).toBeVisible()

    // 基本信息：实例名 + 节点。
    await page.getByPlaceholder('Survival Server').fill(name)
    await page.getByText('选择节点', { exact: true }).click()
    await page.getByRole('button', { name: /alpha/ }).click()
    await page.getByRole('button', { name: '下一步', exact: true }).click()

    // 启动配置：保留 daemon，填写启动命令后进入确认页。
    await page.getByPlaceholder('java -Xmx2G -jar server.jar nogui').fill('java -jar server.jar nogui')
    await page.getByRole('button', { name: '下一步', exact: true }).click()
    await expect(page.getByText(name)).toBeVisible()

    // 提交后回到实例列表，新实例卡片出现在追加的末尾（创建后默认 STOPPED）。
    await page.getByRole('button', { name: '创建', exact: true }).click()
    await expect(page.getByRole('heading', { name: '实例管理' })).toBeVisible({ timeout: 10_000 })
    await page.getByTestId('instances-card-virtual').evaluate((el) => { el.scrollTop = el.scrollHeight })
    await expect(page.getByRole('button', { name, exact: true })).toBeVisible({ timeout: 10_000 })
  })
})
