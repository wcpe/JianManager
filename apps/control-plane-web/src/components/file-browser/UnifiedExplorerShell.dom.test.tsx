import { describe, it, expect, vi } from 'vitest'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import UnifiedExplorerShell from './UnifiedExplorerShell'
import {
  instanceFilesCapability,
  storageBrowseCapability,
  customExplorerCapability,
} from './capability'
import type { FileBrowserSource } from '@/lib/file-browser-types'

// 组件已回迁应用侧（原 ADR-097 迁包已撤销）：包内 UnifiedExplorerShell 从包内 import 标签宿主，
// 因此 mock 必须打在包路径上才拦得到。
vi.mock('@/components/views/explorer/ExplorerTabHost', () => ({
  default: ({ instanceId }: { instanceId: number }) => (
    <div data-testid="mock-tab-host">tab-host-{instanceId}</div>
  ),
}))

const stubSource: FileBrowserSource = {
  list: async () => [{ path: 'a', name: 'a', isDir: false, size: 1 }],
  readContent: async () => ({ kind: 'text', content: 'x' }),
}

describe('UnifiedExplorerShell（FR-378）', () => {
  it('instance-files 渲染 TabHost', () => {
    renderWithProviders(
      <UnifiedExplorerShell capability={instanceFilesCapability()} instanceId={7} />,
    )
    expect(screen.getByTestId('unified-explorer-shell')).toHaveAttribute('data-mode', 'instance-files')
    expect(screen.getByTestId('mock-tab-host')).toHaveTextContent('tab-host-7')
  })

  it('browser 渲染 FileBrowser', async () => {
    renderWithProviders(
      <UnifiedExplorerShell capability={storageBrowseCapability()} source={stubSource} />,
    )
    expect(screen.getByTestId('unified-explorer-shell')).toHaveAttribute('data-cap', 'storage-browse')
    expect(await screen.findByText('a')).toBeInTheDocument()
  })

  it('custom 渲染 children', () => {
    renderWithProviders(
      <UnifiedExplorerShell capability={customExplorerCapability('pub')}>
        <div data-testid="custom-slot">biz</div>
      </UnifiedExplorerShell>,
    )
    expect(screen.getByTestId('custom-slot')).toHaveTextContent('biz')
  })
})
