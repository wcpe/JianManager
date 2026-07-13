import '@testing-library/jest-dom/vitest'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { cleanup } from '@testing-library/react'
import { server } from '@jianmanager/devmock/server'
import { resetDb } from '@jianmanager/devmock/db'
import { clearInjections } from '@jianmanager/devmock/inject'
import { useAuthStore } from '@/stores/auth'
import type { TerminalSessionManager } from '@/lib/terminal-session-manager'

/**
 * jsdom Blob 缺 `stream()` 的最小 polyfill（CI Node 20 真机踩坑）：
 * msw 的 XHR 拦截器会把 blob 响应体（如审计导出 responseType:'blob'）交给 undici 的
 * `new Response()`；undici `extractBody` 见对象带 `arrayBuffer` 且 toStringTag=Blob 即按
 * blob-like 调 `object.stream()`——jsdom 的 Blob 无此方法，直接抛 Unhandled Rejection
 * 使 vitest 全绿仍以错误退出（本地 Node 24 的 undici 路径不受影响，故仅 CI 复现）。
 */
if (typeof Blob !== 'undefined' && typeof Blob.prototype.stream !== 'function') {
  Object.defineProperty(Blob.prototype, 'stream', {
    configurable: true,
    writable: true,
    value(this: Blob) {
      // ReadableStream 类型来自 DOM lib、运行时实现来自 Node 18+ 全局，两侧免依赖；
      // start 用箭头函数捕获方法的 this（Blob），避免 no-this-alias。
      return new ReadableStream<Uint8Array>({
        start: async (controller) => {
          controller.enqueue(new Uint8Array(await this.arrayBuffer()))
          controller.close()
        },
      })
    },
  })
}

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
