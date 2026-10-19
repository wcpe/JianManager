import { create } from 'zustand'
import { decodeJwt } from '@/lib/jwt'
import { queryClient } from '@/lib/query-client'

/** 登录/恢复后异步拉取 /auth/me，填充权限 store（失败由 store 自身回落）。 */
function schedulePermissionsLoad(): void {
  // 续体经 trackDynamicImport 一并登记（不再用 `.then` 挂在外部），
  // 否则「loadFromAuthMe 内部再取模块」这段加载在拆卸时无法被等待。
  void trackDynamicImport(loadPermissionsModule(), (m) => {
    void m.usePermissionsStore.getState().loadFromAuthMe()
  })
}

/** 权限 store 的动态 import 入口。 */
export function loadPermissionsModule() {
  return import('@/stores/permissions')
}

/** 终端会话管理器的动态 import 入口（理由同上：xterm 不进首屏 chunk，但要可等待）。 */
export function loadTerminalSessionManager() {
  return import('@/lib/terminal-session-manager')
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

/**
 * 登记一次动态 import **及其后续动作**，使 `flushAuthDynamicImports` 能等到真正安静。
 *
 * 为什么要把 `then` 续体也算进来（2026-10-19 修）：原先只跟踪 `import()` 本身，于是
 * `import().then((m) => ...)` 里的续体在 flush 眼中是「已经结束」的——若该续体又触发了
 * 新的加载（例如权限 store 初始化过程中再取模块），这段加载完全不在登记表里，环境拆除时
 * 就会以 `Cannot load axios ... after the environment was torn down` 的形式漏成
 * unhandled rejection（CI 实测：2516 个用例全 passed 仍判失败）。
 * 现在把续体并进同一个 promise，flush 一定等到「加载 + 续体」整体落地。
 *
 * 续体抛错按加载失败处理（该路径本就有失败回落语义），不让拆卸阶段因它抛错。
 */
// 导出仅为测试：生产代码一律经 logout / schedulePermissionsLoad 间接触发。
// 测试需要直接验证「续体是否被纳管」，否则只能靠时序竞态去猜（评审 M-5 的教训）。
export function trackDynamicImport<T, R>(
  promise: Promise<T>,
  then?: (module: T) => R | Promise<R>,
): Promise<void> {
  const tracked: Promise<unknown> = promise.then(
    (module) => (then ? then(module) : undefined),
    () => undefined, // 加载失败不该让拆卸阶段抛错（该路径本就有失败回落语义）
  )
  inFlightDynamicImports.add(tracked)
  void tracked.finally(() => inFlightDynamicImports.delete(tracked))
  return tracked.then(() => undefined)
}

/**
 * 等待本模块**已经发起**的全部动态 import（含其续体）落地（幂等；无在途时为 no-op）。
 *
 * 供测试 setup 的 afterEach 调用：`logout()` 保持同步（生产调用点无需等待），
 * 但测试必须知道「在途 import 已结束」才能安全拆除环境，否则 node 报
 * `EnvironmentTeardownError: Cannot load '.../axios/index.js' ... after the environment
 * was torn down` 的 unhandled rejection —— **全部用例都通过**的轮次仍以非零码退出
 * （2026-10-12 主干 CI 实测：319 files / 2505 tests 全 passed 却判失败；
 * 2026-10-19 再次复现：2516 tests 全 passed、320 files 全 passed，仍判失败）。
 *
 * 为什么要**循环**而不是只等一次快照：续体（`.then`）本身可能再发起新的动态加载，
 * 单次 `Promise.all` 只覆盖调用瞬间的那批。这里每轮先排空微任务再判断，直到登记表
 * 连续为空才返回；上限 10 轮是防御性封顶（正常路径 1～2 轮即返回，不会拖慢拆卸）。
 */
export async function flushAuthDynamicImports(): Promise<void> {
  for (let round = 0; round < 10; round += 1) {
    if (inFlightDynamicImports.size === 0) {
      // 让已排队的续体/微任务先跑完，再判断是否真的安静：
      // 续体可能在 await 点之后才发起下一段加载，只看同步时刻会漏。
      await Promise.resolve()
      await Promise.resolve()
      if (inFlightDynamicImports.size === 0) return
    }
    await Promise.allSettled([...inFlightDynamicImports])
  }
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
    void trackDynamicImport(loadPermissionsModule(), (m) => m.usePermissionsStore.getState().reset())
    // 登出即整体释放全部终端会话（FR-295/296，ADR-067）：连接常驻管理器不随组件卸载断开，
    // 必须在此统一 dispose 防孤儿 WS。动态 import 避免把 xterm 卷进首屏 chunk。
    void trackDynamicImport(loadTerminalSessionManager(), (m) => m.terminalSessionManager.disposeAll())
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
