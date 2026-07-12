import { HttpResponse } from 'msw'
import { domainRoute } from '@/mocks/inject'
import { requireAuth } from '@/mocks/auth-middleware'
import { db } from '@/mocks/db'

/**
 * Bot 与终端域 mock handler（FR-209）。照 spec §7 范式：用 domainRoute 注册本域全部 endpoint，
 * 读写 db('bots') 集合，受保护端点首行 requireAuth。
 *
 * 注意：终端 token（GET /instances/:id/terminal-token）与终端 WS 由地基 realtime/terminal-ws.ts
 * 提供，本文件不重定义；terminal.ts 仅取该 token，归地基。本域只负责 /bots* REST。
 */

/**
 * 假后端 Bot 行。字段严格匹配 api/bots.ts 的 BotInfo：
 * config 以 JSON 字符串存储（后端契约，前端解析；见全局记忆「JSON 字符串字段前端解析」）。
 */
export interface BotRow {
  id: number
  uuid: string
  instanceId: number
  stressSessionId?: number
  /** 所属节点 ID，供 GET /bots/summary?groupBy=node 聚合（BotInfo 不含此字段，仅 mock 内部用于分组）。 */
  nodeId: number
  name: string
  status: string
  config: string
  behavior: string
  workerId: string
  createdAt: string
  updatedAt: string
}

interface BotStressSessionRow {
  id: number
  uuid: string
  instanceId: number
  count: number
  behavior: string
  namePrefix: string
  config?: Record<string, unknown>
  orchestrationYaml?: string
  orchestrationSummary?: OrchestrationSummary
  status: string
  startedAt?: string | null
  stoppedAt?: string | null
  createdAt: string
  updatedAt: string
}

interface OrchestrationSummary {
  enabled: boolean
  loop: boolean
  staggerMs: number
  phaseCount: number
  durationSec: number
  behaviors: string[]
}

const NOW = '2026-06-28T00:00:00Z'
const encoder = new TextEncoder()
const botEventControllers = new Map<number, Set<ReadableStreamDefaultController<Uint8Array>>>()

/** 实例 ID → 可读名映射，仅供 summary 分组 label 渲染（不跨域读 db('instances')，保持本域自洽）。 */
const INSTANCE_LABELS: Record<number, string> = { 1: '生存服', 2: '空岛服' }
/** 节点 ID → 可读名映射，同上，仅供 groupBy=node 的 label。 */
const NODE_LABELS: Record<number, string> = { 1: '主节点', 2: '边缘节点' }

function bot(row: Omit<BotRow, 'uuid' | 'config' | 'workerId' | 'createdAt' | 'updatedAt'> & { config: Record<string, unknown>; workerId?: string }): BotRow {
  return {
    uuid: `bot-${row.id}`,
    workerId: row.workerId ?? `node-${row.nodeId}`,
    createdAt: NOW,
    updatedAt: NOW,
    ...row,
    config: JSON.stringify(row.config),
  }
}

// 集合在所属域 handler 模块顶层带 seedFn 唯一声明（import 即播种，resetDb 重播）。
const bots = db<BotRow>('bots', () => [
  bot({ id: 1, instanceId: 1, nodeId: 1, name: 'GuardBot', status: 'connected', behavior: 'guard', config: { server: '127.0.0.1', port: 25565, auth: 'offline' } }),
  bot({ id: 2, instanceId: 1, nodeId: 1, name: 'FollowBot', status: 'connecting', behavior: 'follow', config: { server: '127.0.0.1', port: 25565, auth: 'offline' } }),
  bot({ id: 3, instanceId: 2, nodeId: 2, name: 'PatrolBot', status: 'error', behavior: 'patrol', config: { server: '127.0.0.1', port: 25566, auth: 'offline' } }),
])

const stressSessions = db<BotStressSessionRow>('botStressSessions', () => [])

export function seed(): void {
  bots.seed()
  stressSessions.seed()
}

/** 列表 / summary 共用的多维筛选（与 BotListParams 维度一致）。 */
function filtered(url: URL): BotRow[] {
  const q = url.searchParams.get('q')?.toLowerCase()
  const instanceId = url.searchParams.get('instanceId')
  const nodeId = url.searchParams.get('nodeId')
  const status = url.searchParams.get('status')
  const behavior = url.searchParams.get('behavior')
  return bots.list((b) => {
    if (instanceId && String(b.instanceId) !== instanceId) return false
    if (nodeId && String(b.nodeId) !== nodeId) return false
    if (status && b.status !== status) return false
    if (behavior && b.behavior !== behavior) return false
    if (q && !b.name.toLowerCase().includes(q) && !b.uuid.toLowerCase().includes(q)) return false
    return true
  })
}

/** 按状态聚合计数（connected/connecting/error/...）。 */
function countByStatus(rows: BotRow[]): Record<string, number> {
  const by: Record<string, number> = {}
  for (const b of rows) by[b.status] = (by[b.status] ?? 0) + 1
  return by
}

interface SummaryGroup {
  key: string
  label: string
  total: number
  online: number
}

/** 按指定维度把行分组为 summary.groups（key/label/total/online=connected 数）。 */
function groupRows(rows: BotRow[], dim: string): SummaryGroup[] {
  const buckets = new Map<string, BotRow[]>()
  for (const b of rows) {
    const key =
      dim === 'instance' ? String(b.instanceId)
      : dim === 'node' ? String(b.nodeId)
      : dim === 'status' ? b.status
      : b.behavior
    const arr = buckets.get(key) ?? []
    arr.push(b)
    buckets.set(key, arr)
  }
  return [...buckets.entries()].map(([key, arr]) => ({
    key,
    label:
      dim === 'instance' ? (INSTANCE_LABELS[Number(key)] ?? key)
      : dim === 'node' ? (NODE_LABELS[Number(key)] ?? key)
      : key,
    total: arr.length,
    online: arr.filter((b) => b.status === 'connected').length,
  }))
}

const GROUP_DIMS = new Set(['instance', 'node', 'status', 'behavior'])

function pushBotEvent(botId: number, type: string, data: Record<string, unknown>): void {
  const row = bots.get(botId)
  if (!row) return
  const payload = {
    botId,
    botUuid: row.uuid,
    type,
    data,
    timestamp: Date.now(),
  }
  const bytes = encoder.encode(`event: bot\ndata: ${JSON.stringify(payload)}\n\n`)
  for (const c of botEventControllers.get(botId) ?? []) {
    try {
      c.enqueue(bytes)
    } catch {
      botEventControllers.get(botId)?.delete(c)
    }
  }
}

function stressCounts(sessionId: number): { total: number; byStatus: Record<string, number> } {
  const rows = bots.list((b) => b.stressSessionId === sessionId)
  return { total: rows.length, byStatus: countByStatus(rows) }
}

function summarizeOrchestration(raw?: string): OrchestrationSummary | undefined {
  if (!raw?.includes('phases:')) return undefined
  const behaviorMatches = [...raw.matchAll(/^\s*behavior:\s*"?([^"\s#]+)"?/gm)]
  const durationMatches = [...raw.matchAll(/^\s*durationSec:\s*(\d+)/gm)]
  const behaviors = [...new Set(behaviorMatches.map((m) => m[1]))]
  const durations = durationMatches.map((m) => Number(m[1]))
  const stagger = raw.match(/^\s*staggerMs:\s*(\d+)/m)
  return {
    enabled: true,
    loop: /^\s*loop:\s*true\s*$/m.test(raw),
    staggerMs: stagger ? Number(stagger[1]) : 0,
    phaseCount: Math.max(durations.length, behaviors.length),
    durationSec: durations.reduce((sum, value) => sum + value, 0),
    behaviors,
  }
}

export const handlers = [
  // GET /bots/summary：注册在 /bots 之前，避免 :id 通配吞掉 "summary"（MSW 按顺序匹配）。
  domainRoute('get', '/bots/summary', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const rows = filtered(url)
    const groupBy = url.searchParams.get('groupBy')
    const base = { total: rows.length, byStatus: countByStatus(rows) }
    if (groupBy && GROUP_DIMS.has(groupBy)) {
      return HttpResponse.json({ ...base, groupBy, groups: groupRows(rows, groupBy) })
    }
    if (groupBy) {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: 'groupBy 非法值' }, { status: 400 })
    }
    return HttpResponse.json(base)
  }),

  domainRoute('get', '/bots', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const rows = filtered(url)
    const page = Math.max(1, Number(url.searchParams.get('page') ?? 1) || 1)
    const pageSize = Math.min(100, Math.max(1, Number(url.searchParams.get('pageSize') ?? 20) || 20))
    const start = (page - 1) * pageSize
    return HttpResponse.json({ items: rows.slice(start, start + pageSize), total: rows.length, page, pageSize })
  }),

  domainRoute('post', '/bots', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    // 前端在 useCreateBot 里已 JSON.stringify(config)，故请求体 config 是字符串。
    const body = (await info.request.json()) as { instanceId: number; name: string; config: string; behavior: string }
    const created = bots.insert({
      uuid: `bot-${Date.now()}`,
      instanceId: body.instanceId,
      nodeId: 1,
      name: body.name,
      status: 'pending',
      config: typeof body.config === 'string' ? body.config : JSON.stringify(body.config),
      behavior: body.behavior,
      workerId: 'node-1',
      createdAt: NOW,
      updatedAt: NOW,
    })
    return HttpResponse.json(created, { status: 201 })
  }),

  domainRoute('get', '/bots/stress-sessions', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const page = Math.max(1, Number(url.searchParams.get('page') ?? 1) || 1)
    const pageSize = Math.min(100, Math.max(1, Number(url.searchParams.get('pageSize') ?? 20) || 20))
    const rows = stressSessions.list()
    const start = (page - 1) * pageSize
    return HttpResponse.json({
      items: rows.slice(start, start + pageSize).map((row) => ({ ...row, counts: stressCounts(row.id) })),
      total: rows.length,
      page,
      pageSize,
    })
  }),

  domainRoute('post', '/bots/stress-sessions', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as {
      instanceId: number
      count: number
      behavior: string
      namePrefix: string
      config?: Record<string, unknown>
      orchestrationYaml?: string
    }
    const orchestrationSummary = summarizeOrchestration(body.orchestrationYaml)
    if (!body.instanceId || body.count < 1 || (!body.behavior && !orchestrationSummary) || !body.namePrefix) {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: '参数错误' }, { status: 400 })
    }
    const row = stressSessions.insert({
      uuid: `stress-${Date.now()}`,
      instanceId: body.instanceId,
      count: body.count,
      behavior: body.behavior || orchestrationSummary?.behaviors[0] || 'idle',
      namePrefix: body.namePrefix,
      config: body.config,
      orchestrationYaml: body.orchestrationYaml,
      orchestrationSummary,
      status: 'pending',
      startedAt: null,
      stoppedAt: null,
      createdAt: NOW,
      updatedAt: NOW,
    })
    return HttpResponse.json({ ...row, counts: stressCounts(row.id) }, { status: 201 })
  }),

  domainRoute('get', '/bots/stress-sessions/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const row = stressSessions.get(id)
    if (!row) return HttpResponse.json({ error: 'NOT_FOUND', message: '压测会话不存在' }, { status: 404 })
    return HttpResponse.json({ ...row, counts: stressCounts(id) })
  }),

  domainRoute('post', '/bots/stress-sessions/:id/start', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const row = stressSessions.get(id)
    if (!row) return HttpResponse.json({ error: 'NOT_FOUND', message: '压测会话不存在' }, { status: 404 })
    const existing = bots.list((b) => b.stressSessionId === id)
    if (existing.length === 0) {
      for (let i = 1; i <= row.count; i++) {
        bots.insert({
          uuid: `stress-bot-${Date.now()}-${i}`,
          instanceId: row.instanceId,
          stressSessionId: id,
          nodeId: 1,
          name: `${row.namePrefix}-${String(i).padStart(3, '0')}`,
          status: 'pending',
          config: JSON.stringify(row.config ?? {}),
          behavior: row.orchestrationSummary?.enabled ? 'orchestrated' : row.behavior,
          workerId: 'node-1',
          createdAt: NOW,
          updatedAt: NOW,
        })
      }
    } else {
      for (const b of existing) bots.update(b.id, { status: 'connecting' })
    }
    const updated = stressSessions.update(id, { status: 'running', startedAt: NOW, stoppedAt: null })!
    return HttpResponse.json({ ...updated, counts: stressCounts(id) })
  }),

  domainRoute('post', '/bots/stress-sessions/:id/stop', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const row = stressSessions.get(id)
    if (!row) return HttpResponse.json({ error: 'NOT_FOUND', message: '压测会话不存在' }, { status: 404 })
    for (const b of bots.list((bot) => bot.stressSessionId === id)) {
      bots.update(b.id, { status: 'stopped' })
    }
    const updated = stressSessions.update(id, { status: 'stopped', stoppedAt: NOW })!
    return HttpResponse.json({ ...updated, counts: stressCounts(id) })
  }),

  domainRoute('get', '/bots/:id/events', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const row = bots.get(id)
    if (!row) return HttpResponse.json({ error: 'NOT_FOUND', message: 'Bot 不存在' }, { status: 404 })
    let ref: ReadableStreamDefaultController<Uint8Array>
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        ref = controller
        const set = botEventControllers.get(id) ?? new Set<ReadableStreamDefaultController<Uint8Array>>()
        set.add(controller)
        botEventControllers.set(id, set)
        const init = { botId: id, botUuid: row.uuid, type: 'state', data: { status: row.status, behavior: row.behavior, health: 20, food: 20 }, timestamp: Date.now() }
        controller.enqueue(encoder.encode(`event: bot\ndata: ${JSON.stringify(init)}\n\n`))
      },
      cancel() {
        botEventControllers.get(id)?.delete(ref)
      },
    })
    return new HttpResponse(stream, {
      headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', Connection: 'keep-alive' },
    })
  }),

  domainRoute('get', '/bots/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const row = bots.get(Number(info.params.id))
    if (!row) return HttpResponse.json({ error: 'NOT_FOUND', message: 'Bot 不存在' }, { status: 404 })
    return HttpResponse.json(row)
  }),

  domainRoute('delete', '/bots/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    if (!bots.get(id)) return HttpResponse.json({ error: 'NOT_FOUND', message: 'Bot 不存在' }, { status: 404 })
    bots.remove(id)
    return new HttpResponse(null, { status: 204 })
  }),

  domainRoute('post', '/bots/:id/behavior', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const row = bots.get(id)
    if (!row) return HttpResponse.json({ error: 'NOT_FOUND', message: 'Bot 不存在' }, { status: 404 })
    const { behavior } = (await info.request.json()) as { behavior: string; target?: string }
    bots.update(id, { behavior })
    return HttpResponse.json({ message: '已切换' })
  }),

  domainRoute('post', '/bots/:id/command', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    if (!bots.get(id)) return HttpResponse.json({ error: 'NOT_FOUND', message: 'Bot 不存在' }, { status: 404 })
    const { command } = (await info.request.json()) as { command?: string }
    if (!command) return HttpResponse.json({ error: 'INVALID_REQUEST', message: '缺 command' }, { status: 400 })
    pushBotEvent(id, 'command-sent', { command })
    return HttpResponse.json({ message: '已发送' })
  }),

  domainRoute('post', '/bots/batch', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as {
      action: 'set-behavior' | 'start' | 'stop' | 'delete'
      ids?: number[]
      filter?: { instanceId?: number; nodeId?: number; status?: string; behavior?: string; q?: string }
      behavior?: string
    }
    // 解析目标：ids 优先；否则按 filter 收敛（与 GET /bots 同维度）。
    let targets: BotRow[]
    if (body.ids && body.ids.length > 0) {
      targets = body.ids.map((id) => bots.get(id)).filter((b): b is BotRow => !!b)
    } else if (body.filter) {
      const f = body.filter
      targets = bots.list((b) => {
        if (f.instanceId != null && b.instanceId !== f.instanceId) return false
        if (f.nodeId != null && b.nodeId !== f.nodeId) return false
        if (f.status && b.status !== f.status) return false
        if (f.behavior && b.behavior !== f.behavior) return false
        if (f.q && !b.name.toLowerCase().includes(f.q.toLowerCase())) return false
        return true
      })
    } else {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: '目标为空' }, { status: 400 })
    }
    if (body.action === 'set-behavior' && !body.behavior) {
      return HttpResponse.json({ error: 'INVALID_REQUEST', message: 'set-behavior 缺 behavior' }, { status: 400 })
    }

    const requested = targets.length
    for (const b of targets) {
      switch (body.action) {
        case 'set-behavior':
          bots.update(b.id, { behavior: body.behavior! })
          break
        case 'stop':
          bots.update(b.id, { status: 'stopped' })
          break
        case 'delete':
          bots.remove(b.id)
          break
        case 'start':
          bots.update(b.id, { status: 'connecting' })
          break
      }
    }
    return HttpResponse.json({ action: body.action, requested, succeeded: requested, failed: 0, skipped: 0, errors: [] })
  }),
]
