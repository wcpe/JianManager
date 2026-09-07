import { describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import InstanceResourceCard from './InstanceResourceCard'

// 替身须履行真组件的契约：ConfigExplorer 会把 toolbarLeading 透传进资源管理器工具栏（FR-422），
// 替身若丢掉这个插槽，分段控件就整个消失，测的就不是真实结构了。
vi.mock('@/components/config-explorer/ConfigExplorer', () => ({
  default: ({ instanceId, toolbarLeading }: { instanceId: number; toolbarLeading?: ReactNode }) => (
    <div data-testid="config-explorer">
      {toolbarLeading}
      配置管理：{instanceId}
    </div>
  ),
}))

vi.mock('@/components/explorer/ResourceExplorer', () => ({
  default: ({ instanceId }: { instanceId: number }) => (
    <div data-testid="resource-explorer">文件管理：{instanceId}</div>
  ),
}))

vi.mock('@/components/file-browser/FileBrowser', () => ({
  default: () => <div data-testid="file-browser">只读浏览</div>,
}))

vi.mock('@/components/file-browser/sources/instanceSource', () => ({
  instanceFileSource: () => ({ id: 'instance-files' }),
}))

describe('InstanceResourceCard 文件版本生产入口（FR-204）', () => {
  /**
   * FR-422：分段控件从「自成一条横栏的 Radix Tabs」改为寄居在各视图内层管理器横栏里的
   * `aria-pressed` 按钮组（tablist 嵌 tabpanel 是无效 ARIA 结构，故不再用 tab 角色）。
   * 断言强度不变：仍逐段确认「哪一段处于选中态」与「切过去挂载了哪个组件」。
   */
  it('保留配置管理与浏览，并通过明确的文件分段挂载通用 ResourceExplorer', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstanceResourceCard instanceId={42} />)

    const group = screen.getByRole('group', { name: '资源视图' })
    expect(within(group).getByRole('button', { name: '管理' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByTestId('config-explorer')).toHaveTextContent('配置管理：42')
    expect(within(group).getByRole('button', { name: '文件' })).toHaveAttribute('aria-pressed', 'false')
    expect(within(group).getByRole('button', { name: '浏览' })).toHaveAttribute('aria-pressed', 'false')

    await user.click(within(group).getByRole('button', { name: '文件' }))
    expect(screen.getByTestId('resource-explorer')).toHaveTextContent('文件管理：42')
    expect(
      within(screen.getByRole('group', { name: '资源视图' })).getByRole('button', { name: '文件' }),
    ).toHaveAttribute('aria-pressed', 'true')

    await user.click(within(screen.getByRole('group', { name: '资源视图' })).getByRole('button', { name: '浏览' }))
    expect(screen.getByTestId('file-browser')).toHaveTextContent('只读浏览')
  })

  it('FR-422：分段控件寄居在内层管理器横栏，不再自占一条横栏', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstanceResourceCard instanceId={42} />)

    // 管理视图：分段随 toolbarLeading 进 ConfigExplorer（此处被 mock，只能验传递发生）。
    // 文件视图：分段随 leading 进 ExplorerTabHost 的标签条（真实组件，可验同容器）。
    await user.click(within(screen.getByRole('group', { name: '资源视图' })).getByRole('button', { name: '文件' }))
    const host = screen.getByTestId('explorer-tab-host')
    expect(within(host).getByRole('group', { name: '资源视图' })).toBeInTheDocument()
    // 分段与「新标签」同处标签条这一行，而非在其上另起一条。
    const strip = within(host).getByRole('button', { name: '新标签' }).parentElement as HTMLElement
    expect(within(strip).getByRole('group', { name: '资源视图' })).toBeInTheDocument()
  })
})
