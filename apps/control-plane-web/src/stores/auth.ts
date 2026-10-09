import { create } from 'zustand'
import { decodeJwt } from '@/lib/auth/jwt'
import { queryClient } from '@/lib/query-client'

/** 登录/恢复后异步拉取 /auth/me，填充权限 store（失败由 store 自身回落）。 */
function schedulePermissionsLoad(): void {
  void trackDynamicImport(loadPermissionsModule()).then((m) => {
    void m.usePermissionsStore.getState().loadFromAuthMe()
  })
}

/** 权限 store 的动态 import 入口。 */
export function loadPermissionsModule() {
  return import('@/stores/permissions')
}

/** 终端会话管理器的动态 import 入口（理由同上：xterm 不进首屏 chunk，但要可等待）。 */
export function loadTerminalSessionManager() {
  return import('@/lib/console/terminal-session-manager')
}

/**
 * 在途动态 import 的登记表：`logout`/`schedulePermissionsLoad` 每次发起都登记其 Promise，
 * 由 `flushAuthDynamicImports` 统一收口。
 *
 * 为什么用登记表而不是「flush 时重新 import 一次」：重新 import 在**已加载**时虽命中
 * ES 模块缓存，但首次仍要发起一次新的解析/加载（尤其 `terminal-session-manager` 会把
 * 真 xterm 拉进模块图），在测试拆卸阶段引入额外 tick —— 本文件 setup.ts ② 条注释
 * 明确规避的正是这类污染。登记表只等**已经发起**的那些，不新增加载。
 */
const inFlightDynamicImports = new Set<Promise<unknown>>()

function trackDynamicImport<T>(promise: Promise<T>): Promise<T> {
  const tracked: Promise<unknown> = promise.then(
    () => undefined,
    () => undefined, // 加载失败不该让拆卸阶段抛错（该路径本就有失败回落语义）
  )
  inFlightDynamicImports.add(tracked)
  void tracked.finally(() => inFlightDynamicImports.delete(tracked))
  return promise
}

/**
 * 等待本模块**已经发起**的全部动态 import 落地（幂等；无在途时为 no-op）。
 *
 * 供测试 setup 的 afterEach 调用：`logout()` 保持同步（生产调用点无需等待），
 * 但测试必须知道「在途 import 已结束」才能安全拆除环境，否则 node 报
 * `EnvironmentTeardownError: Cannot load '.../axios/index.js' ... after the environment
 * was torn down` 的 unhandled rejection —— **全部用例都通过**的轮次仍以非零码退出
 * （2026-10-12 主干 CI 实测：319 files / 2505 tests 全 passed 却判失败）。
 */
export function flushAuthDynamicImports(): Promise<void> {
  if (inFlightDynamicImports.size === 0) return Promise.resolve()
  return Promise.all([...inFlightDynamicImports]).then(() => undefined)
}

interface AuthState {
  accessToken: string | null
  refreshToken: string | null
  isAuthenticated: boolean
  /** 当前用户角色（从 access token 解码）：0=组成员 1=组管理员 10=平台管理员，未登录为 null。 */
  role: number | null
  /** 当前用户名（从 access token 解码），用于 UI 展示。 */
  username: string | null
  login: (accessToken: string, refreshToken: string) => void
  logout: () => void
  loadFromStorage: () => void
}

// 同步从 localStorage 读取初始鉴权状态，使首帧渲染即正确（BUG-006）。
// 否则 AuthGuard 在 loadFromStorage 副作用执行前会把已登录用户弹回 /login。
// typeof 守卫：非 DOM 上下文（vitest node 环境/SSR）无 localStorage，避免模块加载即崩。
const hasStorage = typeof localStorage !== 'undefined'
const storedAccess = hasStorage ? localStorage.getItem('accessToken') : null
const storedRefresh = hasStorage ? localStorage.getItem('refreshToken') : null
const storedClaims = decodeJwt(storedAccess)

export const useAuthStore = create<AuthState>((set) => ({
  accessToken: storedAccess,
  refreshToken: storedRefresh,
  isAuthenticated: !!storedAccess,
  role: storedClaims?.role ?? null,
  username: storedClaims?.username ?? null,

  login: (accessToken, refreshToken) => {
    queryClient.clear()
    localStorage.setItem('accessToken', accessToken)
    localStorage.setItem('refreshToken', refreshToken)
    const claims = decodeJwt(accessToken)
    set({
      accessToken,
      refreshToken,
      isAuthenticated: true,
      role: claims?.role ?? null,
      username: claims?.username ?? null,
    })
    schedulePermissionsLoad()
  },

  logout: () => {
    queryClient.clear()
    localStorage.removeItem('accessToken')
    localStorage.removeItem('refreshToken')
    set({ accessToken: null, refreshToken: null, isAuthenticated: false, role: null, username: null })
    // 两处都经 trackDynamicImport 登记（**不是**裸 `void import(...)`）：保持动态 import
    // 语义不变，但让测试能经 flushAuthDynamicImports 等待在途加载收口（见其文档）。
    void trackDynamicImport(loadPermissionsModule()).then((m) => m.usePermissionsStore.getState().reset())
    // 登出即整体释放全部终端会话（FR-295/296，ADR-067）：连接常驻管理器不随组件卸载断开，
    // 必须在此统一 dispose 防孤儿 WS。动态 import 避免把会话管理器卷进首屏 chunk。
    void trackDynamicImport(loadTerminalSessionManager()).then((m) => m.terminalSessionManager.disposeAll())
  },

  loadFromStorage: () => {
    const accessToken = localStorage.getItem('accessToken')
    const refreshToken = localStorage.getItem('refreshToken')
    const claims = decodeJwt(accessToken)
    const authenticated = !!accessToken
    set({
      accessToken,
      refreshToken,
      isAuthenticated: authenticated,
      role: claims?.role ?? null,
      username: claims?.username ?? null,
    })
    if (authenticated) schedulePermissionsLoad()
  },
}))
