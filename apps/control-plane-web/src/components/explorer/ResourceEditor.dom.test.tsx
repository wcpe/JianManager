import { describe, it, expect, beforeAll } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { BrowserRouter } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/i18n'
import { mockInject } from '@/mocks/inject'
import { loginMockUser } from '@/test/auth'
import ResourceExplorer from './ResourceExplorer'

/**
 * jsdom 缺 Range.getClientRects / getBoundingClientRect，CodeMirror 6 的坐标测量会抛
 * 「textRange(...).getClientRects is not a function」（异步、可致 Vitest 误判）。在本测试文件内
 * 补最小垫片（不改 setup.ts），让编辑器在 jsdom 下不抛坐标异常。返回零矩形即可——本文件断言不依赖布局几何。
 */
beforeAll(() => {
  if (!('getClientRects' in Range.prototype)) {
    // 某些 jsdom 版本无该方法。
    ;(Range.prototype as unknown as { getClientRects: () => DOMRectList }).getClientRects = () =>
      ({ length: 0, item: () => null, [Symbol.iterator]: function* () {} }) as unknown as DOMRectList
  } else {
    Range.prototype.getClientRects = () =>
      ({ length: 0, item: () => null, [Symbol.iterator]: function* () {} }) as unknown as DOMRectList
  }
  Range.prototype.getBoundingClientRect = () =>
    ({ x: 0, y: 0, width: 0, height: 0, top: 0, left: 0, right: 0, bottom: 0, toJSON: () => ({}) }) as DOMRect
})

/**
 * 文件编辑器读/改/存联动强断言（FR-204 文件归档域 / FR-070 编辑器）。
 *
 * 编辑器（CodeEditor，CodeMirror 6）只经 ResourceExplorer 在右栏挂载，故驱动整个资源管理器：
 * 双击文件 → 读出种子内容（GET /files/read）→ 在编辑器内输入 → 保存按钮由禁用转可用 →
 * 点保存（POST /files/write）→ 成功后回到「已保存」态（按钮再次禁用、脏点消失）→
 * 关闭重开读回新内容（证明写已落到假后端、读端点返回新值）。
 *
 * 断言策略（按任务降级要求）：CodeMirror 在 jsdom 的逐字符文本查询不稳，故内容断言走
 * 容器 `.cm-content` 的 textContent「包含」子串（不逐字符 / 不依赖换行；经探针验证 jsdom 下
 * CodeMirror 会把文档渲染进 .cm-content，行间无换行符），保存态走「保存按钮 disabled 切换」这一
 * 由 saved===draft 派生的确定性 DOM 信号。toast 走 sonner 但 harness 未挂 <Toaster>，故不断言 toast。
 *
 * 用 instanceId=1（files 种子所在实例）。renderWithProviders 不便取 container，这里内联等价 Provider 链。
 */

/** 与 renderWithProviders 等价的 Provider 链，但返回 container 以便查询 CodeMirror 宿主 DOM。 */
function renderExplorer(instanceId = 1) {
  window.history.pushState({}, '', '/')
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <BrowserRouter>
      <QueryClientProvider client={queryClient}>
        <ResourceExplorer instanceId={instanceId} />
      </QueryClientProvider>
    </BrowserRouter>,
  )
}

/** 双击右侧列表里某文件名把它在编辑器打开（列表项是可双击的文件行 span）。 */
async function openFileInEditor(user: ReturnType<typeof userEvent.setup>, name: string) {
  const rows = await screen.findAllByText(name)
  for (const row of rows) await user.dblClick(row)
}

/** 取编辑器内容容器（CodeMirror 的 .cm-content）。 */
function editorContent(container: HTMLElement): HTMLElement {
  const el = container.querySelector('.cm-content')
  if (!el) throw new Error('CodeMirror .cm-content 未挂载')
  return el as HTMLElement
}

/** 取右栏编辑器头部（含 文件名 / 历史 / 保存 / 关闭 按钮的那一条）。 */
function editorHeaderSaveButton(): HTMLButtonElement {
  // 编辑器头部的「保存」按钮文案为 files.save（zh = 保存）。Toolbar 没有「保存」按钮，故唯一。
  return screen.getByRole('button', { name: '保存' }) as HTMLButtonElement
}

describe('文件编辑器读/改/存（mock 假后端，FR-204）', () => {
  it('双击文件读出种子内容', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const { container } = renderExplorer()

    await openFileInEditor(user, 'server.properties')

    // 编辑器挂载且读出 GET /files/read 的种子文本（包含 motd 那一行）。
    await waitFor(() => {
      expect(container.querySelector('.cm-content')).not.toBeNull()
      expect(editorContent(container).textContent).toContain('motd=A Minecraft Server')
    })
  })

  it('改动 → 保存按钮可用 → 保存后回到已保存态', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const { container } = renderExplorer()

    await openFileInEditor(user, 'server.properties')
    await waitFor(() =>
      expect(editorContent(container).textContent).toContain('motd=A Minecraft Server'),
    )

    // 初始已保存（draft===saved）：保存按钮禁用。
    expect(editorHeaderSaveButton()).toBeDisabled()

    // 在编辑器内输入 → dirty → 保存按钮转可用，且标题出现脏点 •。
    const content = editorContent(container)
    content.focus()
    await user.type(content, 'ZZZ')
    await waitFor(() => expect(editorHeaderSaveButton()).toBeEnabled())

    // 点保存：POST /files/write 成功 → saved 同步为 draft → 保存按钮再次禁用（确定性成功信号）。
    await user.click(editorHeaderSaveButton())
    await waitFor(() => expect(editorHeaderSaveButton()).toBeDisabled())

    // 编辑器内容确含刚输入的片段（仍是 dirty 后写入的那份）。
    expect(editorContent(container).textContent).toContain('ZZZ')
  })

  it('写端点注入 500：保存失败仍停留在脏态（不崩溃）', async () => {
    loginMockUser()
    mockInject('post', '/instances/:id/files/write', { kind: 'status', status: 500 })
    const user = userEvent.setup()
    const { container } = renderExplorer()

    await openFileInEditor(user, 'server.properties')
    await waitFor(() =>
      expect(editorContent(container).textContent).toContain('motd=A Minecraft Server'),
    )

    const content = editorContent(container)
    content.focus()
    await user.type(content, 'ZZZ')
    await waitFor(() => expect(editorHeaderSaveButton()).toBeEnabled())

    // 保存触发 500：catch 后只 toast 错误、不改 saved，故仍 dirty（保存按钮保持可用）。
    await user.click(editorHeaderSaveButton())
    // 给失败回调时间执行；按钮应仍可用（未进入已保存态），且编辑器未崩溃仍在。
    await waitFor(() => expect(editorHeaderSaveButton()).toBeEnabled())
    expect(container.querySelector('.cm-content')).not.toBeNull()
  })
})

/**
 * 编辑器读/写端点的内容级 round-trip：进入子目录打开 config.yml、改写、保存、关闭、重开、读回新内容。
 * 单列一个 describe 以便用清晰的导航步骤（先下钻 plugins/ 再开 config.yml）。
 */
describe('文件编辑器内容 round-trip（mock 假后端，FR-204）', () => {
  it('改写 config.yml → 保存 → 重开读回新内容', async () => {
    loginMockUser()
    const user = userEvent.setup()
    const { container } = renderExplorer()

    // 下钻 plugins 目录（双击右侧列表里的 plugins 目录行）。
    const pluginsRows = await screen.findAllByText('plugins')
    for (const row of pluginsRows) await user.dblClick(row)
    // 打开 config.yml（在 plugins 目录里）。
    await waitFor(() => expect(screen.getByText('config.yml')).toBeInTheDocument())
    await user.dblClick(screen.getByText('config.yml'))

    // 读出种子内容（debug: false）。
    await waitFor(() => expect(editorContent(container).textContent).toContain('debug: false'))

    // 追加一段「同字符」独特标记 QQQ：种子内容不含 Q，且相同字符的连写与顺序无关——
    // 规避 CodeMirror 在 jsdom（零几何）下逐字符光标定位会打乱「不同字符」串的问题，
    // 仍能确定性地证明「写入的新内容」落盘并被读回。
    const content = editorContent(container)
    content.focus()
    await user.type(content, 'QQQ')
    await waitFor(() => {
      expect(editorContent(container).textContent).toContain('QQQ')
      expect(editorHeaderSaveButton()).toBeEnabled()
    })

    // 保存（POST /files/write 落盘到假后端）。
    await user.click(editorHeaderSaveButton())
    await waitFor(() => expect(editorHeaderSaveButton()).toBeDisabled())

    // 关闭编辑器（头部 关闭 X 按钮，title=common.close=关闭）。
    await user.click(screen.getByRole('button', { name: '关闭' }))
    await waitFor(() => expect(container.querySelector('.cm-content')).toBeNull())

    // 重新打开 config.yml → openByPath 走 GET /files/read → 读回刚写入的新内容（含标记 QQQ）。
    await user.dblClick(screen.getByText('config.yml'))
    await waitFor(() => {
      expect(container.querySelector('.cm-content')).not.toBeNull()
      expect(editorContent(container).textContent).toContain('QQQ')
    })
  })
})
