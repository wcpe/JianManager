import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import NodeGlobalPackagesSection from './NodeGlobalPackagesSection'

/**
 * FR-307 节点全局包管理 DOM 测：种子列表渲染 + 可更新徽章与升级入口 +
 * 安装（202 受理提示）+ 卸载（DangerConfirm → 列表联动消失）。
 */

beforeEach(() => {
  loginMockUser()
})

describe('NodeGlobalPackagesSection（FR-307）', () => {
  it('渲染种子包列表，含可更新徽章与升级入口', async () => {
    renderWithProviders(<NodeGlobalPackagesSection nodeId={1} active />)

    expect(await screen.findByText('mineflayer')).toBeInTheDocument()
    expect(screen.getByText('typescript')).toBeInTheDocument()
    // mineflayer 可更新：latest 徽章 + 升级按钮；typescript 已最新无升级入口。
    expect(screen.getByText('4.21.0')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: '升级' })).toHaveLength(1)
  })

  it('安装新包：202 受理后表单清空（toast 走 portal 不入断言）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<NodeGlobalPackagesSection nodeId={1} active />)
    await screen.findByText('mineflayer')

    const nameInput = screen.getByRole('textbox', { name: '包名' })
    await user.type(nameInput, 'prismarine-viewer')
    await user.click(screen.getByRole('button', { name: '安装' }))
    // onSuccess 清空表单 = 202 受理成功的确定性副作用。
    await waitFor(() => expect(nameInput).toHaveValue(''))
  })

  it('卸载：确认后列表联动消失', async () => {
    const user = userEvent.setup()
    renderWithProviders(<NodeGlobalPackagesSection nodeId={1} active />)
    await screen.findByText('typescript')

    // typescript 行的卸载 → DangerConfirm → 确认。
    const removeBtns = screen.getAllByRole('button', { name: '卸载' })
    await user.click(removeBtns[removeBtns.length - 1])
    expect(await screen.findByText('卸载全局包?')).toBeInTheDocument()
    await user.click(screen.getAllByRole('button', { name: '卸载' }).pop()!)

    await waitFor(() => expect(screen.queryByText('typescript')).not.toBeInTheDocument())
  })
})
