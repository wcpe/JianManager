import { describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import InstanceResourceCard from './InstanceResourceCard'

vi.mock('@/components/config-explorer/ConfigExplorer', () => ({
  default: ({ instanceId }: { instanceId: number }) => (
    <div data-testid="config-explorer">配置管理：{instanceId}</div>
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
  it('保留配置管理与浏览，并通过明确的文件标签挂载通用 ResourceExplorer', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstanceResourceCard instanceId={42} />)

    expect(screen.getByRole('tab', { name: '管理' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByTestId('config-explorer')).toHaveTextContent('配置管理：42')
    expect(screen.getByRole('tab', { name: '文件' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '浏览' })).toBeInTheDocument()

    await user.click(screen.getByRole('tab', { name: '文件' }))
    expect(screen.getByTestId('resource-explorer')).toHaveTextContent('文件管理：42')

    await user.click(screen.getByRole('tab', { name: '浏览' }))
    expect(screen.getByTestId('file-browser')).toHaveTextContent('只读浏览')
  })
})
