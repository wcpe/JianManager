import '@testing-library/jest-dom/vitest'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { cleanup } from '@testing-library/react'
import { server } from '@/mocks/server'
import { resetDb } from '@/mocks/db'
import { clearInjections } from '@/mocks/inject'
import { useAuthStore } from '@/stores/auth'
import type { TerminalSessionManager } from '@/lib/terminal-session-manager'

/**
 * jsdom 组件 / 页面测试的全局 setup（FR-196，vitest dom project）。
 * onUnhandledRequest:'error' 是有意的覆盖闸：未 mock 的请求即让测试失败，逼域簇补齐 handler。
 * 每例后卸载 DOM + 重置 handler 覆盖 + 假后端 + 注入 + 鉴权态（localStorage / store），保证用例隔离
 * （否则成功登录用例写入的 token 会泄漏到下个用例，使 LoginPage 误判已登录而重定向）。
 */

/**
 * 终端会话常驻单例管理器（FR-295，ADR-067），在 beforeAll 里动态 import 缓存到此，缘由有二：
 * ① **动态 import**——静态顶层 import 会在测试文件的 `vi.mock('@xterm/xterm')` 生效前把真 xterm
 *    绑进管理器模块缓存，使各测试文件的 xterm mock 失效；`beforeAll` 晚于 vi.mock 注册，import 得到 mock。
 * ② **缓存供 afterEach 同步调用**——afterEach 内若 `await import(...)` 会多出一个拆卸 tick，
 *    让在途查询（如 `/nodes`）在鉴权态已清后走到刷新令牌失败路径、抛出未处理 rejection 污染无关用例。
 */
let terminalSessionManager: TerminalSessionManager | null = null

beforeAll(async () => {
  server.listen({ onUnhandledRequest: 'error' })
  ;({ terminalSessionManager } = await import('@/lib/terminal-session-manager'))
})
afterEach(() => {
  cleanup()
  server.resetHandlers()
  resetDb()
  clearInjections()
  localStorage.clear()
  useAuthStore.getState().logout()
  // 每例后统一释放终端会话，防止会话（WS/xterm/计时器）泄漏到下个用例。
  terminalSessionManager?.disposeAll()
})
afterAll(() => server.close())
