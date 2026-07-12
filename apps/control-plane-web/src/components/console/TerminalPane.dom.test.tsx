import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@/mocks/server'

const xtermHarness = vi.hoisted(() => {
  type DataHandler = (data: string) => void
  type ScrollHandler = (line: number) => void
  type KeyHandler = (event: KeyboardEvent) => boolean

  class MockBufferLine {
    constructor(private readonly text: string) {}

    translateToString() {
      return this.text
    }
  }

  class MockTerminal {
    cols = 80
    rows = 24
    options: { fontSize?: number }
    lines = ['']
    viewportY = 0
    scrollCalls: number[] = []
    buffer: {
      active: {
        readonly length: number
        readonly viewportY: number
        getLine: (index: number) => MockBufferLine | undefined
      }
    }
    private rowsEl: HTMLDivElement | null = null
    private dataHandlers: DataHandler[] = []
    private scrollHandlers: ScrollHandler[] = []
    private keyHandler: KeyHandler | null = null
    private selection = ''

    constructor(options: { fontSize?: number }) {
      this.options = options
      instances.push(this)
      this.buffer = {
        active: {
          get length() {
            return 0
          },
          get viewportY() {
            return 0
          },
          getLine: (index: number) => {
            const line = this.lines[index]
            return line === undefined ? undefined : new MockBufferLine(line)
          },
        },
      }
      Object.defineProperty(this.buffer.active, 'length', { get: () => this.lines.length })
      Object.defineProperty(this.buffer.active, 'viewportY', { get: () => this.viewportY })
    }

    loadAddon() {}

    open(container: HTMLElement) {
      this.rowsEl = document.createElement('div')
      this.rowsEl.className = 'xterm-rows'
      container.append(this.rowsEl)
      this.renderRows()
    }

    write(data: string) {
      const parts = data.replace(/\r\n/g, '\n').replace(/\r/g, '\n').split('\n')
      this.lines[this.lines.length - 1] += parts[0]
      for (const part of parts.slice(1)) this.lines.push(part)
      this.renderRows()
    }

    attachCustomKeyEventHandler(handler: KeyHandler) {
      this.keyHandler = handler
    }

    onData(handler: DataHandler) {
      this.dataHandlers.push(handler)
      return { dispose: () => { this.dataHandlers = this.dataHandlers.filter((item) => item !== handler) } }
    }

    onScroll(handler: ScrollHandler) {
      this.scrollHandlers.push(handler)
      return { dispose: () => { this.scrollHandlers = this.scrollHandlers.filter((item) => item !== handler) } }
    }

    emitData(data: string) {
      this.dataHandlers.forEach((handler) => handler(data))
    }

    emitKey(event: KeyboardEvent) {
      return this.keyHandler?.(event)
    }

    scrollToLine(line: number) {
      this.viewportY = Math.max(0, Math.min(line, this.lines.length - 1))
      this.scrollCalls.push(this.viewportY)
      this.renderRows()
      this.scrollHandlers.forEach((handler) => handler(this.viewportY))
    }

    selectAll() {
      this.selection = this.lines.join('\n')
    }

    getSelection() {
      return this.selection
    }

    clearSelection() {
      this.selection = ''
    }

    clear() {
      this.lines = ['']
      this.renderRows()
    }

    focus() {}

    dispose() {}

    private renderRows() {
      if (!this.rowsEl) return
      this.rowsEl.replaceChildren()
      for (const line of this.lines.slice(this.viewportY, this.viewportY + this.rows)) {
        const row = document.createElement('div')
        row.textContent = line
        this.rowsEl.append(row)
      }
    }
  }

  class MockFitAddon {
    fit() {}
  }

  const instances: MockTerminal[] = []
  return { instances, MockTerminal, MockFitAddon }
})

const wsHarness = vi.hoisted(() => {
  class MockWebSocket {
    static CONNECTING = 0
    static OPEN = 1
    static CLOSED = 3
    readyState = MockWebSocket.CONNECTING
    sent: string[] = []
    onopen: ((event: Event) => void) | null = null
    onmessage: ((event: MessageEvent) => void) | null = null
    onclose: ((event: CloseEvent) => void) | null = null
    onerror: ((event: Event) => void) | null = null

    constructor(readonly url: string) {
      sockets.push(this)
      window.setTimeout(() => {
        this.readyState = MockWebSocket.OPEN
        this.onopen?.(new Event('open'))
      }, 0)
    }

    send(data: string) {
      this.sent.push(data)
    }

    close() {
      this.readyState = MockWebSocket.CLOSED
      this.onclose?.(new CloseEvent('close'))
    }

    emitMessage(data: unknown) {
      this.onmessage?.({ data: JSON.stringify(data) } as MessageEvent)
    }
  }

  const sockets: MockWebSocket[] = []
  return { sockets, MockWebSocket }
})

vi.mock('@xterm/xterm', () => ({
  Terminal: xtermHarness.MockTerminal,
}))

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: xtermHarness.MockFitAddon,
}))

import TerminalPane from './TerminalPane'

beforeEach(() => {
  xtermHarness.instances.length = 0
  wsHarness.sockets.length = 0
  vi.stubGlobal('WebSocket', wsHarness.MockWebSocket)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

async function findTerminal() {
  await waitFor(() => expect(xtermHarness.instances.length).toBeGreaterThan(0))
  return xtermHarness.instances.at(-1)!
}

async function findSocket() {
  await waitFor(() => expect(wsHarness.sockets.length).toBeGreaterThan(0))
  return wsHarness.sockets.at(-1)!
}

/**
 * TerminalPane FIX-B 回归：停机（STOPPED）实例打开终端必须呈现「实例未运行」静态占位、
 * 不挂载终端、不发起 WS（杜绝死循环刷断连）；运行中实例放行终端。
 * seed：id=1 RUNNING、id=2 STOPPED（见 mocks/handlers/domains/instance.ts）。
 */
describe('TerminalPane（mock 假后端）', () => {
  it('停机实例：显示「实例未运行」占位、不挂载终端', async () => {
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={2} hideHeader />)

    // 占位文案出现（status 注入到 terminalNotRunning）。
    expect(await screen.findByText(/实例未运行（STOPPED）/)).toBeInTheDocument()
    expect(screen.getByText('启动实例后即可使用终端')).toBeInTheDocument()
    // 不挂载真实终端 → 不发起 WS → 不会刷「连接已断开」。
    expect(xtermHarness.instances).toHaveLength(0)
    expect(wsHarness.sockets).toHaveLength(0)
  })

  it('运行中实例：挂载终端（不显示停机占位）', async () => {
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    // 状态拉到 RUNNING 后挂载终端。
    await findTerminal()
    await findSocket()
    await waitFor(() => {
      expect(screen.queryByText(/实例未运行/)).not.toBeInTheDocument()
    })
  })

  it('运行中实例：显示读写徽标、重连和字号工具', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    expect((await findTerminal()).options.fontSize).toBe(14)
    expect(screen.getByText('可写')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重新连接' })).toBeInTheDocument()

    await user.click(await screen.findByRole('button', { name: '放大字号' }))
    expect(await screen.findByText('15px')).toBeInTheDocument()
    expect((await findTerminal()).options.fontSize).toBe(15)
  })

  it('运行中实例：全屏按钮可切换并保留可访问状态', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    await findTerminal()
    const fullscreen = screen.getByRole('button', { name: '全屏' })
    await user.click(fullscreen)

    expect(screen.getByRole('button', { name: '退出全屏' })).toHaveAttribute('aria-pressed', 'true')

    await user.click(screen.getByRole('button', { name: '退出全屏' }))
    expect(screen.getByRole('button', { name: '全屏' })).toHaveAttribute('aria-pressed', 'false')
  })

  it('运行中实例：搜索按钮和 Ctrl+F 打开终端搜索，输入后显示匹配反馈', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    const term = await findTerminal()
    act(() => term.write('stop\r\nsay hello\r\nstop again'))
    await user.click(screen.getByRole('button', { name: '搜索终端' }))

    const input = screen.getByRole('searchbox', { name: '搜索终端输入' })
    await user.type(input, 'stop')
    expect(screen.getByRole('status')).toHaveTextContent('第 1 / 2 项')

    await user.keyboard('{Escape}')
    expect(screen.queryByRole('searchbox', { name: '搜索终端输入' })).not.toBeInTheDocument()

    act(() => term.emitKey(new KeyboardEvent('keydown', { key: 'f', ctrlKey: true })))
    expect(screen.getByRole('searchbox', { name: '搜索终端输入' })).toBeInTheDocument()
  })

  it('运行中实例：搜索高亮当前项，并支持上一条/下一条定位切换', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    const term = await findTerminal()
    const ws = await findSocket()
    act(() => {
      ws.emitMessage({ type: 'stdout', data: 'alpha stop\nbeta stop\nstop gamma\n' })
    })

    await user.click(screen.getByRole('button', { name: '搜索终端' }))
    await user.type(screen.getByRole('searchbox', { name: '搜索终端输入' }), 'stop')

    expect(screen.getByRole('status')).toHaveTextContent('第 1 / 3 项')
    expect(document.querySelectorAll('mark[data-terminal-search-match="true"]')).toHaveLength(3)
    expect(document.querySelector('mark[data-terminal-search-current="true"]')).toHaveTextContent('stop')
    expect(term.scrollCalls.at(-1)).toBe(0)

    await user.click(screen.getByRole('button', { name: '下一条匹配' }))
    expect(screen.getByRole('status')).toHaveTextContent('第 2 / 3 项')
    expect(document.querySelectorAll('mark[data-terminal-search-current="true"]')).toHaveLength(1)
    expect(term.scrollCalls.at(-1)).toBe(1)

    await user.click(screen.getByRole('button', { name: '上一条匹配' }))
    expect(screen.getByRole('status')).toHaveTextContent('第 1 / 3 项')
    expect(term.scrollCalls.at(-1)).toBe(0)
  })

  it('FR-276：WORKER_TOKEN_REJECTED 错误显示密钥不一致定向诊断，而非裸 [状态: error]', async () => {
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    const term = await findTerminal()
    const ws = await findSocket()
    act(() => {
      ws.emitMessage({
        type: 'state',
        state: 'error',
        code: 'WORKER_TOKEN_REJECTED',
        data: '终端令牌被 Worker 拒绝（HTTP 401）：该节点的 WS 令牌密钥与平台不一致。',
      })
    })

    const text = term.lines.join('\n')
    expect(text).toContain('[终端令牌被节点拒绝]')
    expect(text).toContain('WS 令牌密钥与平台不一致')
  })

  it('FR-276：一般错误态带出 data 原因；无 data 维持原 [状态: xxx] 行为', async () => {
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    const term = await findTerminal()
    const ws = await findSocket()
    act(() => {
      ws.emitMessage({ type: 'state', state: 'error', data: '连接 Worker 失败: dial tcp: refused' })
      ws.emitMessage({ type: 'state', state: 'running' })
    })

    const text = term.lines.join('\n')
    expect(text).toContain('[状态: error] 连接 Worker 失败: dial tcp: refused')
    expect(text).toContain('[状态: running]')
    expect(text).not.toContain('[终端令牌被节点拒绝]')
  })

  /**
   * FR-140 回归：终端一次性 token 首连即被 CP 消费失效（onetimetoken.Store），
   * 点「重新连接」必须重新拉取一条新的一次性 token；若复用已消费的旧 token，
   * /ws/terminal 会以 401「token already used」拒绝，导致重连永不恢复。
   * 断言重连触发了新的 terminal-token 拉取，且新连接携带的是新 token（非复用旧 token）。
   */
  it('FR-140：点重新连接必须重新拉取一次性 token，不复用已消费的旧 token', async () => {
    const user = userEvent.setup()
    loginMockUser()

    // 每次拉取返回递增的唯一 token，用于识别「是否重取 / 是否复用」。
    let tokenFetches = 0
    server.use(
      http.get('*/api/v1/instances/:id/terminal-token', () => {
        tokenFetches += 1
        return HttpResponse.json({
          token: `once-token-${tokenFetches}`,
          wsUrl: 'ws://localhost/_mock/terminal',
          expiresIn: 30,
        })
      }),
    )

    renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    const firstSocket = await findSocket()
    const firstToken = new URL(firstSocket.url).searchParams.get('token')
    const fetchesBeforeReconnect = tokenFetches

    await user.click(screen.getByRole('button', { name: '重新连接' }))

    // 重连应新建一条 WS 连接。
    await waitFor(() => expect(wsHarness.sockets.length).toBeGreaterThan(1))
    const secondSocket = wsHarness.sockets.at(-1)!
    const secondToken = new URL(secondSocket.url).searchParams.get('token')

    // 重连必须重新拉取 token（bug 表现为 0 次重取）。
    expect(tokenFetches).toBeGreaterThan(fetchesBeforeReconnect)
    // 新连接携带的是新签发的一次性 token，而非复用首连已消费的旧 token。
    expect(secondToken).not.toBe(firstToken)
  })
})
