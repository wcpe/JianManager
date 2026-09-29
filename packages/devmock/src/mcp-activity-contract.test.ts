import { describe, it, expect, beforeAll, afterAll, afterEach } from 'vitest'
import { server } from '@jianmanager/devmock/server'
import { resetDb } from '@jianmanager/devmock/db'
import type { McpActivityResponse } from '@jianmanager/devmock/handlers/domains/mcp'

/**
 * MCP 活动视图（FR-391 / ADR-096）**经 mock handler** 的契约测试。
 *
 * 存在的理由：devmock 此前**完全没有**该端点的 handler——mock 模式下页面直接是错误态，
 * 且前端把档位发成 `7d`（Go duration 不支持「天」）时 mock 毫无反应，缺陷只能靠真机验收发现。
 * 补 handler 的价值全在「复刻真后端的 window 校验语义」：非法形态与越界区间必须回 400，
 * 否则 mock 模式下的 e2e 只是「随便答应」，守不住任何东西。
 * 校验矩阵对齐 internal/controlplane/mcp/handler.go 的 ListActivity
 * 与 internal/controlplane/router/mcp_test.go 的 TestMCP_Activity_WindowValidation。
 */

const BASE = 'http://localhost/api/v1'

async function login(username: string, password: string): Promise<string> {
  const r = await fetch(`${BASE}/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  expect(r.status).toBe(200)
  return ((await r.json()) as { accessToken: string }).accessToken
}

function activity(token: string | null, window?: string): Promise<Response> {
  const url =
    window === undefined
      ? `${BASE}/agent/mcp/activity`
      : `${BASE}/agent/mcp/activity?window=${encodeURIComponent(window)}`
  return fetch(url, { headers: token ? { Authorization: `Bearer ${token}` } : {} })
}

async function activityBody(token: string, window?: string): Promise<McpActivityResponse> {
  const r = await activity(token, window)
  expect(r.status).toBe(200)
  return (await r.json()) as McpActivityResponse
}

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => {
  server.resetHandlers()
  resetDb()
})
afterAll(() => server.close())

describe('MCP 活动 mock（GET /agent/mcp/activity）', () => {
  it('24h → 200：window 回显 Go duration 形态，items 按最近活动降序', async () => {
    const token = await login('admin', 'admin123')
    const body = await activityBody(token, '24h')

    // 真后端回显的是解析后生效窗口的 String() 形态，不是入参原样。
    expect(body.window).toBe('24h0m0s')
    expect(body.generatedAt).toBeTruthy()
    expect(body.items.length).toBeGreaterThanOrEqual(2)

    const times = body.items.map((it) => new Date(it.lastActivityAt).getTime())
    expect(times).toEqual([...times].sort((a, b) => b - a))

    for (const item of body.items) {
      expect(item.tokenName).toBeTruthy()
      expect(item.tokenPrefix).toMatch(/^jmat_/)
      expect(item.callCount).toBeGreaterThan(0)
      expect(item.failureCount).toBeGreaterThanOrEqual(0)
      expect(item.clientIPs.length).toBeGreaterThan(0)
      expect(Object.keys(item.clients).length).toBeGreaterThan(0)
    }
  })

  it('缺省 window 按 24h 生效', async () => {
    const token = await login('admin', 'admin123')
    expect((await activityBody(token)).window).toBe('24h0m0s')
  })

  it('非法 duration 形态 → 400（Go duration 没有「天」）', async () => {
    const token = await login('admin', 'admin123')
    for (const bad of ['7d', '24', 'abc', '1d', '500ms', '-1h']) {
      const r = await activity(token, bad)
      expect(r.status, `window=${bad} 应回 400`).toBe(400)
      expect((await r.json()) as { error: string }).toMatchObject({ error: 'BAD_REQUEST' })
    }
  })

  it('解析成功但越界 → 400（闭区间 1h~168h）', async () => {
    const token = await login('admin', 'admin123')
    for (const bad of ['10m', '59m', '169h', '0']) {
      const r = await activity(token, bad)
      expect(r.status, `window=${bad} 应回 400`).toBe(400)
      expect((await r.json()) as { message: string }).toMatchObject({
        message: 'window 允许区间 1h~168h',
      })
    }
  })

  it('边界与组合形态合法 → 200', async () => {
    const token = await login('admin', 'admin123')
    for (const [ok, echo] of [
      ['1h', '1h0m0s'],
      ['168h', '168h0m0s'],
      ['90m', '1h30m0s'],
    ]) {
      expect((await activityBody(token, ok)).window, `window=${ok} 应被接受`).toBe(echo)
    }
  })

  it('窗口真的参与聚合：168h 能看到 24h 之外的更早活动', async () => {
    const token = await login('admin', 'admin123')
    const short = await activityBody(token, '24h')
    const long = await activityBody(token, '168h')

    expect(long.items.length).toBeGreaterThan(short.items.length)
    // 窗口外的行不得出现在短窗口里，且长窗口的第一行同为最近活跃的 Token。
    expect(short.items[0].tokenId).toBe(long.items[0].tokenId)
  })

  it('未登录 → 401；非平台管理员 → 403', async () => {
    expect((await activity(null, '24h')).status).toBe(401)
    const opToken = await login('operator', 'op123456')
    expect((await activity(opToken, '24h')).status).toBe(403)
  })
})
