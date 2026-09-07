import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'

vi.mock('@xterm/xterm', async () => {
  const harness = await import('@/test/xterm-ws-harness')
  return { Terminal: harness.MockTerminal }
})
vi.mock('@xterm/addon-fit', async () => {
  const harness = await import('@/test/xterm-ws-harness')
  return { FitAddon: harness.MockFitAddon }
})

import { MockWebSocket, resetTerminalHarness, wsSockets, xtermInstances } from '@/test/xterm-ws-harness'
import { terminalSessionManager } from '@/lib/terminal-session-manager'
import TerminalPane from './TerminalPane'

beforeEach(() => {
  resetTerminalHarness()
  vi.stubGlobal('WebSocket', MockWebSocket)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

/** 等控制台输出区挂载（FR-415 起是 DOM 虚拟列表，不再是 xterm 画布）。 */
async function findOutput() {
  return screen.findByTestId('console-output')
}

async function findSocket() {
  await waitFor(() => expect(wsSockets.length).toBeGreaterThan(0))
  return wsSockets.at(-1)!
}

/** 当前搜索命中所在行的 seq（取代旧实现对 xterm `scrollToLine` 调用的断言）。 */
function currentMatchSeq() {
  const mark = document.querySelector('mark[data-terminal-search-current="true"]')
  return mark?.closest('[data-console-line-seq]')?.getAttribute('data-console-line-seq')
}

/**
 * TerminalPane FIX-B/FR-345 回归：停机（STOPPED）实例打开控制台必须回放 DB 历史，
 * 不发起 WS（杜绝死循环刷断连）；运行中实例放行控制台。
 * seed：id=1 RUNNING、id=2 STOPPED（见 mocks/handlers/domains/instance.ts）。
 *
 * FR-415 起控制台是「DOM 输出区 + 原生命令栏」（ADR-086），故本文件的断言从
 * 「xterm 实例/缓冲」平移到「输出区 DOM / 权威行缓冲」，并新增「这条路径零 xterm」的守卫。
 */
describe('TerminalPane（mock 假后端）', () => {
  it('F11 进入沉浸模式，覆盖实例详情外壳而不改当前路由', async () => {
    loginMockUser()
    const { container } = renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    await findOutput()
    fireEvent.keyDown(container.firstElementChild!, { key: 'F11', bubbles: true })

    const immersive = await screen.findByLabelText('专注终端工作台')
    expect(immersive).toHaveClass('bg-[#0f1115]')
    expect(within(immersive).getByRole('textbox', { name: '控制台命令输入' })).toHaveClass(
      'bg-[#101419]',
      'text-slate-100',
    )
  })

  it('停机实例：按旧→新回放历史正文，重新挂载仍持久且不发起 WS', async () => {
    loginMockUser()
    let logReads = 0
    server.use(
      http.get('*/api/v1/logs', ({ request }) => {
        const url = new URL(request.url)
        if (url.searchParams.get('instanceId') !== '2') return HttpResponse.json({ items: [], total: 0, page: 1, pageSize: 300 })
        logReads += 1
        // 后端契约为 time DESC（新→旧），StoppedLogsView 必须反转为终端阅读顺序（旧→新）。
        return HttpResponse.json({
          items: [
            { id: 2, source: 'instance', level: 'info', instanceId: 2, instanceUuid: 'stopped-2', nodeId: 1, message: '[Server thread/INFO]: ThreadedAnvilChunkStorage: All dimensions are saved', time: '2026-07-18T12:00:02Z' },
            { id: 1, source: 'instance', level: 'info', instanceId: 2, instanceUuid: 'stopped-2', nodeId: 1, message: '[Server thread/INFO]: Saving chunks for level minecraft:overworld', time: '2026-07-18T12:00:01Z' },
          ],
          total: 2,
          page: 1,
          pageSize: 300,
        })
      }),
    )

    const firstMount = renderWithProviders(<TerminalPane instanceId={2} hideHeader />)

    expect(await screen.findByText('实例未运行（STOPPED），显示历史日志')).toBeInTheDocument()
    const oldLine = await screen.findByText('[Server thread/INFO]: Saving chunks for level minecraft:overworld')
    const newLine = screen.getByText('[Server thread/INFO]: ThreadedAnvilChunkStorage: All dimensions are saved')
    expect(oldLine.compareDocumentPosition(newLine) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
    expect(screen.getByRole('link', { name: '查看完整历史' })).toHaveAttribute('href', '/logs?instanceId=2')
    expect(xtermInstances).toHaveLength(0)
    expect(wsSockets).toHaveLength(0)

    firstMount.unmount()
    renderWithProviders(<TerminalPane instanceId={2} hideHeader />)
    expect(await screen.findByText('[Server thread/INFO]: Saving chunks for level minecraft:overworld')).toBeInTheDocument()
    await waitFor(() => expect(logReads).toBeGreaterThanOrEqual(2))
    expect(xtermInstances).toHaveLength(0)
    expect(wsSockets).toHaveLength(0)
  })

  /** spec §2.3：停机态输入禁用 + 一行原因 + 「启动实例」直达动作。 */
  it('停机实例：命令栏禁用并给出原因与「启动实例」直达动作', async () => {
    loginMockUser()
    server.use(
      http.get('*/api/v1/logs', () => HttpResponse.json({ items: [], total: 0, page: 1, pageSize: 300 })),
    )
    renderWithProviders(<TerminalPane instanceId={2} hideHeader />)

    const input = await screen.findByRole('textbox', { name: '控制台命令输入' })
    expect(input).toBeDisabled()
    expect(screen.getByText('实例未运行（STOPPED），命令输入已禁用')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '启动实例' })).toBeEnabled()
  })

  it('运行中实例：挂载控制台（不显示停机占位），且不创建 xterm', async () => {
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    // 状态拉到 RUNNING 后挂载控制台。
    await findOutput()
    await findSocket()
    await waitFor(() => {
      expect(screen.queryByText(/实例未运行/)).not.toBeInTheDocument()
    })
    // ADR-086：实例控制台路径不再经 xterm 渲染。
    expect(xtermInstances).toHaveLength(0)
  })

  it('运行中实例：显示读写徽标、重连和字号工具', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    // 字号落在输出区的行内 style 上（DOM 输出区的字号是纯 CSS，不再是 xterm option）。
    expect(await findOutput()).toHaveStyle({ fontSize: '14px' })
    expect(screen.getByText('可写')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重新连接' })).toBeInTheDocument()

    await user.click(await screen.findByRole('button', { name: '放大字号' }))
    expect(await screen.findByText('15px')).toBeInTheDocument()
    expect(await findOutput()).toHaveStyle({ fontSize: '15px' })
  })

  it('运行中实例：全屏按钮可切换并保留可访问状态', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    await findOutput()
    const fullscreen = screen.getByRole('button', { name: '全屏' })
    await user.click(fullscreen)

    expect(screen.getByRole('button', { name: '退出全屏' })).toHaveAttribute('aria-pressed', 'true')

    await user.click(screen.getByRole('button', { name: '退出全屏' }))
    expect(screen.getByRole('button', { name: '全屏' })).toHaveAttribute('aria-pressed', 'false')
  })

  it('运行中实例：搜索按钮和 Ctrl+F 打开控制台搜索，输入后显示匹配反馈', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    await findOutput()
    const ws = await findSocket()
    act(() => ws.emitMessage({ type: 'stdout', data: 'stop\nsay hello\nstop again\n' }))
    await user.click(screen.getByRole('button', { name: '搜索终端' }))

    const input = screen.getByRole('searchbox', { name: '搜索终端输入' })
    await user.type(input, 'stop')
    expect(screen.getByRole('status')).toHaveTextContent('第 1 / 2 项')

    await user.keyboard('{Escape}')
    expect(screen.queryByRole('searchbox', { name: '搜索终端输入' })).not.toBeInTheDocument()

    // Ctrl+F 由 TerminalPane 的 keydown 承接（原实现挂在 xterm 的自定义键处理上）。
    await user.click(screen.getByRole('textbox', { name: '控制台命令输入' }))
    await user.keyboard('{Control>}f{/Control}')
    expect(screen.getByRole('searchbox', { name: '搜索终端输入' })).toBeInTheDocument()
  })

  it('运行中实例：搜索高亮当前项，并支持上一条/下一条定位切换', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    await findOutput()
    const ws = await findSocket()
    act(() => {
      ws.emitMessage({ type: 'stdout', data: 'alpha stop\nbeta stop\nstop gamma\n' })
    })

    await user.click(screen.getByRole('button', { name: '搜索终端' }))
    await user.type(screen.getByRole('searchbox', { name: '搜索终端输入' }), 'stop')

    expect(screen.getByRole('status')).toHaveTextContent('第 1 / 3 项')
    expect(document.querySelectorAll('mark[data-terminal-search-match="true"]')).toHaveLength(3)
    expect(document.querySelector('mark[data-terminal-search-current="true"]')).toHaveTextContent('stop')
    // 当前项落在第 1 行（seq 0），取代旧实现对 scrollToLine(0) 的断言。
    expect(currentMatchSeq()).toBe('0')

    await user.click(screen.getByRole('button', { name: '下一条匹配' }))
    expect(screen.getByRole('status')).toHaveTextContent('第 2 / 3 项')
    expect(document.querySelectorAll('mark[data-terminal-search-current="true"]')).toHaveLength(1)
    expect(currentMatchSeq()).toBe('1')

    await user.click(screen.getByRole('button', { name: '上一条匹配' }))
    expect(screen.getByRole('status')).toHaveTextContent('第 1 / 3 项')
    expect(currentMatchSeq()).toBe('0')
  })

  it('FR-276：WORKER_TOKEN_REJECTED 错误显示密钥不一致定向诊断，而非裸 [状态: error]', async () => {
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    await findOutput()
    const ws = await findSocket()
    act(() => {
      ws.emitMessage({
        type: 'state',
        state: 'error',
        code: 'WORKER_TOKEN_REJECTED',
        data: '终端令牌被 Worker 拒绝（HTTP 401）：该节点的 WS 令牌密钥与平台不一致。',
      })
    })

    // 断言渲染出的 DOM 而非缓冲字符串：诊断必须真的出现在用户眼前。
    expect(await screen.findByText('[终端令牌被节点拒绝]')).toBeInTheDocument()
    expect(screen.getByText(/WS 令牌密钥与平台不一致/)).toBeInTheDocument()
  })

  it('FR-276：一般错误态带出 data 原因；无 data 维持原 [状态: xxx] 行为', async () => {
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    await findOutput()
    const ws = await findSocket()
    act(() => {
      ws.emitMessage({ type: 'state', state: 'error', data: '连接 Worker 失败: dial tcp: refused' })
      ws.emitMessage({ type: 'state', state: 'running' })
    })

    expect(await screen.findByText('[状态: error] 连接 Worker 失败: dial tcp: refused')).toBeInTheDocument()
    expect(screen.getByText('[状态: running]')).toBeInTheDocument()
    expect(screen.queryByText('[终端令牌被节点拒绝]')).not.toBeInTheDocument()
  })

  /** spec §2.2：Enter 提交整行 + 本地回显 kind:'command'；data 不补行尾（Worker 侧补）。 */
  it('运行中实例：命令栏 Enter 提交整行并本地回显', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<TerminalPane instanceId={1} hideHeader />)

    await findOutput()
    const ws = await findSocket()
    await waitFor(() => expect(terminalSessionManager.getState(1)).toBe('connected'))

    await user.type(screen.getByRole('textbox', { name: '控制台命令输入' }), 'say 大家好{Enter}')

    // 本地回显：不等服务端就出现在输出区。
    expect(await screen.findByText('say 大家好')).toBeInTheDocument()
    expect(terminalSessionManager.getLines(1).at(-1)).toMatchObject({ kind: 'command', body: 'say 大家好' })
    // 整行下发一次，且不含额外换行（Worker 的 SendCommand 自己补 \n）。
    expect(ws.sent.map((raw) => JSON.parse(raw))).toEqual([
      { type: 'stdin', instanceId: '1', data: 'say 大家好' },
    ])
    // 提交后输入框清空。
    expect(screen.getByRole('textbox', { name: '控制台命令输入' })).toHaveValue('')
  })

  /** FR-140 回归：一次性 token 首连即被消费，重连必须重取。 */
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
    await waitFor(() => expect(wsSockets.length).toBeGreaterThan(1))
    const secondSocket = wsSockets.at(-1)!
    const secondToken = new URL(secondSocket.url).searchParams.get('token')

    // 重连必须重新拉取 token（bug 表现为 0 次重取）。
    expect(tokenFetches).toBeGreaterThan(fetchesBeforeReconnect)
    // 新连接携带的是新签发的一次性 token，而非复用首连已消费的旧 token。
    expect(secondToken).not.toBe(firstToken)
  })
})
