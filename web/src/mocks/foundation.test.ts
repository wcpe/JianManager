import { describe, it, expect, beforeAll, afterAll, afterEach } from 'vitest'
import { server } from './server'
import { handlers } from './handlers'
import { resetDb } from './db'
import { clearInjections, mockInject } from './inject'
import { requireAuth } from './auth-middleware'

/**
 * 地基整合测试（node project）：经 MSW server 验证 auth 联动 + 错误注入 + 鉴权中间件 + 终端 token。
 * 用原生 fetch（不经 axios 拦截器），直接验 mock handler 契约。
 */
const BASE = 'http://localhost/api/v1'

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => {
  server.resetHandlers()
  resetDb()
  clearInjections()
})
afterAll(() => server.close())

function login(username: string, password: string) {
  return fetch(`${BASE}/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })
}

describe('假后端 auth 纵切（FR-197/198/199 地基）', () => {
  it('错误凭据 → 401', async () => {
    const r = await login('admin', 'wrong')
    expect(r.status).toBe(401)
  })

  it('正确凭据 → 发 token；token 过 requireAuth，无 token 401', async () => {
    const r = await login('admin', 'admin123')
    expect(r.status).toBe(200)
    const body = (await r.json()) as { accessToken: string }
    expect(body.accessToken).toMatch(/^mock\./)

    const ok = requireAuth({
      request: new Request(`${BASE}/protected`, { headers: { Authorization: `Bearer ${body.accessToken}` } }),
    })
    expect(ok).toBeNull()

    const denied = requireAuth({ request: new Request(`${BASE}/protected`) })
    expect(denied?.status).toBe(401)
  })

  it('注入 500 → 登录返回 500（成功默认 + 按需注入）', async () => {
    mockInject('post', '/auth/login', { kind: 'status', status: 500 })
    const r = await login('admin', 'admin123')
    expect(r.status).toBe(500)
  })

  it('注入清除后恢复成功（用例隔离）', async () => {
    const r = await login('admin', 'admin123')
    expect(r.status).toBe(200)
  })

  it('终端 token handler 返回 mock wsUrl（FR-198）', async () => {
    const r = await fetch(`${BASE}/instances/1/terminal-token?permission=write`)
    expect(r.status).toBe(200)
    const body = (await r.json()) as { wsUrl: string }
    expect(body.wsUrl).toContain('_mock/terminal')
  })

  it('实例事件 SSE handler 排在实例域 /instances/:id 之前（防贪婪遮蔽 SSE，真机回归）', () => {
    // 真机发现：实例域 `/instances/:id` 会把 `events` 当 id 贪婪匹配，把 `/instances/events` SSE 流 404 遮蔽。
    // MSW 取首个匹配，故字面路径 events 必须排在参数路径 :id 之前。（SSE 流式响应在 node fetch 下会挂住、
    // 无法直接断言，改测 handler 注册顺序——确定性捕获该遮蔽回归。）
    const paths = handlers.map((h) => (h as { info?: { path?: string } }).info?.path ?? '')
    const eventsIdx = paths.findIndex((p) => p.endsWith('/instances/events'))
    const byIdIdx = paths.findIndex((p) => p.endsWith('/instances/:id'))
    expect(eventsIdx).toBeGreaterThanOrEqual(0)
    expect(byIdIdx).toBeGreaterThanOrEqual(0)
    expect(eventsIdx).toBeLessThan(byIdIdx)
  })
})
