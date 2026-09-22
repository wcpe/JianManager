import { describe, it, expect, beforeAll, beforeEach, afterAll, afterEach } from 'vitest'
import { server } from './server'
import { db, resetDb } from './db'
import { clearInjections } from './inject'
import type { MockInstance } from './handlers/domains/instance'

/**
 * FR-466 / FR-468 / FR-470 mock 语义对齐测试（W-07 / W-08 / W-15 / W-16 / W-17）。
 *
 * 这些断言锁定「mock 与真后端语义一致」，而不是「mock 能返回 200」：
 * 趋势与列表必须是**两个数据源**、K=5 必须真的裁剪、快照 ID 必须随 resetDb 重置、
 * 二进制回滚必须真的交换 Current/Previous、配额来源必须按真实语义判定。
 * 以上每条都是审查发现的 mock 语义缺口——不测就会让 dom 测试变成假绿。
 */
const BASE = 'http://localhost/api/v1'

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => {
  server.resetHandlers()
  resetDb()
  clearInjections()
})
afterAll(() => server.close())

/** 全部 P3 端点都过 requireAuth（9 个新端点无一例外），故统一带 token 请求。 */
let authHeaders: Record<string, string> = {}

/**
 * 每个用例前重新登录取 token —— **不能放 beforeAll**：`afterEach` 的 `resetDb()` 会连
 * `sessions` 集合一起重播，前一例的会话随之失效，第二个用例起就会全部 401。
 */
async function login(): Promise<void> {
  const res = await fetch(`${BASE}/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: 'admin', password: 'admin123' }),
  })
  const body = (await res.json()) as { accessToken: string }
  authHeaders = { Authorization: `Bearer ${body.accessToken}` }
}

beforeEach(login)

/** 把实例置为 STOPPED：升级/回滚要求实例已停止（种子 30 是 RUNNING）。 */
function stopInstance(id: number): void {
  db<MockInstance>('instances').update(id, { status: 'STOPPED' })
}

/** 带鉴权的 GET。 */
async function getJson<T>(path: string): Promise<T> {
  const res = await fetch(`${BASE}${path}`, { headers: authHeaders })
  return (await res.json()) as T
}

/** 带鉴权的 POST（可选 JSON body）。 */
async function postJson<T>(path: string, body?: unknown): Promise<{ status: number; data: T }> {
  const res = await fetch(`${BASE}${path}`, {
    method: 'POST',
    headers: { ...authHeaders, 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  return { status: res.status, data: (await res.json()) as T }
}

interface CrashRow {
  id: number
  instanceId: number
  occurredAt: string
  rootCause?: string
  signature?: string
}

interface CrashTrend {
  instanceId: number
  days: number
  total: number
  points: { day: string; rootCause: string; count: number }[]
  byRootCause: { rootCause: string; count: number }[]
  topSignatures: { signature: string; rootCause: string; count: number }[]
}

describe('崩溃快照 K=5 与趋势独立汇总表（W-08 / W-16）', () => {
  it('列表按 K=5 裁剪，而趋势总数来自独立汇总表（可大于列表条数）', async () => {
    // 实例 12 种子 6 条快照 —— 列表被裁到 5 条，趋势仍是完整的 6 次。
    const list = await getJson<CrashRow[]>('/instances/12/crash-snapshots')
    expect(list).toHaveLength(5)

    const trend = await getJson<CrashTrend>('/instances/12/crash-trend?days=30')
    expect(trend.total).toBe(6)
    // 核心断言：趋势总数 **严格大于** 列表条数。若有人把趋势来源改回列表（或去掉列表裁剪），
    // 这里立刻变红 —— 这正是原审查所指的「假绿」防护点。
    expect(trend.total).toBeGreaterThan(list.length)
  })

  it('趋势按 (天 × 根因) 聚合：同天同因多个指纹只出一个点，且与 byRootCause 自洽', async () => {
    const trend = await getJson<CrashTrend>('/instances/12/crash-trend?days=30')

    // 每天每根因恰好一个点（统计行唯一键含指纹，逐行 append 会生成重复点）。
    const seen = new Set<string>()
    for (const p of trend.points) {
      const key = `${p.day}\u0000${p.rootCause}`
      expect(seen.has(key), `趋势出现重复点：${key}`).toBe(false)
      seen.add(key)
      expect(p.count).toBeGreaterThan(0)
    }
    // points 求和 == total（同一份数据的两种视图必须自洽）。
    expect(trend.points.reduce((s, p) => s + p.count, 0)).toBe(trend.total)
    // 实例 12 共 6 次：5 次 oom + 1 次 port_in_use（按计数降序）。
    expect(trend.byRootCause).toEqual([
      { rootCause: 'oom', count: 5 },
      { rootCause: 'port_in_use', count: 1 },
    ])
    // 同类聚合按 (根因 × 指纹) 分组：计数合计 = total。
    expect(trend.topSignatures.map((s) => s.signature).sort()).toEqual([
      'Address already in use',
      'GC overhead limit exceeded',
      'Java heap space',
    ])
    expect(trend.topSignatures.reduce((s, x) => s + x.count, 0)).toBe(trend.total)
  })

  it('days 参数生效且被归一化（<=0 → 30，>365 → 365）', async () => {
    // 种子全在最近 5 天内：窄窗口只留最近几天、宽窗口与默认一致。
    const narrow = await getJson<CrashTrend>('/instances/12/crash-trend?days=1')
    const wide = await getJson<CrashTrend>('/instances/12/crash-trend?days=365')
    expect(narrow.days).toBe(1)
    expect(wide.days).toBe(365)
    // 窗口越窄，纳入的崩溃越少（这证明 days **真的**参与了过滤，而非被忽略后原样回显）。
    expect(narrow.total).toBeLessThan(wide.total)
    expect(wide.total).toBe(6)
    // 归一化：0/负数 → 30；超大值 → 365。
    const zero = await getJson<CrashTrend>('/instances/12/crash-trend?days=0')
    const huge = await getJson<CrashTrend>('/instances/12/crash-trend?days=99999')
    expect(zero.days).toBe(30)
    expect(huge.days).toBe(365)
  })
})

interface QuotaStatus {
  instanceId: number
  diskSource: string
  memSource: string
  cpuSource: string
  groupId: number
  enforceStateScope: string
  throttleCpuLimit: number
  throttleMemLimitMb: number
}

describe('配额 mock 来源判定（W-09 / W-17）', () => {
  it('无组实例的磁盘来源为 none，不是无条件 group', async () => {
    // 实例 9 不属于任何实例分组。
    const q = await getJson<QuotaStatus>('/instances/9/quota')
    expect(q.groupId).toBe(0)
    expect(q.diskSource).toBe('none')
  })

  it('组内实例且无实例级磁盘限额时才是组派生', async () => {
    // 实例 3 属于分组 3，且 diskLimitMb=0（内存限额是实例级 → memSource=instance）。
    const q = await getJson<QuotaStatus>('/instances/3/quota')
    expect(q.groupId).toBe(3)
    expect(q.diskSource).toBe('group')
    expect(q.memSource).toBe('instance')
    expect(q.cpuSource).toBe('instance')
  })

  it('响应含 enforceStateScope 与待收紧限额字段（DTO 契约）', async () => {
    const q = await getJson<QuotaStatus>('/instances/3/quota')
    expect(q.enforceStateScope).toBe('in_process')
    expect(q.throttleCpuLimit).toBe(0)
    expect(q.throttleMemLimitMb).toBe(0)
  })
})

interface BinaryView {
  bound: boolean
  currentAssetId: number
  currentVersion: string
  hasRollback: boolean
  previousVersion: string
  candidates: { assetId: number; version: string }[]
}

describe('二进制版本 mock 可回滚态（W-07）', () => {
  it('已绑定实例预置可回滚点：hasRollback=true 且上一版本非空', async () => {
    // 实例 30（beacon-1）：种子 startCommand 已是 1.1.0，回滚点是 1.0.0。
    const view = await getJson<BinaryView>('/instances/30/binary-version')
    expect(view.bound).toBe(true)
    expect(view.hasRollback).toBe(true)
    expect(view.currentVersion).toBe('1.1.0')
    expect(view.previousVersion).toBe('1.0.0')
    // 候选按 assetId 倒序（与后端 View 的排序契约一致）。
    expect(view.candidates.map((c) => c.assetId)).toEqual([13, 12, 11])
  })

  it('回滚真正交换 Current/Previous（可再次回滚），并更新启动命令', async () => {
    stopInstance(30)
    const before = await getJson<BinaryView>('/instances/30/binary-version')
    // 前置状态断言：没有它就无法证明后面观察到的变化来自「交换」而非别处。
    expect(before.currentVersion, '前置：实例 30 应为 1.1.0、回滚点 1.0.0').toBe('1.1.0')
    expect(before.hasRollback).toBe(true)

    const { status, data: result } = await postJson<{
      fromVersion: string
      toVersion: string
      startCommand: string
    }>('/instances/30/binary-rollback')
    expect(status).toBe(200)
    expect(result.fromVersion).toBe('1.1.0')
    expect(result.toVersion).toBe('1.0.0')
    expect(result.startCommand).toContain('beacon-1.0.0-linux-amd64')

    const after = await getJson<BinaryView>('/instances/30/binary-version')
    expect(after.currentVersion).toBe('1.0.0')
    // 语义对称：回滚本身也可再回滚。
    expect(after.hasRollback).toBe(true)
    expect(after.previousVersion).toBe(before.currentVersion)
  })

  it('升级换成目标版本并把旧当前版本记为回滚点', async () => {
    stopInstance(30)
    const { status } = await postJson('/instances/30/binary-upgrade', { assetId: 13 })
    expect(status).toBe(200)
    const after = await getJson<BinaryView>('/instances/30/binary-version')
    expect(after.currentVersion).toBe('1.2.0')
    expect(after.previousVersion).toBe('1.1.0')
  })

  it('运行中实例拒绝版本变更（409，与后端 CONFLICT 同码）', async () => {
    // 实例 30 种子 status=RUNNING。
    const up = await postJson('/instances/30/binary-upgrade', { assetId: 13 })
    expect(up.status).toBe(409)
    const rb = await postJson('/instances/30/binary-rollback')
    expect(rb.status).toBe(409)
  })

  it('无回滚点的绑定实例回滚返回 400 NO_PREVIOUS_VERSION', async () => {
    // 实例 31（懒建绑定）默认无回滚点。
    stopInstance(31)
    const { status, data } = await postJson<{ error: string }>('/instances/31/binary-rollback')
    expect(status).toBe(400)
    expect(data.error).toBe('NO_PREVIOUS_VERSION')
  })

  it('非二进制实例（MC）返回未登记绑定', async () => {
    const view = await getJson<BinaryView>('/instances/1/binary-version')
    expect(view.bound).toBe(false)
    expect(view.hasRollback).toBe(false)
  })
})

interface SnapshotRow {
  id: number
  name: string
  kind: string
  state: string
}

describe('整机快照 mock 可重播性（W-15）', () => {
  it('快照 ID 随 resetDb 重置（不依赖用例执行顺序）', async () => {
    const create = async (name: string) => {
      const { status, data } = await postJson<SnapshotRow>('/instances/2/snapshots', { name })
      expect(status).toBe(202)
      return data
    }

    const first = await create('用例 A')
    const second = await create('用例 A-2')
    expect(first.id).toBe(101)
    expect(second.id).toBe(102)

    // 模拟 vitest 的 afterEach 隔离：重置后同一进程内的下一个用例必须拿到同样的 101。
    // resetDb 会连 sessions 一起重播，故必须重新登录取 token（这正是用例隔离的真实行为）。
    resetDb()
    await login()
    const afterReset = await create('用例 B')
    expect(afterReset.id, 'resetDb 后 ID 必须回到 101，否则测试将依赖执行顺序').toBe(101)
  })

  it('实例 3 的两条种子都挂实例 3，实例 2 为空（注释与实际一致，W-19）', async () => {
    const three = await getJson<SnapshotRow[]>('/instances/3/snapshots')
    expect(three).toHaveLength(2)
    expect(three.map((s) => s.kind).sort()).toEqual(['manual', 'pre_rollback'])
    const two = await getJson<SnapshotRow[]>('/instances/2/snapshots')
    expect(two).toHaveLength(0)
    const one = await getJson<SnapshotRow[]>('/instances/1/snapshots')
    expect(one, '实例 1 无快照种子').toHaveLength(0)
  })
})
