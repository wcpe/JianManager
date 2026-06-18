import { create } from 'zustand'

interface AuthState {
  accessToken: string | null
  refreshToken: string | null
  isAuthenticated: boolean
  login: (accessToken: string, refreshToken: string) => void
  logout: () => void
  loadFromStorage: () => void
}

// 同步从 localStorage 读取初始鉴权状态，使首帧渲染即正确（BUG-006）。
// 否则 AuthGuard 在 loadFromStorage 副作用执行前会把已登录用户弹回 /login。
const storedAccess = localStorage.getItem('accessToken')
const storedRefresh = localStorage.getItem('refreshToken')

export const useAuthStore = create<AuthState>((set) => ({
  accessToken: storedAccess,
  refreshToken: storedRefresh,
  isAuthenticated: !!storedAccess,

  login: (accessToken, refreshToken) => {
    localStorage.setItem('accessToken', accessToken)
    localStorage.setItem('refreshToken', refreshToken)
    set({ accessToken, refreshToken, isAuthenticated: true })
  },

  logout: () => {
    localStorage.removeItem('accessToken')
    localStorage.removeItem('refreshToken')
    set({ accessToken: null, refreshToken: null, isAuthenticated: false })
  },

  loadFromStorage: () => {
    const accessToken = localStorage.getItem('accessToken')
    const refreshToken = localStorage.getItem('refreshToken')
    set({
      accessToken,
      refreshToken,
      isAuthenticated: !!accessToken,
    })
  },
}))
