import { describe, it, expect, vi, beforeEach } from 'vitest'
import api from './client'
import { useAuthStore } from '@/stores/auth'

/**
 * 登录失败整页刷新 bug 的回归（本会话原始诉求）。
 * 根因：响应 401 拦截器对**所有** 401 统一「刷 token 失败→clearAuth+`window.location.href='/login'`」，
 * 把 `/auth/login` 自身的 401（凭据错误）误当「会话过期」→整页跳转把错误提示冲掉。
 * 修复：豁免 `/auth/*` 端点的 401 自动刷新+跳转，原样抛回给调用方展示错误。
 * （修复只新增 `!isAuthEndpoint` 条件，仅影响 url 含 `/auth/` 的请求；受保护端点的会话过期兜底
 *  逻辑完全不变，由全量套件大量命中受保护端点的用例保障，不在此重复守卫。）
 */
describe('client 401 拦截器 — 登录失败不触发会话过期处理（登录刷页 bug 回归）', () => {
  beforeEach(() => {
    localStorage.clear()
    useAuthStore.getState().logout()
  })

  it('POST /auth/login 401（错误凭据）不清鉴权、不跳转，原样抛回 401', async () => {
    const logoutSpy = vi.spyOn(useAuthStore.getState(), 'logout')
    const pathBefore = window.location.pathname

    await expect(
      api.post('/auth/login', { username: 'admin', password: 'wrong-password' }),
    ).rejects.toMatchObject({ response: { status: 401 } })

    // 修复前：误当会话过期 → clearAuth(logout) + window.location.href='/login' 整页跳转
    expect(logoutSpy).not.toHaveBeenCalled()
    expect(window.location.pathname).toBe(pathBefore)
    logoutSpy.mockRestore()
  })
})
