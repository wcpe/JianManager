import { create } from 'zustand'
import { decodeJwt } from '@/lib/jwt'

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
  },

  logout: () => {
    localStorage.removeItem('accessToken')
    localStorage.removeItem('refreshToken')
    set({ accessToken: null, refreshToken: null, isAuthenticated: false, role: null, username: null })
    // 登出即整体释放全部终端会话（FR-295/296，ADR-067）：连接常驻管理器不随组件卸载断开，
    // 必须在此统一 dispose 防孤儿 WS。动态 import 避免把 xterm 卷进首屏 chunk。
    void import('@/lib/terminal-session-manager').then((m) => m.terminalSessionManager.disposeAll())
  },

  loadFromStorage: () => {
    const accessToken = localStorage.getItem('accessToken')
    const refreshToken = localStorage.getItem('refreshToken')
    const claims = decodeJwt(accessToken)
    set({
      accessToken,
      refreshToken,
      isAuthenticated: !!accessToken,
      role: claims?.role ?? null,
      username: claims?.username ?? null,
    })
  },
}))
