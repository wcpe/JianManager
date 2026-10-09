import '@testing-library/jest-dom/vitest'
import { webcrypto } from 'node:crypto'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { cleanup } from '@testing-library/react'
import { server } from '@jianmanager/devmock/server'
import { resetDb } from '@jianmanager/devmock/db'
import { clearInjections } from '@jianmanager/devmock/inject'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import type { TerminalSessionManager } from '@/lib/terminal-session-manager'

/**
 * jsdom Blob 缺 `stream()` 的最小 polyfill（CI Node 20 真机踩坑）：
 * msw 的 XHR 拦截器会把 blob 响应体（如审计导出 responseType:'blob'）交给 undici 的
 * `new Response()`；undici `extractBody` 见对象带 `arrayBuffer` 且 toStringTag=Blob 即按
 * blob-like 调 `object.stream()`——jsdom 的 Blob 无此方法，直接抛 Unhandled Rejection
 * 使 vitest 全绿仍以错误退出（本地 Node 24 的 undici 路径不受影响，故仅 CI 复现）。
 */
/**
 * jsdom Crypto 缺 `subtle` 的最小 polyfill（FR-346）：发布页上传前用
 * `crypto.subtle.digest('SHA-256', …)` 算秒传预查 hash，jsdom 的 window.crypto 只有
 * getRandomValues——桥接 Node 内建 WebCrypto 的 subtle（与浏览器同规范实现）。
 */
if (globalThis.crypto && !globalThis.crypto.subtle) {
  Object.defineProperty(globalThis.crypto, 'subtle', {
    configurable: true,
    value: webcrypto.subtle,
  })
}

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
 * jsdom 缺 `Range.getClientRects` / `getBoundingClientRect`，CodeMirror 6 的坐标测量会异步抛
 * 「textRange(...).getClientRects is not a function」，表现为测试文件在收集阶段即失败。
 * 原先 explorer / config-explorer 的三个测试文件各自在文件内打桩；业务视图回迁后，
 * BotsPage / DatabasePage / NodesPage 等渲染树同样含 CodeMirror，故提升到全局统一垫片。
 * 返回零矩形即可——相关断言不依赖布局几何。
 */
if (typeof Range !== 'undefined') {
  const emptyRects = () =>
    ({ length: 0, item: () => null, [Symbol.iterator]: function* () {} }) as unknown as DOMRectList
  const emptyRect = () =>
    ({ x: 0, y: 0, width: 0, height: 0, top: 0, left: 0, right: 0, bottom: 0, toJSON: () => ({}) }) as DOMRect
  Range.prototype.getClientRects = emptyRects
  Range.prototype.getBoundingClientRect = emptyRect
}

/**
 * jsdom 组件 / 页面测试的全局 setup（FR-196，vitest dom project）。
 * onUnhandledRequest:'error' 是有意的覆盖闸：未 mock 的请求即让测试失败，逼域簇补齐 handler。
 * 每例后卸载 DOM + 重置 handler 覆盖 + 假后端 + 注入 + 鉴权态（localStorage / store），保证用例隔离
 * （否则成功登录用例写入的 token 会泄漏到下个用例，使 LoginPage 误判已登录而重定向）。
 */

/**
 * 终端会话常驻单例管理器（FR-295，ADR-067），在 beforeAll 里动态 import 缓存到此，缘由有二：
 * ① **动态 import**——晚于各测试文件的 `vi.mock` 注册与全局桩安装，import 拿到的是这些桩生效后的模块；
 *    静态顶层 import 会抢在它们之前把模块绑进缓存。
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
  usePermissionsStore.getState().reset()
  // 每例后统一释放终端会话，防止会话（WS/xterm/计时器）泄漏到下个用例。
  terminalSessionManager?.disposeAll()
})
afterAll(() => server.close())
