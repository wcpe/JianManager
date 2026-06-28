import type { HttpHandler } from 'msw'
import { instanceEventsHandlers } from '../realtime/instance-events'
import { terminalTokenHandler } from '../realtime/terminal-ws'

/**
 * 自动聚合 domains/*.ts 的 handler（ADR-047 决策 5）：域簇加文件即生效，无需改本文件。
 * import.meta.glob 由 Vite/vitest 解析；eager 立即 import → 触发各域顶层 db() 播种。
 */
const modules = import.meta.glob<{ handlers?: HttpHandler[] }>('./domains/*.ts', { eager: true })
const domainHandlers: HttpHandler[] = Object.values(modules).flatMap((m) => m.handlers ?? [])

/**
 * server（node/jsdom 测试）与 browser（mock 模式）共用的 handler 集：
 * 域簇 REST + 实例事件 SSE + 终端 token（HTTP）。终端 WS 仅 browser 追加（见 browser.ts）。
 */
export const handlers: HttpHandler[] = [...domainHandlers, ...instanceEventsHandlers, terminalTokenHandler]
