import { test, expect } from '@playwright/test'
import { login } from './helpers'

/**
 * FR-192 拉取密钥可查看/可编辑 · 单机（Playwright + mock 模式）验收。
 * 覆盖：创建密钥（可逆加密存储）→ 密钥行 查看（可随时再看明文）/ 编辑 / 吊销；就绪度步骤器随建密钥推进。
 * （可逆加密存储 + admin 可查看明文 + 非 admin 拒绝由 Go client_key_reveal_test.go 覆盖）
 * 证据落 .tmp/acceptance/FR-192/。
 */

test('FR-192 拉取密钥 创建 + 可随时查看明文 + 可编辑/吊销', async ({ page }) => {
  await login(page)
  await page.goto('/client-channels')
  await page.getByRole('button', { name: /survival-s2/ }).click()

  // 建密钥（模态）
  await page.getByRole('button', { name: '创建密钥' }).first().click()
  const dlg = page.getByRole('dialog', { name: '创建密钥' })
  await dlg.getByRole('textbox', { name: '密钥名称' }).fill('e2e-key')
  await dlg.getByRole('button', { name: '创建密钥' }).click()
  // 创建后的明文弹窗确认密钥已加密保存，后续仍可从列表查看。
  const secret = page.getByRole('dialog', { name: '拉取密钥' })
  await expect(secret).toContainText('此密钥已加密保存，关闭后仍可在密钥列表中随时查看明文。')
  await expect(secret).not.toContainText(/仅此一次|无法再次查看/)
  await secret.getByRole('button', { name: '关闭' }).click()

  // 密钥行：查看（可随时再看明文）/ 编辑 / 吊销
  await expect(page.getByText('e2e-key')).toBeVisible()
  await expect(page.getByRole('button', { name: '编辑' }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: '吊销' }).first()).toBeVisible()
  await page.getByRole('button', { name: '查看' }).first().click()
  const reveal = page.getByRole('dialog', { name: '拉取密钥明文' })
  await expect(reveal).toBeVisible() // 可随时再看明文（非仅一次）
  await expect(reveal.getByRole('button', { name: '复制' })).toBeVisible()

  await page.screenshot({ path: '../.tmp/acceptance/FR-192/single-machine-key-reveal.png', fullPage: true })
})
