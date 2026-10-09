import { describe, it, expect, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithI18n } from '../../../test/i18n'
import NodeGlobalPackagesSection, { type GlobalPackagesView } from '@/components/views/nodes/NodeGlobalPackagesSection'

/**
 * FR-307 节点全局包管理 · 受控视图测（ADR-097 b 范式）。
 *
 * 与迁移前的两处差异**是有意的取舍，勿当缺陷「修掉」**：
 * 1. 数据经 props 注入，不再经 mock 后端取数，因此没有「卸载成功后列表联动消失」这一
 *    断言——列表归外壳所有，视图无权失效重取。这里改断言「onRemove 收到正确的包名」，
 *    联动效果由外壳的集成测覆盖。
 * 2. toast 已移出组件，故失败分支的提示不在本测范围内；成功分支仍断在
 *    「安装受理后表单清空」这一确定性副作用上（与迁移前保持一致）。
 */

const view: GlobalPackagesView = {
  pm: 'pnpm',
  packages: [
    { name: 'mineflayer', version: '4.20.0', latest: '4.21.0' },
    { name: 'typescript', version: '5.9.3' },
  ],
}

function renderSection(props: Partial<Parameters<typeof NodeGlobalPackagesSection>[0]> = {}) {
  // 默认在前、props 在后：返回的回调与真正传给组件的**是同一个引用**，
  // 测试自带 mock 时断言才落在实处（若返回默认 mock，覆盖场景会假失败）。
  const handlers = {
    onInstall: vi.fn().mockResolvedValue(true),
    onRemove: vi.fn().mockResolvedValue(true),
    onRefresh: vi.fn(),
    ...props,
  }
  // 展开 render 的返回值，使调用方可直接取 container 等。
  const result = renderWithI18n(<NodeGlobalPackagesSection data={view} {...handlers} />)
  return { ...handlers, ...result }
}

describe('NodeGlobalPackagesSection（FR-307 · ADR-097 b 范式）', () => {
  it('加载态渲染骨架占位而非空态', () => {
    const { container } = renderSection({ isLoading: true, data: undefined })
    expect(container.querySelectorAll('[data-slot="skeleton"]').length).toBe(3)
    expect(screen.queryByText('mineflayer')).not.toBeInTheDocument()
  })

  it('渲染注入的包列表，含可更新徽章与升级入口', () => {
    renderSection()

    expect(screen.getByText('mineflayer')).toBeInTheDocument()
    expect(screen.getByText('typescript')).toBeInTheDocument()
    expect(screen.getByText('pnpm')).toBeInTheDocument()
    // mineflayer 可更新：latest 徽章 + 升级按钮；typescript 已最新，无升级入口。
    expect(screen.getByText('4.21.0')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: '升级' })).toHaveLength(1)
  })

  it('列表读取失败时展示外壳注入的错误消息', () => {
    renderSection({ errorMessage: '节点离线' })
    expect(screen.getByText('节点离线')).toBeInTheDocument()
  })

  it('安装：回调上报包名，受理成功后表单清空', async () => {
    const user = userEvent.setup()
    const { onInstall } = renderSection()

    const nameInput = screen.getByRole('textbox', { name: '包名' })
    await user.type(nameInput, 'prismarine-viewer')
    await user.click(screen.getByRole('button', { name: '安装' }))

    expect(onInstall).toHaveBeenCalledWith('prismarine-viewer', '')
    await waitFor(() => expect(nameInput).toHaveValue(''))
  })

  it('安装失败时保留表单输入，便于用户修正后重试', async () => {
    const user = userEvent.setup()
    const { onInstall } = renderSection({ onInstall: vi.fn().mockResolvedValue(false) })

    const nameInput = screen.getByRole('textbox', { name: '包名' })
    await user.type(nameInput, 'bad-pkg')
    await user.click(screen.getByRole('button', { name: '安装' }))

    await waitFor(() => expect(onInstall).toHaveBeenCalled())
    expect(nameInput).toHaveValue('bad-pkg')
  })

  it('卸载：DangerConfirm 确认后回调上报包名', async () => {
    const user = userEvent.setup()
    const { onRemove } = renderSection()

    // typescript 行的卸载 → DangerConfirm → 确认。
    const removeBtns = screen.getAllByRole('button', { name: '卸载' })
    await user.click(removeBtns[removeBtns.length - 1])
    expect(await screen.findByText('卸载全局包?')).toBeInTheDocument()
    await user.click(screen.getAllByRole('button', { name: '卸载' }).pop()!)

    await waitFor(() => expect(onRemove).toHaveBeenCalledWith('typescript'))
  })

  it('刷新按钮回调外壳', async () => {
    const user = userEvent.setup()
    const { onRefresh } = renderSection()

    await user.click(screen.getByRole('button', { name: '刷新' }))
    expect(onRefresh).toHaveBeenCalledTimes(1)
  })
})
