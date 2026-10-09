import { describe, it, expect } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import ClientUpdaterCoreSelector from './ClientUpdaterCoreSelector'

/**
 * ClientUpdaterCoreSelector 强断言（FR-259）：列出归档版本 + 当前选定高亮 + 切换确认弹窗。
 * mock 返回 v2(selected) + v1 两版本。
 */
describe('ClientUpdaterCoreSelector（mock 假后端）', () => {
  it('列出归档版本并高亮当前选定', async () => {
    loginMockUser()
    renderWithProviders(<ClientUpdaterCoreSelector channelId="skyblock-s1" />)

    // 新版展示 jar 内构建版本，旧资产仍回退显示数字归档版本。
    expect(await screen.findAllByText('0.1.0-SNAPSHOT+abc123def456.dirty')).not.toHaveLength(0)
    expect(screen.getByText('abc123def456.dirty')).toBeInTheDocument()
    expect(screen.getByText('dirty')).toBeInTheDocument()
    expect(screen.getByText('v1')).toBeInTheDocument()
    // 最新归档标为「当前选定」。
    expect(screen.getByText('当前选定')).toBeInTheDocument()
    // v1 有「选定」按钮。
    expect(screen.getByRole('button', { name: '选定' })).toBeInTheDocument()
  })

  it('打开手动上传 updater-core 弹窗', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ClientUpdaterCoreSelector channelId="skyblock-s1" />)

    await screen.findAllByText('0.1.0-SNAPSHOT+abc123def456.dirty')
    await user.click(screen.getByRole('button', { name: '上传 updater-core.jar' }))

    expect(await screen.findByText('用于紧急 hotfix：上传后会归档为 client-updater-core 制品，可选择立即作为当前频道版本。')).toBeInTheDocument()
    expect(screen.getByText('上传后立即选为当前频道版本')).toBeInTheDocument()
  })

  it('点击选定按钮弹出确认弹窗', async () => {
    loginMockUser()
    const user = userEvent.setup()
    renderWithProviders(<ClientUpdaterCoreSelector channelId="skyblock-s1" />)

    // 等版本加载。
    await screen.findAllByText('0.1.0-SNAPSHOT+abc123def456.dirty')
    // 点击 v1 的「选定」按钮。
    const btn = screen.getByRole('button', { name: '选定' })
    await user.click(btn)
    // 确认弹窗出现。
    expect(await screen.findByText('切换 updater-core 版本？')).toBeInTheDocument()
  })
})
