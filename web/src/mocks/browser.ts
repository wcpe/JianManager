import { setupWorker } from 'msw/browser'
import { handlers } from './handlers'
import { terminalWsHandler } from './realtime/terminal-ws'

/**
 * 浏览器 mock 模式（VITE_MOCK）用的 Service Worker（FR-196）。main.tsx 启动时 worker.start()。
 * 比 server 多终端 WS handler —— 终端伪交互只在真浏览器 mock 模式跑（jsdom 不测）。
 */
export const worker = setupWorker(...handlers, terminalWsHandler)
