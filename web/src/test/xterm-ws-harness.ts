/**
 * jsdom 终端测试共享 harness（FR-295/296）：xterm 与 WebSocket 的可断言替身。
 *
 * 用法（vi.mock 工厂内动态 import，绕开 hoisting 限制）：
 * ```ts
 * vi.mock('@xterm/xterm', async () => {
 *   const harness = await import('@/test/xterm-ws-harness')
 *   return { Terminal: harness.MockTerminal }
 * })
 * vi.mock('@xterm/addon-fit', async () => {
 *   const harness = await import('@/test/xterm-ws-harness')
 *   return { FitAddon: harness.MockFitAddon }
 * })
 * ```
 * beforeEach 调 {@link resetTerminalHarness} 并 `vi.stubGlobal('WebSocket', MockWebSocket)`。
 */

type DataHandler = (data: string) => void
type ScrollHandler = (line: number) => void
type KeyHandler = (event: KeyboardEvent) => boolean

class MockBufferLine {
  constructor(private readonly text: string) {}

  translateToString() {
    return this.text
  }
}

/** 可断言的 xterm 替身：按行渲染进 `.xterm-rows`，记录 write/dispose。 */
export class MockTerminal {
  cols = 80
  rows = 24
  options: { fontSize?: number }
  lines = ['']
  viewportY = 0
  scrollCalls: number[] = []
  disposed = false
  element: HTMLElement | undefined
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

  constructor(options: { fontSize?: number } = {}) {
    this.options = options
    xtermInstances.push(this)
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
    this.element = document.createElement('div')
    this.element.className = 'terminal xterm'
    this.rowsEl = document.createElement('div')
    this.rowsEl.className = 'xterm-rows'
    this.element.append(this.rowsEl)
    container.append(this.element)
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

  dispose() {
    this.disposed = true
  }

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

export class MockFitAddon {
  fitCalls = 0
  fit() {
    this.fitCalls++
  }
}

/** 记录构造顺序的 WebSocket 替身：0ms 后自动 open，可注入服务端消息。 */
export class MockWebSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSED = 3
  readyState = MockWebSocket.CONNECTING
  sent: string[] = []
  closedByClient = false
  onopen: ((event: Event) => void) | null = null
  onmessage: ((event: MessageEvent) => void) | null = null
  onclose: ((event: CloseEvent) => void) | null = null
  onerror: ((event: Event) => void) | null = null

  constructor(readonly url: string) {
    wsSockets.push(this)
    window.setTimeout(() => {
      if (this.readyState !== MockWebSocket.CONNECTING) return
      this.readyState = MockWebSocket.OPEN
      this.onopen?.(new Event('open'))
    }, 0)
  }

  send(data: string) {
    this.sent.push(data)
  }

  close() {
    this.closedByClient = true
    this.readyState = MockWebSocket.CLOSED
    this.onclose?.(new CloseEvent('close'))
  }

  emitMessage(data: unknown) {
    this.onmessage?.({ data: JSON.stringify(data) } as MessageEvent)
  }
}

/** 当前测试文件内创建的全部 xterm 替身（按创建顺序）。 */
export const xtermInstances: MockTerminal[] = []

/** 当前测试文件内创建的全部 WS 替身（按创建顺序）。 */
export const wsSockets: MockWebSocket[] = []

/** 每例重置 harness 记录（beforeEach 调用）。 */
export function resetTerminalHarness() {
  xtermInstances.length = 0
  wsSockets.length = 0
}
