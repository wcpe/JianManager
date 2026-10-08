/**
 * jsdom 终端测试共享 harness（FR-295/296）：**只提供 WebSocket 替身**。
 *
 * 历史上它还提供 xterm / FitAddon 替身（故仍叫这个文件名，改名会牵动一批测试文件，
 * 不值当）。xterm 渲染壳已全站下线（FR-415 之后实例控制台走 DOM 输出区，ADR-086），
 * `@xterm/*` 依赖与 mock 一并移除，此处只剩 WS 桩。
 *
 * 用法：beforeEach 调 {@link resetTerminalHarness} 并
 * `vi.stubGlobal('WebSocket', MockWebSocket)`。
 */

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
  readonly url: string

  constructor(url: string) {
    this.url = url
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

/** 当前测试文件内创建的全部 WS 替身（按创建顺序）。 */
export const wsSockets: MockWebSocket[] = []

/** 每例重置 harness 记录（beforeEach 调用）。 */
export function resetTerminalHarness() {
  wsSockets.length = 0
}
