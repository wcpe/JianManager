import { expect, test, type Locator, type Page } from '@playwright/test'
import { login } from './helpers'

const FILE_PAYLOAD = {
  name: 'example.jar',
  mimeType: 'application/java-archive',
  buffer: Buffer.from('context-menu-surface'),
}

/** 添加一个本地草稿文件，进入可操作的文件树。 */
async function addDraftFile(page: Page): Promise<void> {
  const input = page.getByText('添加文件', { exact: true }).locator('input[type="file"]')
  await input.setInputFiles(FILE_PAYLOAD)
  await expect(page.getByTestId('fe-file-row')).toContainText('example.jar')
}

/** 在指定视口坐标触发真实 contextmenu 事件。 */
async function openAt(target: Locator, x: number, y: number): Promise<void> {
  await target.evaluate((element, point) => {
    element.dispatchEvent(new MouseEvent('contextmenu', {
      bubbles: true,
      cancelable: true,
      button: 2,
      clientX: point.x,
      clientY: point.y,
    }))
  }, { x, y })
}

/** 断言菜单已 portal 到 body，且被钳制在 8px 视口边距内。 */
async function expectPortalAndViewportClamp(menu: Locator): Promise<void> {
  await expect(menu).toBeVisible()
  expect(await menu.evaluate((element) => element.parentElement === document.body)).toBe(true)
  const geometry = await menu.evaluate((element) => {
    const box = element.getBoundingClientRect()
    const inBounds = box.left >= 7 && box.top >= 7 && box.right <= window.innerWidth - 7 && box.bottom <= window.innerHeight - 7
    return { inBounds, left: box.left, top: box.top, right: box.right, bottom: box.bottom, width: window.innerWidth, height: window.innerHeight }
  })
  expect(geometry.inBounds, JSON.stringify(geometry)).toBe(true)
}

test.describe('ContextMenuSurface 共享包迁移', () => {
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => localStorage.setItem('theme', 'light'))
    await login(page)
    await page.goto('/client-channels/1/publish')
    await expect(page.getByRole('heading', { name: '发布新版本' })).toBeVisible()
  })

  test('文件菜单支持 portal、视口边缘、亮暗主题与重命名动作', async ({ page }) => {
    await addDraftFile(page)
    const row = page.getByTestId('fe-file-row')
    const menu = page.getByTestId('fe-context-menu')
    const viewport = page.viewportSize()
    expect(viewport).not.toBeNull()

    await openAt(row, viewport!.width - 1, viewport!.height - 1)
    await expectPortalAndViewportClamp(menu)
    await expect(page.locator('html')).not.toHaveClass(/dark/)
    const lightBackground = await menu.evaluate((element) => getComputedStyle(element).backgroundColor)

    await menu.getByRole('button', { name: '重命名' }).click()
    const renameInput = page.getByTestId('fe-rename-input')
    await renameInput.fill('renamed.jar')
    await renameInput.press('Enter')
    await expect(row).toContainText('renamed.jar')

    await page.getByRole('button', { name: '切换主题' }).click()
    await page.getByRole('menuitem', { name: '深色' }).click()
    await expect(page.locator('html')).toHaveClass(/dark/)

    await openAt(row, viewport!.width - 1, viewport!.height - 1)
    await expectPortalAndViewportClamp(menu)
    const darkBackground = await menu.evaluate((element) => getComputedStyle(element).backgroundColor)
    expect(darkBackground).not.toBe(lightBackground)
  })

  test('清理范围菜单通过 portal 完成真实目录标记动作', async ({ page }) => {
    await addDraftFile(page)
    await page.getByTestId('fe-new-folder').click()
    await page.getByTestId('fe-rename-input').fill('mods')
    await page.getByTestId('fe-rename-input').press('Enter')

    const explorerDir = page.locator('[data-testid="fe-dir-row"][data-dir-path="mods"]')
    await explorerDir.click({ button: 'right' })
    await page.getByTestId('fe-menu-upload-files').click()
    await page.getByTestId('fe-upload-files-input').setInputFiles({
      ...FILE_PAYLOAD,
      name: 'nested.jar',
    })
    await expect(page.getByTestId('fe-file-row').filter({ hasText: 'nested.jar' })).toBeVisible()

    await page.getByRole('button', { name: /下一步/ }).click()
    await page.getByRole('button', { name: /下一步/ }).click()
    const row = page.locator('[data-testid="clean-scope-dir-row"][data-dir-path="mods"]')
    await expect(row).toHaveAttribute('data-mark', 'none')

    const viewport = page.viewportSize()
    expect(viewport).not.toBeNull()
    await openAt(row, viewport!.width - 1, viewport!.height - 1)
    const menu = page.getByTestId('clean-scope-context-menu')
    await expectPortalAndViewportClamp(menu)

    await menu.getByTestId('clean-scope-mark-clean').click()
    await expect(row).toHaveAttribute('data-mark', 'clean')
    await page.getByRole('button', { name: /下一步/ }).click()
    await expect(page.getByRole('definition').filter({ hasText: /^mods$/ })).toBeVisible()
  })
})
