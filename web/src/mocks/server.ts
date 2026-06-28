import { setupServer } from 'msw/node'
import { handlers } from './handlers'

/**
 * vitest（node / jsdom）用的 MSW 拦截服务器（FR-196）。
 * 生命周期（listen/resetHandlers/close）+ 假后端 resetDb 由 web/src/test/setup.ts 统一管理。
 * 不含终端 WS handler（jsdom WebSocket 不稳，见 ADR-047/spec §6）。
 */
export const server = setupServer(...handlers)
