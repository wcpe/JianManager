import axios from 'axios'
import { isTokenExpired } from '@/lib/jwt'

const api = axios.create({
  baseURL: '/api/v1',
  timeout: 10000,
})

/**
 * 共享的刷新 promise：并发请求只触发一次 /auth/refresh，避免重复刷新产生竞态（与后端行锁互补）。
 * 请求前主动刷新（BUG-008）与响应 401 被动刷新复用同一把闸。
 */
let refreshPromise: Promise<string> | null = null

/** 用 refreshToken 换取新的 access/refresh 并落库，返回新的 accessToken。失败时抛错。 */
function refreshTokens(): Promise<string> {
  if (refreshPromise) return refreshPromise
  refreshPromise = (async () => {
    const refreshToken = localStorage.getItem('refreshToken')
    if (!refreshToken) {
      throw new Error('no refresh token')
    }
    const { data } = await axios.post('/api/v1/auth/refresh', { refreshToken })
    localStorage.setItem('accessToken', data.accessToken)
    localStorage.setItem('refreshToken', data.refreshToken)
    return data.accessToken as string
  })()
  // 无论成败都释放闸，便于下次重新刷新。
  refreshPromise.finally(() => {
    refreshPromise = null
  })
  return refreshPromise
}

// 请求拦截：附加 token；若 access token 已过期且有 refresh token，先主动刷新再发，
// 避免登录态下加载期出现一条无谓的 401（BUG-008）。刷新失败时不阻断本次请求——
// 让其照常发出并由响应 401 拦截器统一处理（跳登录），避免请求层吞掉错误。
api.interceptors.request.use(async (config) => {
  let token = localStorage.getItem('accessToken')
  if (token && isTokenExpired(token) && localStorage.getItem('refreshToken')) {
    try {
      token = await refreshTokens()
    } catch {
      // 刷新失败：保留原（过期）token 发出，401 拦截器会接管跳登录。
    }
  }
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

// 响应拦截：401 自动刷新 token（兜底未被请求前刷新覆盖的情形：token 提前失效/服务端撤销等）
api.interceptors.response.use(
  (response) => response,
  async (error) => {
    const originalRequest = error.config

    if (error.response?.status === 401 && !originalRequest._retry) {
      originalRequest._retry = true

      try {
        const accessToken = await refreshTokens()
        originalRequest.headers.Authorization = `Bearer ${accessToken}`
        return api(originalRequest)
      } catch {
        clearAuth()
        window.location.href = '/login'
        return Promise.reject(error)
      }
    }

    return Promise.reject(error)
  },
)

function clearAuth() {
  localStorage.removeItem('accessToken')
  localStorage.removeItem('refreshToken')
}

/**
 * 返回一个可用（未过期）的 accessToken：若当前 token 已过期且有 refreshToken 则先刷新。
 * 供绕过 axios 拦截器的原生 fetch 流（如 SSE）在连接前调用，避免加载期无谓 401（BUG-008）。
 * 无 token 或刷新失败时返回当前（可能为 null/过期）token，交由调用方与后端兜底。
 */
export async function ensureFreshToken(): Promise<string | null> {
  const token = localStorage.getItem('accessToken')
  if (token && isTokenExpired(token) && localStorage.getItem('refreshToken')) {
    try {
      return await refreshTokens()
    } catch {
      return token
    }
  }
  return token
}

export default api
