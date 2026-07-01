import { describe, it, expect, vi } from 'vitest'
import { screen, fireEvent } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import ClientFileTree from '@/components/ClientFileTree'
import type { ManifestFileLike } from '@/lib/client-publish-wizard'

/**
 * ClientFileTree 拖拽编排 DOM 测试（FR-254）。
 * 验证：拖拽文件节点到目录 → onPathChange 改路径；拖拽目录节点 → 批量改子树路径；
 * 拖到根 → 剥离目录前缀；拖到自身 → no-op。
 */

/** 构造测试用文件列表（与 ManifestFile 兼容的最小形态）。 */
function files(...paths: string[]): ManifestFileLike[] {
  return paths.map((p) => ({
    path: p,
    sync: 'strict' as const,
    platform: '' as const,
    size: 100,
  }))
}

/** jsdom 的 DataTransfer 不完整，构造最小 mock 供 DragEvent 使用。 */
function mockDataTransfer() {
  return { effectAllowed: '', dropEffect: '', setData: () => {}, getData: () => '' }
}

/** 按 data-file-index 找文件行。 */
function fileRowByIndex(index: number): HTMLElement {
  const rows = screen.getAllByTestId('file-row')
  const row = rows.find((el) => el.getAttribute('data-file-index') === String(index))
  if (!row) throw new Error(`未找到 data-file-index=${index} 的文件行`)
  return row
}

/** 按 data-dir-path 找目录行。 */
function dirRowByPath(path: string): HTMLElement {
  const rows = screen.getAllByTestId('dir-row')
  const row = rows.find((el) => el.getAttribute('data-dir-path') === path)
  if (!row) throw new Error(`未找到 data-dir-path=${path} 的目录行`)
  return row
}

describe('ClientFileTree 拖拽编排（FR-254）', () => {
  it('拖拽文件到目录 → onPathChange 改为目录+文件名', () => {
    const onPathChange = vi.fn()
    renderWithProviders(
      <ClientFileTree
        files={files('mods/a.jar', 'config/x.txt')}
        onPathChange={onPathChange}
      />,
    )

    // dragStart 文件行 mods/a.jar（index=0）
    fireEvent.dragStart(fileRowByIndex(0), { dataTransfer: mockDataTransfer() })

    // drop 到目录 config
    const configDir = dirRowByPath('config')
    fireEvent.dragOver(configDir, { dataTransfer: mockDataTransfer() })
    fireEvent.drop(configDir, { dataTransfer: mockDataTransfer() })

    expect(onPathChange).toHaveBeenCalledWith(0, 'config/a.jar')
  })

  it('拖拽文件到根 → onPathChange 剥离目录前缀仅留文件名', () => {
    const onPathChange = vi.fn()
    renderWithProviders(
      <ClientFileTree
        files={files('mods/a.jar', 'config/x.txt')}
        onPathChange={onPathChange}
      />,
    )

    fireEvent.dragStart(fileRowByIndex(0), { dataTransfer: mockDataTransfer() })

    // drop 到根容器（非目录区域）
    const root = screen.getByTestId('tree-root')
    fireEvent.dragOver(root, { dataTransfer: mockDataTransfer() })
    fireEvent.drop(root, { dataTransfer: mockDataTransfer() })

    expect(onPathChange).toHaveBeenCalledWith(0, 'a.jar')
  })

  it('拖拽目录到另一目录 → 批量改子树全部文件路径', () => {
    const onPathChange = vi.fn()
    renderWithProviders(
      <ClientFileTree
        files={files('config/foo/a.txt', 'config/foo/sub/b.txt', 'mods/c.jar')}
        onPathChange={onPathChange}
      />,
    )

    fireEvent.dragStart(dirRowByPath('config/foo'), { dataTransfer: mockDataTransfer() })

    // drop 到 mods 目录
    const modsDir = dirRowByPath('mods')
    fireEvent.dragOver(modsDir, { dataTransfer: mockDataTransfer() })
    fireEvent.drop(modsDir, { dataTransfer: mockDataTransfer() })

    // config/foo/a.txt → mods/foo/a.txt
    // config/foo/sub/b.txt → mods/foo/sub/b.txt
    expect(onPathChange).toHaveBeenCalledWith(0, 'mods/foo/a.txt')
    expect(onPathChange).toHaveBeenCalledWith(1, 'mods/foo/sub/b.txt')
    // mods/c.jar 不受影响
    expect(onPathChange).not.toHaveBeenCalledWith(2, expect.anything())
  })

  it('拖拽目录到自身 → no-op（不调用 onPathChange）', () => {
    const onPathChange = vi.fn()
    renderWithProviders(
      <ClientFileTree
        files={files('config/foo/a.txt', 'mods/b.jar')}
        onPathChange={onPathChange}
      />,
    )

    const fooDir = dirRowByPath('config/foo')
    fireEvent.dragStart(fooDir, { dataTransfer: mockDataTransfer() })
    fireEvent.dragOver(fooDir, { dataTransfer: mockDataTransfer() })
    fireEvent.drop(fooDir, { dataTransfer: mockDataTransfer() })

    expect(onPathChange).not.toHaveBeenCalled()
  })

  it('只读模式不渲染拖拽手柄、不触发拖拽', () => {
    const onPathChange = vi.fn()
    renderWithProviders(
      <ClientFileTree
        files={files('mods/a.jar')}
        readonly
        onPathChange={onPathChange}
      />,
    )

    // 只读模式无编排态 testid
    expect(screen.queryByTestId('file-row')).not.toBeInTheDocument()
    expect(screen.queryByTestId('dir-row')).not.toBeInTheDocument()
  })
})
