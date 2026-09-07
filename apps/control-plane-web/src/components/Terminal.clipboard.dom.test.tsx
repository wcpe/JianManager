import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'

vi.mock('@xterm/xterm', async () => {
  const harness = await import('@/test/xterm-ws-harness')
  return { Terminal: harness.MockTerminal }
})
vi.mock('@xterm/addon-fit', async () => {
  const harness = await import('@/test/xterm-ws-harness')
  return { FitAddon: harness.MockFitAddon }
})
vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

import { toast } from 'sonner'
import { MockWebSocket, resetTerminalHarness, xtermInstances } from '@/test/xterm-ws-harness'
import TerminalComponent from './Terminal'

/** 把 navigator.clipboard 置为指定值（undefined = 模拟 HTTP 非安全上下文）。 */
const stubClipboard = (value: unknown) => {
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value })
}

/** 把 document.execCommand 置为返回 ok 的 spy（jsdom 默认不实现）。 */
const stubExecCommand = (ok: boolean) => {
  const spy = vi.fn(() => ok)
  Object.defineProperty(document, 'execCommand', { configurable: true, value: spy })
  return spy
}

let nextInstanceId = 9001

/** 挂载终端并等 xterm 就绪；每例用独立 instanceId，避免常驻会话跨例复用。 */
async function mountTerminal() {
  const instanceId = String(nextInstanceId++)
  renderWithProviders(
    <TerminalComponent
      instanceId={instanceId}
      fetchToken={async () => ({ wsUrl: 'ws://localhost/_mock/terminal', token: 'once-token' })}
    />,
  )
  await waitFor(() => expect(xtermInstances.length).toBeGreaterThan(0))
  return xtermInstances.at(-1)!
}

/** 在终端区右键唤出上下文菜单（事件冒泡到容器 div 的 onContextMenu）。 */
async function openContextMenu() {
  fireEvent.contextMenu(document.querySelector('.xterm')!)
  await screen.findByRole('menu', { name: '终端菜单' })
}

beforeEach(() => {
  resetTerminalHarness()
  vi.stubGlobal('WebSocket', MockWebSocket)
  vi.mocked(toast.success).mockClear()
  vi.mocked(toast.error).mockClear()
})

afterEach(() => {
  vi.unstubAllGlobals()
  stubClipboard(undefined)
})

/**
 * BUG-1 / BUG-2 回归：终端剪贴板在 HTTP 非安全上下文（生产验收主机
 * http://103.45.143.199:50100）下的行为。此时整个 navigator.clipboard 为 undefined：
 * - 复制：必须走 execCommand 回退，且成功/失败都出 toast（原实现 void 掉 Promise，静默）
 * - 粘贴：读方向无 JS 兜底，必须明确提示改按 Ctrl+V（原实现 `?.readText()` 静默返回
 *   undefined，落到 `if (text)` 为假 → 点了毫无反应）
 */
describe('Terminal 剪贴板（HTTP 非安全上下文）', () => {
  it('复制全部：navigator.clipboard 不存在时走 execCommand 回退并提示已复制', async () => {
    const user = userEvent.setup()
    stubClipboard(undefined)
    const execCommand = stubExecCommand(true)

    const term = await mountTerminal()
    act(() => term.write('[Server thread/INFO]: Done (3.1s)'))

    await openContextMenu()
    await user.click(screen.getByRole('menuitem', { name: '复制全部' }))

    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('已复制'))
    expect(execCommand).toHaveBeenCalledWith('copy')
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('复制全部：连 execCommand 回退都失败时提示失败并给出手动复制路径', async () => {
    const user = userEvent.setup()
    stubClipboard(undefined)
    stubExecCommand(false)

    const term = await mountTerminal()
    act(() => term.write('[Server thread/INFO]: Done (3.1s)'))

    await openContextMenu()
    await user.click(screen.getByRole('menuitem', { name: '复制全部' }))

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('复制失败，请手动选中文本后按 Ctrl+C'))
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('复制选中（Ctrl+C 路径）：同样给出复制回执，而非静默', async () => {
    stubClipboard(undefined)
    stubExecCommand(true)

    const term = await mountTerminal()
    act(() => term.write('say hello'))
    act(() => term.selectAll())
    // 终端内 Ctrl+C 的语义是「复制选区」（MC 控制台无中断语义）。
    act(() => term.emitData('\x03'))

    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('已复制'))
  })

  it('粘贴：navigator.clipboard 不存在时提示改按 Ctrl+V，不再静默无反应', async () => {
    const user = userEvent.setup()
    stubClipboard(undefined)

    const term = await mountTerminal()
    const linesBefore = [...term.lines]

    await openContextMenu()
    await user.click(screen.getByRole('menuitem', { name: '粘贴' }))

    // 核心断言：必须有明确反馈。bug 表现为一个 toast 都不出、终端也毫无变化。
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        '浏览器不允许读取剪贴板（当前为 HTTP 非安全上下文），请改按 Ctrl+V 粘贴',
      ),
    )
    // 读不到内容自然不写入终端；断言没有把 undefined 之类的脏值写进输入行。
    expect(term.lines).toEqual(linesBefore)
  })

  it('粘贴：安全上下文可读时把多行内容折成单行写入输入行', async () => {
    const user = userEvent.setup()
    stubClipboard({ readText: vi.fn(async () => 'say hello\nworld') })

    const term = await mountTerminal()

    await openContextMenu()
    await user.click(screen.getByRole('menuitem', { name: '粘贴' }))

    await waitFor(() => expect(term.lines.join('\n')).toContain('say hello world'))
    expect(toast.error).not.toHaveBeenCalled()
  })
})
