import { describe, it, expect, beforeAll, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import '@/i18n'
import FileBrowser from './FileBrowser'
import type { FileBrowserSource, FileEntry, PreviewContent } from './types'

/**
 * FileBrowser 组件强断言（FR-213）。
 *
 * 关键：本测试**不接任何后端 / MSW**——只注入一个内存 {@link FileBrowserSource} 假数据源，
 * 借此同时证明「组件与具体后端解耦」（契约即 props）。覆盖：扁平树渲染、点文件→文本预览、
 * 二进制/超大降级 + 下载兜底、readOnly 隐藏 actions / 可操作态显示 actions。
 *
 * jsdom 缺 Range.getClientRects / getBoundingClientRect，CodeMirror 6 坐标测量会抛异常，
 * 本文件补最小垫片（返回零矩形，断言不依赖布局几何）。
 */
beforeAll(() => {
  const zeroRects = () =>
    ({ length: 0, item: () => null, [Symbol.iterator]: function* () {} }) as unknown as DOMRectList
  Range.prototype.getClientRects = zeroRects
  Range.prototype.getBoundingClientRect = () =>
    ({ x: 0, y: 0, width: 0, height: 0, top: 0, left: 0, right: 0, bottom: 0, toJSON: () => ({}) }) as DOMRect
})

/** 扁平全量假数据源：一组条目 + 按 path 决定预览内容。 */
function fakeFlatSource(
  entries: FileEntry[],
  contentByPath: Record<string, PreviewContent>,
  download?: (e: FileEntry) => void,
): FileBrowserSource {
  return {
    flat: true,
    list: async () => entries,
    readContent: async (e) => contentByPath[e.path] ?? { kind: 'error', message: 'no content' },
    download,
  }
}

const SAMPLE: FileEntry[] = [
  { path: 'server.properties', name: 'server.properties', isDir: false, size: 50 },
  { path: 'world', name: 'world', isDir: true },
  { path: 'world/level.dat', name: 'level.dat', isDir: false, size: 2048 },
  { path: 'huge.log', name: 'huge.log', isDir: false, size: 5_000_000 },
]

const CONTENT: Record<string, PreviewContent> = {
  'server.properties': { kind: 'text', content: 'motd=Hello\nmax-players=20' },
  'world/level.dat': { kind: 'binary' },
  'huge.log': { kind: 'too-large', size: 5_000_000 },
}

describe('FileBrowser 渲染与预览（注入假数据源，FR-213）', () => {
  it('扁平数据源渲染目录树（目录与文件名都出现）', async () => {
    render(<FileBrowser source={fakeFlatSource(SAMPLE, CONTENT)} />)
    await waitFor(() => expect(screen.getByText('server.properties')).toBeInTheDocument())
    expect(screen.getByText('world')).toBeInTheDocument()
    // 嵌套文件（world 目录默认展开）。
    expect(screen.getByText('level.dat')).toBeInTheDocument()
    // 未选中文件时显示引导占位。
    expect(screen.getByText('在左侧选择文件以预览内容')).toBeInTheDocument()
  })

  it('点文本文件 → 右栏高亮预览出内容', async () => {
    const user = userEvent.setup()
    const { container } = render(<FileBrowser source={fakeFlatSource(SAMPLE, CONTENT)} />)
    await user.click(await screen.findByText('server.properties'))
    await waitFor(() => {
      expect(container.querySelector('.cm-content')).not.toBeNull()
      expect((container.querySelector('.cm-content') as HTMLElement).textContent).toContain('motd=Hello')
    })
  })

  it('二进制文件 → 降级占位 + 下载按钮', async () => {
    const onDl = vi.fn()
    const user = userEvent.setup()
    render(<FileBrowser source={fakeFlatSource(SAMPLE, CONTENT, onDl)} />)
    await user.click(await screen.findByText('level.dat'))
    // 降级文案出现，且无编辑器内容容器。
    await waitFor(() => expect(screen.getByText(/二进制文件/)).toBeInTheDocument())
    // 下载按钮可点（降级态下载兜底）。
    const dlBtn = screen.getByRole('button', { name: /下载/ })
    await user.click(dlBtn)
    expect(onDl).toHaveBeenCalledWith(expect.objectContaining({ path: 'world/level.dat' }))
  })

  it('超大文件 → 降级占位（带大小）', async () => {
    const user = userEvent.setup()
    render(<FileBrowser source={fakeFlatSource(SAMPLE, CONTENT, vi.fn())} />)
    await user.click(await screen.findByText('huge.log'))
    await waitFor(() => expect(screen.getByText(/文件过大/)).toBeInTheDocument())
  })

  it('readOnly（默认）不渲染行操作菜单；可操作态渲染', async () => {
    const action = { key: 'del', label: '删除', onAction: vi.fn(), visible: (e: FileEntry) => !e.isDir }
    const src = fakeFlatSource(SAMPLE, CONTENT)

    // 只读：行操作按钮不出现。
    const { unmount } = render(<FileBrowser source={src} actions={[action]} />)
    await screen.findByText('server.properties')
    expect(screen.queryByRole('button', { name: '操作' })).toBeNull()
    unmount()

    // 可操作：每个文件行出现「操作」菜单触发器。
    render(<FileBrowser source={src} readOnly={false} actions={[action]} />)
    await screen.findByText('server.properties')
    const triggers = screen.getAllByRole('button', { name: '操作' })
    expect(triggers.length).toBeGreaterThan(0)
  })

  it('选中文件触发 onSelect 回调', async () => {
    const onSelect = vi.fn()
    const user = userEvent.setup()
    render(<FileBrowser source={fakeFlatSource(SAMPLE, CONTENT)} onSelect={onSelect} />)
    await user.click(await screen.findByText('server.properties'))
    await waitFor(() =>
      expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ path: 'server.properties' })),
    )
  })
})

/** 懒加载分层假数据源：按目录 path 返回该层（验证另一种形态）。 */
function fakeLazySource(byDir: Record<string, FileEntry[]>): FileBrowserSource {
  return {
    flat: false,
    list: async (dir) => byDir[dir] ?? [],
    readContent: async () => ({ kind: 'text', content: 'x' }),
  }
}

describe('FileBrowser 懒加载分层（FR-213）', () => {
  it('根层渲染 + 展开目录拉子层', async () => {
    const user = userEvent.setup()
    const src = fakeLazySource({
      '': [
        { path: 'plugins', name: 'plugins', isDir: true },
        { path: 'eula.txt', name: 'eula.txt', isDir: false, size: 10 },
      ],
      plugins: [{ path: 'plugins/Essentials.jar', name: 'Essentials.jar', isDir: false, size: 999 }],
    })
    render(<FileBrowser source={src} />)
    // 根层。
    await waitFor(() => expect(screen.getByText('plugins')).toBeInTheDocument())
    expect(screen.getByText('eula.txt')).toBeInTheDocument()
    // 子层未加载前不出现。
    expect(screen.queryByText('Essentials.jar')).toBeNull()
    // 展开 plugins → 拉子层。
    await user.click(screen.getByText('plugins'))
    await waitFor(() => expect(screen.getByText('Essentials.jar')).toBeInTheDocument())
  })
})
