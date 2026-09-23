import { describe, it, expect, beforeAll, afterAll, afterEach } from 'vitest'
import { server } from '@jianmanager/devmock/server'
import { resetDb } from '@jianmanager/devmock/db'

/**
 * 平台全景观测（FR-402 / FR-461）**经 mock handler** 的契约测试。
 *
 * 存在的理由：这两个端点是平台首页与总览页的读模型。此前 devmock **完全没有**对应 handler——
 * 前端请求不被 MSW 拦截、透传 vite proxy 后报 ECONNREFUSED，E2E（navigation-benchmark /
 * metrics-probe）随之失败。补 handler 时又踩过一次坑：mock 的 collection 只有
 * `list(pred?)`/`find(pred)`，**没有 `filter()`**，写成 `db(...).filter(...)` 会在请求时抛错。
 * 本文件一律打真 mock 路由并断言结构，杜绝「handler 写了但运行时崩」这类静默损坏。
 */

const BASE = 'http://localhost/api/v1'

async function login(): Promise<string> {
  const r = await fetch(`${BASE}/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: 'admin', password: 'admin123' }),
  })
  expect(r.status).toBe(200)
  return ((await r.json()) as { accessToken: string }).accessToken
}

/** 健康墙单格（对齐前端 HealthWallNode 的关键字段）。 */
interface WallNode {
  nodeId: number
  nodeUuid: string
  name: string
  freshness: string
  cpuPct: number | null
  memPct: number | null
  diskPct: number | null
  running: number
  crashed: number
  stopped: number
  level: string
  href: string
}

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => {
  server.resetHandlers()
  resetDb()
})
afterAll(() => server.close())

describe('平台全景观测 overview（FR-402）', () => {
  it('返回健康/资源/Bot/告警/任务/异常六大段且数值来自种子集合', async () => {
    const token = await login()
    const r = await fetch(`${BASE}/observability/overview`, { headers: { Authorization: `Bearer ${token}` } })
    expect(r.status).toBe(200)
    const body = (await r.json()) as {
      sampledAt: string | null
      health: Record<string, number>
      resources: { freshness: string }
      bots: { sharedRuntime: boolean; nodeCount: number }
      alerts: unknown[]
      tasks: unknown[]
      exceptions: unknown[]
    }
    expect(body.sampledAt).toBeTruthy()
    expect(body.health.nodeCount).toBeGreaterThan(0)
    expect(body.health.onlineNodeCount).toBeGreaterThan(0)
    // 在线 + 离线 = 总数（自洽）
    expect(body.health.onlineNodeCount + body.health.offlineNodeCount).toBe(body.health.nodeCount)
    expect(['fresh', 'stale', 'offline', 'unavailable']).toContain(body.resources.freshness)
    // Bot Worker 为节点级共享进程，契约显式声明
    expect(body.bots.sharedRuntime).toBe(true)
    expect(body.bots.nodeCount).toBe(body.health.nodeCount)
    expect(Array.isArray(body.alerts)).toBe(true)
    expect(Array.isArray(body.tasks)).toBe(true)
    expect(Array.isArray(body.exceptions)).toBe(true)
  })
})

describe('健康墙 health-wall（FR-461）', () => {
  it('逐节点返回快照，字段齐全、level 合法、href 可下钻', async () => {
    const token = await login()
    const r = await fetch(`${BASE}/observability/health-wall?sort=level`, {
      headers: { Authorization: `Bearer ${token}` },
    })
    expect(r.status).toBe(200)
    const body = (await r.json()) as { nodes: WallNode[] }
    expect(body.nodes.length).toBeGreaterThan(0)
    for (const n of body.nodes) {
      expect(typeof n.nodeId).toBe('number')
      expect(n.nodeUuid).toBeTruthy()
      expect(n.name).toBeTruthy()
      expect(['fresh', 'stale', 'offline']).toContain(n.freshness)
      expect(['offline', 'stale', 'degraded', 'healthy']).toContain(n.level)
      expect(typeof n.running).toBe('number')
      expect(typeof n.crashed).toBe('number')
      expect(typeof n.stopped).toBe('number')
      expect(n.href).toContain('/monitoring?node=')
    }
  })

  it('sort=cpu 时按 cpuPct 降序（与真后端排序键一致）', async () => {
    const token = await login()
    const r = await fetch(`${BASE}/observability/health-wall?sort=cpu`, {
      headers: { Authorization: `Bearer ${token}` },
    })
    const body = (await r.json()) as { nodes: WallNode[] }
    const cpus = body.nodes.map((n) => n.cpuPct ?? 0)
    const sorted = [...cpus].sort((a, b) => b - a)
    expect(cpus).toEqual(sorted)
  })
})
