import { describe, it, expect, vi, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import api, { ensureFreshToken } from './client'
import { useAuthStore } from '@/stores/auth'

function loginWithExpiredToken(refreshToken: string): string {
  const payload = btoa(JSON.stringify({ exp: Math.floor(Date.now() / 1000) - 60 }))
  const accessToken = `mock.${payload}.sig`
  useAuthStore.getState().login(accessToken, refreshToken)
  return accessToken
}

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

describe('refresh Promise 并发与失败恢复', () => {
  it('并发刷新共享同一次请求并同步鉴权状态', async () => {
    loginWithExpiredToken('refresh-concurrent')
    let refreshCount = 0
    server.use(
      http.post(API('/auth/refresh'), () => {
        refreshCount += 1
        return HttpResponse.json({ accessToken: 'access-refreshed', refreshToken: 'refresh-refreshed' })
      }),
    )

    const tokens = await Promise.all([ensureFreshToken(), ensureFreshToken()])

    expect(tokens).toEqual(['access-refreshed', 'access-refreshed'])
    expect(refreshCount).toBe(1)
    expect(useAuthStore.getState()).toMatchObject({
      accessToken: 'access-refreshed',
      refreshToken: 'refresh-refreshed',
    })
  })

  it('刷新拒绝被调用方消费，不产生 unhandledRejection', async () => {
    const expiredToken = loginWithExpiredToken('refresh-rejected')
    const unhandledReasons: unknown[] = []
    const onUnhandledRejection = (reason: unknown) => unhandledReasons.push(reason)
    process.on('unhandledRejection', onUnhandledRejection)

    try {
      await expect(ensureFreshToken()).resolves.toBe(expiredToken)
      await new Promise((resolve) => setTimeout(resolve, 0))
      expect(unhandledReasons).toEqual([])
    } finally {
      process.off('unhandledRejection', onUnhandledRejection)
    }
  })

  it('刷新失败释放共享状态，后续调用可以重新刷新', async () => {
    const expiredToken = loginWithExpiredToken('refresh-retry')
    let refreshCount = 0
    server.use(
      http.post(API('/auth/refresh'), () => {
        refreshCount += 1
        if (refreshCount === 1) {
          return HttpResponse.json({ message: '刷新失败' }, { status: 500 })
        }
        return HttpResponse.json({ accessToken: 'access-retried', refreshToken: 'refresh-retried' })
      }),
    )

    await expect(ensureFreshToken()).resolves.toBe(expiredToken)
    await expect(ensureFreshToken()).resolves.toBe('access-retried')
    expect(refreshCount).toBe(2)
  })
})
