import '@testing-library/jest-dom/vitest'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { cleanup } from '@testing-library/react'
import { server } from '@/mocks/server'
import { resetDb } from '@/mocks/db'
import { clearInjections } from '@/mocks/inject'
import { useAuthStore } from '@/stores/auth'
import { terminalSessionManager } from '@/lib/terminal-session-manager'

/**
 * jsdom 组件 / 页面测试的全局 setup（FR-196，vitest dom project）。
 * onUnhandledRequest:'error' 是有意的覆盖闸：未 mock 的请求即让测试失败，逼域簇补齐 handler。
 * 每例后卸载 DOM + 重置 handler 覆盖 + 假后端 + 注入 + 鉴权态（localStorage / store），保证用例隔离
 * （否则成功登录用例写入的 token 会泄漏到下个用例，使 LoginPage 误判已登录而重定向）。
 */
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => {
  cleanup()
  server.resetHandlers()
  resetDb()
  clearInjections()
  localStorage.clear()
  useAuthStore.getState().logout()
  // 终端会话常驻单例管理器（FR-295，ADR-067）：组件卸载不再断连，
  // 每例后统一释放，防止会话（WS/xterm/计时器）泄漏到下个用例。
  terminalSessionManager.disposeAll()
})
afterAll(() => server.close())
