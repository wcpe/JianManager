import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import FileExplorer from './FileExplorer'
import type { LocalUnit, ManifestFileLike } from '@/lib/client-publish-wizard'

/** 构造文件资源管理器所需的最小 manifest 文件。 */
function manifestFiles(...paths: string[]): ManifestFileLike[] {
  return paths.map((path) => ({
    path,
    sync: 'strict' as const,
    platform: '' as const,
    size: 100,
  }))
}

/** 按源数组下标定位文件行。 */
function fileRow(index: number): HTMLElement {
  const row = screen.getAllByTestId('fe-file-row').find((item) => item.getAttribute('data-file-index') === String(index))
  if (!row) throw new Error(`未找到 data-file-index=${index} 的文件行`)
  return row
}

/** 构造外部拖入文件的数据传输对象。 */
function dropData(file: File) {
  return { files: [file], types: ['Files'] }
}

describe('FileExplorer 文件选择与删除（FR-261）', () => {
  it('Ctrl 点击切换多选文件', () => {
    renderWithProviders(<FileExplorer files={manifestFiles('a.jar', 'b.jar', 'c.jar')} />)

    fireEvent.click(fileRow(0))
    fireEvent.click(fileRow(2), { ctrlKey: true })

    expect(fileRow(0)).toHaveAttribute('data-selected', 'true')
    expect(fileRow(1)).toHaveAttribute('data-selected', 'false')
    expect(fileRow(2)).toHaveAttribute('data-selected', 'true')

    fireEvent.click(fileRow(0), { ctrlKey: true })
    expect(fileRow(0)).toHaveAttribute('data-selected', 'false')
    expect(fileRow(2)).toHaveAttribute('data-selected', 'true')
  })

  it('Shift 点击按可见顺序连续选择锚点到目标范围', () => {
    renderWithProviders(<FileExplorer files={manifestFiles('a.jar', 'b.jar', 'c.jar', 'd.jar')} />)

    fireEvent.click(fileRow(1))
    fireEvent.click(fileRow(3), { shiftKey: true })

    expect(fileRow(0)).toHaveAttribute('data-selected', 'false')
    expect(fileRow(1)).toHaveAttribute('data-selected', 'true')
    expect(fileRow(2)).toHaveAttribute('data-selected', 'true')
    expect(fileRow(3)).toHaveAttribute('data-selected', 'true')
  })

  it('Delete 打开 DangerConfirm，确认后调用批量删除回调', async () => {
    const user = userEvent.setup()
    const onRemove = vi.fn()
    const onRemoveMultiple = vi.fn()
    renderWithProviders(
      <FileExplorer
        files={manifestFiles('a.jar', 'b.jar', 'c.jar')}
        onRemove={onRemove}
        onRemoveMultiple={onRemoveMultiple}
      />,
    )

    fireEvent.click(fileRow(0))
    fireEvent.click(fileRow(2), { ctrlKey: true })
    fireEvent.keyDown(document, { key: 'Delete' })

    const dialog = await screen.findByRole('dialog', { name: '确认删除？' })
    expect(within(dialog).getByText('将删除选中的 2 个文件，此操作不可撤销。')).toBeInTheDocument()
    expect(onRemoveMultiple).not.toHaveBeenCalled()

    await user.click(within(dialog).getByRole('button', { name: '删除' }))
    expect(onRemoveMultiple).toHaveBeenCalledOnce()
    expect(onRemoveMultiple).toHaveBeenCalledWith([0, 2])
    expect(onRemove).not.toHaveBeenCalled()
  })
})

describe('FileExplorer 同路径冲突决策（FR-261）', () => {
  it.each([
    { label: '全部忽略', testId: 'fe-conflict-skip-all', expectedPath: null, removesExisting: false },
    { label: '全部覆盖', testId: 'fe-conflict-replace-all', expectedPath: 'mods/a.jar', removesExisting: true },
    { label: '全部保留两者', testId: 'fe-conflict-keep-both-all', expectedPath: 'mods/a (1).jar', removesExisting: false },
  ])('$label 应用对应批量决策', async ({ testId, expectedPath, removesExisting }) => {
    const user = userEvent.setup()
    const incomingFile = new File(['new'], 'a.jar')
    const incoming: LocalUnit = { file: incomingFile, path: 'mods/a.jar' }
    const resolveDrop = vi.fn().mockResolvedValue([incoming])
    const onAddFiles = vi.fn()
    const onRemoveMultiple = vi.fn()
    renderWithProviders(
      <FileExplorer
        files={manifestFiles('mods/a.jar')}
        resolveDrop={resolveDrop}
        onAddFiles={onAddFiles}
        onRemoveMultiple={onRemoveMultiple}
      />,
    )

    fireEvent.drop(screen.getByTestId('fe-tree-root'), { dataTransfer: dropData(incomingFile) })

    const dialog = await screen.findByRole('dialog', { name: '同名文件冲突' })
    expect(within(dialog).getByText('mods/a.jar')).toBeInTheDocument()
    await user.click(within(dialog).getByTestId(testId))

    await waitFor(() => expect(screen.queryByRole('dialog', { name: '同名文件冲突' })).not.toBeInTheDocument())
    expect(resolveDrop).toHaveBeenCalledWith([incomingFile], '')

    if (expectedPath === null) {
      expect(onAddFiles).not.toHaveBeenCalled()
    } else {
      expect(onAddFiles).toHaveBeenCalledWith([{ file: incomingFile, path: expectedPath }])
    }

    if (removesExisting) {
      expect(onRemoveMultiple).toHaveBeenCalledWith([0])
    } else {
      expect(onRemoveMultiple).not.toHaveBeenCalled()
    }
  })
})
