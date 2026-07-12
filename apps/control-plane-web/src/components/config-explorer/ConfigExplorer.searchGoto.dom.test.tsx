import { describe, it, expect, beforeAll, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { BrowserRouter } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/i18n'
import { loginMockUser } from '@/test/auth'
import ConfigExplorer from './ConfigExplorer'

/**
 * jsdom 缺 Range.getClientRects / getBoundingClientRect，CodeMirror 6 的坐标测量会抛异常。
 * 与 ResourceEditor.dom.test.tsx 同款最小垫片（返回零矩形，本文件断言不依赖布局几何）。
 */
beforeAll(() => {
  Range.prototype.getClientRects = () =>
    ({ length: 0, item: () => null, [Symbol.iterator]: function* () {} }) as unknown as DOMRectList
  Range.prototype.getBoundingClientRect = () =>
    ({ x: 0, y: 0, width: 0, height: 0, top: 0, left: 0, right: 0, bottom: 0, toJSON: () => ({}) }) as DOMRect
})

/** 与 renderWithProviders 等价的 Provider 链，但返回 container 以便查询 CodeMirror 宿主 DOM。 */
function renderConfigExplorer(instanceId = 1) {
  window.history.pushState({}, '', '/')
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <BrowserRouter>
      <QueryClientProvider client={queryClient}>
        <ConfigExplorer instanceId={instanceId} />
      </QueryClientProvider>
    </BrowserRouter>,
  )
}

/**
 * 配置模式全文搜索命中定位（FR-074 复验缺陷）。
 *
 * 从配置管理主入口（ConfigExplorer → ResourceExplorer configMode）打开搜索面板、
 * 点击内容命中后，配置编辑器不仅要打开该文件，还必须把光标定位到命中行——
 * 与纯文件模式（CodeEditor gotoLine）行为一致。
 *
 * 断言策略：CodeMirror highlightActiveLineGutter 会给光标所在行的行号 gutter 加
 * `.cm-activeLineGutter`。files 种子中 server.properties 的 `max-players` 在第 2 行，
 * 命中点击后活动行号应为 2（缺陷态：不定位，光标停留第 1 行）。
 */
describe('ConfigExplorer 搜索命中定位（FR-074）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('configMode 点击搜索命中后编辑器定位到命中行', async () => {
    const user = userEvent.setup()
    const { container } = renderConfigExplorer()

    // 等资源管理器列表就绪后打开搜索面板。
    await screen.findAllByText('server.properties')
    await user.click(screen.getByRole('button', { name: '搜索' }))

    // 输入关键字（300ms debounce 后走 POST /files/search）→ 命中 server.properties 第 2 行。
    await user.type(screen.getByPlaceholderText('输入关键字搜索文件内容…'), 'max-players')
    const hit = await screen.findByText((_, node) => node?.textContent === 'max-players=20')
    await user.click(hit.closest('button') ?? hit)

    // 配置编辑器打开该文件（读配置端点内容含 server-port 行）。
    await waitFor(() => {
      const content = container.querySelector('.cm-content')
      expect(content).not.toBeNull()
      expect(content!.textContent).toContain('server-port=25565')
    })

    // 光标定位到命中行：活动行号 gutter 为 2。
    await waitFor(() => {
      const activeGutter = container.querySelector('.cm-activeLineGutter')
      expect(activeGutter).not.toBeNull()
      expect(activeGutter!.textContent).toBe('2')
    })
  })
})
