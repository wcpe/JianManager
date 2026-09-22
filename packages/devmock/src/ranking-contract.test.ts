import { describe, it, expect, beforeAll, afterAll, afterEach } from 'vitest'
import { server } from '@jianmanager/devmock/server'
import { db, resetDb } from '@jianmanager/devmock/db'
import type { RankingResultInfo } from '@jianmanager/devmock/contracts'

/**
 * 跨实例排行（FR-469）假后端契约测试。
 *
 * 背景：真后端 `GET /metrics/instances/ranking` 的 `nodeId` 参数是**节点 UUID**（router/metric.go
 * 的 `nodeUUID := c.Query("nodeId")` → `service.RankingQuery.NodeUUID` → 按
 * `s.node_uuid = ?` 过滤），响应里 `nodeUuid` 也必须是**真实节点 UUID**——前端
 * `InstanceRankingPanel` 用 `nodes.find(n => n.uuid === row.nodeUuid)?.name` 渲染「节点」列，
 * 用节点 UUID 作为筛选项 value。
 */

const BASE = 'http://localhost/api/v1'

/** 假后端已 seed 的节点 UUID 集合（node.ts 的 nodes 集合，随域 import 播种）。 */
function seededNodeUuids(): string[] {
  return db<{ id: number; uuid: string }>('nodes')
    .list()
    .map((n) => n.uuid)
}

async function login(): Promise<string> {
  const r = await fetch(`${BASE}/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: 'admin', password: 'admin123' }),
  })
  expect(r.status).toBe(200)
  return ((await r.json()) as { accessToken: string }).accessToken
}

async function fetchRanking(token: string, query = ''): Promise<RankingResultInfo> {
  const r = await fetch(`${BASE}/metrics/instances/ranking${query}`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  expect(r.status).toBe(200)
  return (await r.json()) as RankingResultInfo
}

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => {
  server.resetHandlers()
  resetDb()
})
afterAll(() => server.close())

describe('排行假后端契约（FR-469）', () => {
  it('每条排行项的 nodeUuid 都必须属于已 seed 的节点集合', async () => {
    const token = await login()
    const body = await fetchRanking(token, '?metric=inst_tps&limit=50')

    const known = seededNodeUuids()
    expect(known.length).toBeGreaterThan(0)
    expect(body.items.length).toBeGreaterThan(0)

    const foreign = body.items.filter((it) => !known.includes(it.nodeUuid))
    expect(
      foreign.map((it) => `${it.name}:${it.nodeUuid}`),
      'nodeUuid 必须是真实 seed 的节点 UUID（否则前端「节点」列解析不出名称、退化成截断 UUID）',
    ).toEqual([])
  })

  it('传 nodeId=<节点 UUID> 时只返回该节点的实例', async () => {
    const token = await login()
    const all = await fetchRanking(token, '?metric=inst_tps&limit=100')
    const target = all.items[0].nodeUuid
    const expected = all.items.filter((it) => it.nodeUuid === target).map((it) => it.instanceUuid)

    const scoped = await fetchRanking(
      token,
      `?metric=inst_tps&limit=100&nodeId=${encodeURIComponent(target)}`,
    )
    expect(scoped.items.length, '筛选后条数应少于全量榜').toBeLessThan(all.items.length)
    expect(
      scoped.items.map((it) => it.instanceUuid),
      '筛选结果 = 候选池中该节点的实例（顺序与排序规则一致）',
    ).toEqual(expected)
    for (const it of scoped.items) expect(it.nodeUuid).toBe(target)
  })

  it('nodeId 不存在 → 404 TARGET_NOT_FOUND（与真后端一致，不静默返回全量榜）', async () => {
    const token = await login()
    const r = await fetch(`${BASE}/metrics/instances/ranking?metric=inst_tps&nodeId=node-does-not-exist`, {
      headers: { Authorization: `Bearer ${token}` },
    })
    expect(r.status).toBe(404)
    expect(((await r.json()) as { error: string }).error).toBe('TARGET_NOT_FOUND')
  })

  it('nodeId 过滤后每个 seed 节点都能筛出对应子集（且互不重叠、并集 = 全量）', async () => {
    const token = await login()
    const all = await fetchRanking(token, '?metric=inst_tps&limit=100')
    const allIds = new Set(all.items.map((it) => it.instanceUuid))

    const seen: string[] = []
    for (const nodeUuid of seededNodeUuids()) {
      const scoped = await fetchRanking(
        token,
        `?metric=inst_tps&limit=100&nodeId=${encodeURIComponent(nodeUuid)}`,
      )
      for (const it of scoped.items) {
        expect(it.nodeUuid, `nodeId=${nodeUuid} 的筛选结果混入了别的节点`).toBe(nodeUuid)
        expect(allIds, `nodeId=${nodeUuid} 筛出了不在全量榜内的实例`).toContain(it.instanceUuid)
        expect(seen, '同一实例不得同时归属两个节点的筛选结果').not.toContain(it.instanceUuid)
        seen.push(it.instanceUuid)
      }
    }
    // 各节点子树互不重叠、并集等于全量榜 → 过滤既没漏也没重（修复前筛选恒返回全量 12 条，
    // seen 会膨胀到 24+ 且大量重复，此断言直接锁住该缺陷）。
    expect(new Set(seen).size).toBe(seen.length)
    expect(seen.length).toBe(all.items.length)
  })
})
