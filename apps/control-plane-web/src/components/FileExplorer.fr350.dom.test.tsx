import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import FileExplorer from './FileExplorer'
import type { ManifestFileLike } from '@/lib/client-publish-wizard'

/** 构造文件资源管理器所需的最小 manifest 文件。 */
function manifestFiles(...paths: string[]): ManifestFileLike[] {
  return paths.map((path) => ({
    path,
    sync: 'strict' as const,
    platform: '' as const,
    size: 100,
  }))
}

/** 当前树中全部目录行的 data-dir-path 列表。 */
function dirPaths(): string[] {
  return screen
    .queryAllByTestId('fe-dir-row')
    .map((el) => el.getAttribute('data-dir-path') ?? '')
}

/** 按 data-dir-path 定位目录行。 */
function dirRow(path: string): HTMLElement {
  const row = screen.getAllByTestId('fe-dir-row').find((el) => el.getAttribute('data-dir-path') === path)
  if (!row) throw new Error(`未找到 data-dir-path=${path} 的目录行`)
  return row
}

describe('FileExplorer 多级目录一次建（FR-350）', () => {
  it('就地重命名输入 a/b/c 拆层级逐级建目录', async () => {
    renderWithProviders(<FileExplorer files={manifestFiles('readme.txt')} />)

    fireEvent.click(screen.getByTestId('fe-new-folder'))
    const input = await screen.findByTestId('fe-rename-input')
    fireEvent.change(input, { target: { value: 'a/b/c' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    await waitFor(() => {
      expect(dirPaths()).toEqual(expect.arrayContaining(['a', 'a/b', 'a/b/c']))
    })
  })

  it('就地重命名含 .. 越界拒绝（占位目录保持原名）', async () => {
    renderWithProviders(<FileExplorer files={manifestFiles('readme.txt')} />)

    fireEvent.click(screen.getByTestId('fe-new-folder'))
    const input = await screen.findByTestId('fe-rename-input')
    fireEvent.change(input, { target: { value: '../escape' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    await waitFor(() => {
      expect(dirPaths()).toContain('新建文件夹')
    })
    expect(dirPaths()).not.toContain('escape')
  })

  it('工具栏「新建多级目录」模态：多行输入批量建并预览层级', async () => {
    const user = userEvent.setup()
    renderWithProviders(<FileExplorer files={manifestFiles('readme.txt')} />)

    fireEvent.click(screen.getByTestId('fe-new-multi-dir'))
    const dialog = await screen.findByRole('dialog', { name: '新建多级目录' })
    fireEvent.change(within(dialog).getByTestId('fe-multi-dir-input'), { target: { value: 'x/y\nz' } })

    // 预览展开整条层级链：x、x/y、z 共 3 层。
    expect(within(dialog).getByText('将创建 3 个目录层级')).toBeInTheDocument()

    await user.click(within(dialog).getByTestId('fe-multi-dir-confirm'))
    await waitFor(() => {
      expect(screen.queryByRole('dialog', { name: '新建多级目录' })).not.toBeInTheDocument()
    })
    expect(dirPaths()).toEqual(expect.arrayContaining(['x', 'x/y', 'z']))
  })

  it('模态非法路径行（含 ..）提示忽略数量且不创建', async () => {
    const user = userEvent.setup()
    renderWithProviders(<FileExplorer files={manifestFiles('readme.txt')} />)

    fireEvent.click(screen.getByTestId('fe-new-multi-dir'))
    const dialog = await screen.findByRole('dialog', { name: '新建多级目录' })
    fireEvent.change(within(dialog).getByTestId('fe-multi-dir-input'), { target: { value: '../evil\nok' } })

    expect(within(dialog).getByText('已忽略 1 条非法路径（含 .. 或为空）')).toBeInTheDocument()

    await user.click(within(dialog).getByTestId('fe-multi-dir-confirm'))
    await waitFor(() => {
      expect(dirPaths()).toContain('ok')
    })
    expect(dirPaths()).not.toContain('evil')
    expect(dirPaths()).not.toContain('..')
  })

  it('模态全部路径非法时禁用创建按钮', async () => {
    renderWithProviders(<FileExplorer files={manifestFiles('readme.txt')} />)

    fireEvent.click(screen.getByTestId('fe-new-multi-dir'))
    const dialog = await screen.findByRole('dialog', { name: '新建多级目录' })
    expect(within(dialog).getByTestId('fe-multi-dir-confirm')).toBeDisabled()

    fireEvent.change(within(dialog).getByTestId('fe-multi-dir-input'), { target: { value: '../evil' } })
    expect(within(dialog).getByTestId('fe-multi-dir-confirm')).toBeDisabled()
  })

  it('右键目录打开模态：新层级拼在该目录路径下', async () => {
    const user = userEvent.setup()
    renderWithProviders(<FileExplorer files={manifestFiles('mods/a.jar')} />)

    fireEvent.contextMenu(dirRow('mods'))
    const menu = await screen.findByTestId('fe-context-menu')
    fireEvent.click(within(menu).getByTestId('fe-menu-multi-dir'))

    const dialog = await screen.findByRole('dialog', { name: '新建多级目录' })
    fireEvent.change(within(dialog).getByTestId('fe-multi-dir-input'), { target: { value: 'a/b' } })
    await user.click(within(dialog).getByTestId('fe-multi-dir-confirm'))

    await waitFor(() => {
      expect(dirPaths()).toEqual(expect.arrayContaining(['mods', 'mods/a', 'mods/a/b']))
    })
  })

  it('已存在层级静默复用不重复建', async () => {
    const user = userEvent.setup()
    renderWithProviders(<FileExplorer files={manifestFiles('mods/a.jar')} />)

    fireEvent.click(screen.getByTestId('fe-new-multi-dir'))
    const dialog = await screen.findByRole('dialog', { name: '新建多级目录' })
    fireEvent.change(within(dialog).getByTestId('fe-multi-dir-input'), { target: { value: 'mods/sub' } })
    await user.click(within(dialog).getByTestId('fe-multi-dir-confirm'))

    await waitFor(() => {
      expect(dirPaths()).toContain('mods/sub')
    })
    expect(dirPaths().filter((p) => p === 'mods')).toHaveLength(1)
  })
})

describe('FileExplorer 右键定点上传（FR-350）', () => {
  it('提供 onUploadToDir 时目录右键菜单出现上传两项', async () => {
    renderWithProviders(
      <FileExplorer files={manifestFiles('mods/a.jar')} onUploadToDir={vi.fn()} />,
    )

    fireEvent.contextMenu(dirRow('mods'))
    const menu = await screen.findByTestId('fe-context-menu')
    expect(within(menu).getByText('上传文件到此')).toBeInTheDocument()
    expect(within(menu).getByText('上传文件夹到此')).toBeInTheDocument()
  })

  it('未提供 onUploadToDir 时不显示上传项', async () => {
    renderWithProviders(<FileExplorer files={manifestFiles('mods/a.jar')} />)

    fireEvent.contextMenu(dirRow('mods'))
    const menu = await screen.findByTestId('fe-context-menu')
    expect(within(menu).queryByText('上传文件到此')).not.toBeInTheDocument()
    expect(within(menu).queryByText('上传文件夹到此')).not.toBeInTheDocument()
  })

  it('文件行右键菜单不出现上传项（仅目录）', async () => {
    renderWithProviders(
      <FileExplorer files={manifestFiles('mods/a.jar')} onUploadToDir={vi.fn()} />,
    )

    const fileEl = screen.getAllByTestId('fe-file-row')[0]
    fireEvent.contextMenu(fileEl)
    const menu = await screen.findByTestId('fe-context-menu')
    expect(within(menu).queryByText('上传文件到此')).not.toBeInTheDocument()
  })

  it('上传文件到此：回调携带所选文件与目标目录路径', async () => {
    const onUploadToDir = vi.fn()
    renderWithProviders(
      <FileExplorer files={manifestFiles('mods/a.jar')} onUploadToDir={onUploadToDir} />,
    )

    fireEvent.contextMenu(dirRow('mods'))
    const menu = await screen.findByTestId('fe-context-menu')
    fireEvent.click(within(menu).getByText('上传文件到此'))

    const input = screen.getByTestId('fe-upload-files-input') as HTMLInputElement
    expect(input.multiple).toBe(true)
    expect(input.hasAttribute('webkitdirectory')).toBe(false)

    const picked = new File(['x'], 'extra.jar')
    fireEvent.change(input, { target: { files: [picked] } })

    expect(onUploadToDir).toHaveBeenCalledOnce()
    expect(onUploadToDir).toHaveBeenCalledWith([picked], 'mods')
  })

  it('上传文件夹到此：input 带 webkitdirectory 且回调携带目录路径', async () => {
    const onUploadToDir = vi.fn()
    renderWithProviders(
      <FileExplorer files={manifestFiles('mods/a.jar', 'config/x.toml')} onUploadToDir={onUploadToDir} />,
    )

    fireEvent.contextMenu(dirRow('config'))
    const menu = await screen.findByTestId('fe-context-menu')
    fireEvent.click(within(menu).getByText('上传文件夹到此'))

    const input = screen.getByTestId('fe-upload-folder-input') as HTMLInputElement
    expect(input.hasAttribute('webkitdirectory')).toBe(true)

    const picked = new File(['y'], 'hud.json')
    fireEvent.change(input, { target: { files: [picked] } })

    expect(onUploadToDir).toHaveBeenCalledOnce()
    expect(onUploadToDir).toHaveBeenCalledWith([picked], 'config')
  })

  it('选择器取消（空 FileList）不触发回调', async () => {
    const onUploadToDir = vi.fn()
    renderWithProviders(
      <FileExplorer files={manifestFiles('mods/a.jar')} onUploadToDir={onUploadToDir} />,
    )

    fireEvent.contextMenu(dirRow('mods'))
    const menu = await screen.findByTestId('fe-context-menu')
    fireEvent.click(within(menu).getByText('上传文件到此'))
    fireEvent.change(screen.getByTestId('fe-upload-files-input'), { target: { files: [] } })

    expect(onUploadToDir).not.toHaveBeenCalled()
  })
})
