import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { queryClient } from '@/lib/query-client'

vi.mock('@/stores/permissions', () => ({
  usePermissionsStore: { getState: () => ({ loadFromAuthMe: vi.fn(), reset: vi.fn() }) },
}))
vi.mock('@/lib/terminal-session-manager', () => ({
  terminalSessionManager: { disposeAll: vi.fn() },
}))

import { useAuthStore } from './auth'

function tokenFor(userId: number, username: string): string {
  const payload = btoa(JSON.stringify({ userId, username, role: 0, exp: Math.floor(Date.now() / 1000) + 900 }))
  return `mock.${payload}.sig`
}

describe('auth store 查询缓存隔离', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  beforeEach(() => {
    const values = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => values.set(key, value),
      removeItem: (key: string) => values.delete(key),
      clear: () => values.clear(),
    })
    localStorage.clear()
    queryClient.clear()
    useAuthStore.setState({
      accessToken: null,
      refreshToken: null,
      isAuthenticated: false,
      role: null,
      username: null,
    })
  })

  it('登录切换主体时清空旧主体缓存', () => {
    useAuthStore.getState().login(tokenFor(1, 'alice'), 'refresh-a')
    queryClient.setQueryData(['private', 'alice'], { value: 'alice-only' })
    useAuthStore.getState().login(tokenFor(2, 'bob'), 'refresh-b')
    expect(queryClient.getQueryData(['private', 'alice'])).toBeUndefined()
  })

  it('登出时清空当前主体缓存', () => {
    useAuthStore.getState().login(tokenFor(1, 'alice'), 'refresh-a')
    queryClient.setQueryData(['private', 'alice'], { value: 'alice-only' })
    useAuthStore.getState().logout()
    expect(queryClient.getQueryData(['private', 'alice'])).toBeUndefined()
  })
})
