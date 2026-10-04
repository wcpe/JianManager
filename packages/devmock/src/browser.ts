import { setupWorker } from 'msw/browser'
import { handlers } from './handlers'
import { terminalWsHandler } from './realtime/terminal-ws'
import { isMockRequestLogEnabled, setMockRequestLogEnabled } from './runtime-control'

/**
 * 浏览器 mock 模式（VITE_MOCK）用的 Service Worker（FR-196）。main.tsx 启动时调 `startMockWorker()`。
 * 比 server 多终端 WS handler —— 终端伪交互只在真浏览器 mock 模式跑（jsdom 不测）。
 */
export const worker = setupWorker(...handlers, terminalWsHandler)

/**
 * 起 worker（FR-496 阶段 6 补丁）。
 *
 * 默认 **quiet**：MSW 每拦一个请求就往控制台打一组
 * 「Request / Handler / Response」日志，且响应体整段进 console——首屏几十个请求直接刷穿控制台，
 * `/instances` 那一行还是 1200 条实例，真正想看的前端日志、React 警告全被埋掉。
 * 需要看「这个请求命中了哪个 handler、返回了什么」时，用调试面板的「请求日志」开关临时打开。
 * 静默只关日志，拦截行为、`onUnhandledRequest: 'bypass'` 语义都不变。
 */
export async function startMockWorker(): Promise<void> {
  await worker.start({ onUnhandledRequest: 'bypass', quiet: !isMockRequestLogEnabled() })
}

/**
 * 运行时切换 MSW 请求日志（FR-496 阶段 6 补丁）。
 *
 * MSW 只在 `start()` 读一次 `quiet`（其内部把 quiet 塞进网络层的 resolutionContext），
 * 没有运行时开关，所以这里只能 `stop()` → `start()` 重配一次网络层。窗口期内（毫秒级）
 * 在途请求会落回真实网络，对本地 dev 无害；先落持久化状态，保证刷新后与界面显示一致。
 */
export async function setMockRequestLog(enabled: boolean): Promise<void> {
  if (enabled === isMockRequestLogEnabled()) return
  setMockRequestLogEnabled(enabled)
  worker.stop()
  await worker.start({ onUnhandledRequest: 'bypass', quiet: !enabled })
}
