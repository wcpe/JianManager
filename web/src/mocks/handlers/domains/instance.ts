import { HttpResponse } from 'msw'
import { domainRoute } from '@/mocks/inject'
import { db } from '@/mocks/db'
import { requireAuth } from '@/mocks/auth-middleware'

/**
 * 实例核心域 mock handler（FR-201，照 spec §7 范式）。
 * 覆盖实例 CRUD / 状态机（start→RUNNING、stop→STOPPED…）/ 端口 / 服务器状态 / 组织分组。
 * 不重定义地基/它域端点：/instances/:id/terminal-token（realtime/terminal-ws）、
 * /instances/events（realtime/instance-events）、/instances/:id/metrics（监控域）。
 */

/**
 * 假后端实例行。字段严格对齐 web/src/api/instances.ts 的 InstanceInfo——
 * tags 是 JSON 字符串列（"" / '["env:prod"]' / "null"），与后端一致、前端经 parseTags 解析，
 * 直接当数组用会白屏（见全局记忆「JSON 字符串字段前端解析」）。
 */
export interface MockInstance {
  id: number
  uuid: string
  nodeId: number
  name: string
  type: string
  role: string
  processType: string
  status: string
  startCommand: string
  workDir: string
  image: string
  cpuLimit: number
  memLimitMb: number
  diskLimitMb: number
  serverPort: number
  autoStart: boolean
  autoRestart: boolean
  /** JSON 字符串列（保真返回字符串，勿返数组）。 */
  tags: string
  createdAt: string
}

/** 假后端组织分组节点（对应 InstanceGroupNode，扁平 + parentId 重建层级）。 */
export interface MockInstanceGroup {
  id: number
  uuid: string
  name: string
  parentId: number | null
  sort: number
}

/** 分组 ↔ 实例成员关系（多对多；instanceCount 由子树去重聚合）。 */
export interface MockGroupMembership {
  id: number
  groupId: number
  instanceId: number
}

// 集合在所属域 handler 模块顶层带 seedFn 唯一声明（import 即播种，resetDb 重播）。
const instances = db<MockInstance>('instances', () => [
  {
    id: 1,
    uuid: 'i-survival',
    nodeId: 1,
    name: 'survival-1',
    type: 'minecraft_java',
    role: 'backend',
    processType: 'daemon',
    status: 'RUNNING',
    startCommand: 'java -Xmx2G -jar paper.jar nogui',
    workDir: '/servers/survival-1',
    image: '',
    cpuLimit: 0,
    memLimitMb: 0,
    diskLimitMb: 0,
    serverPort: 25565,
    autoStart: false,
    autoRestart: true,
    tags: '["env:prod","survival"]',
    createdAt: '2026-01-01T00:00:00Z',
  },
  {
    id: 2,
    uuid: 'i-lobby',
    nodeId: 1,
    name: 'lobby-proxy',
    type: 'minecraft_proxy',
    role: 'proxy',
    processType: 'daemon',
    status: 'STOPPED',
    startCommand: 'java -jar velocity.jar',
    workDir: '/servers/lobby-proxy',
    image: '',
    cpuLimit: 0,
    memLimitMb: 0,
    diskLimitMb: 0,
    serverPort: 25577,
    autoStart: true,
    autoRestart: true,
    tags: '["env:prod"]',
    createdAt: '2026-01-02T00:00:00Z',
  },
  {
    id: 3,
    uuid: 'i-creative',
    nodeId: 2,
    name: 'creative-1',
    type: 'minecraft_java',
    role: 'backend',
    processType: 'docker',
    status: 'CRASHED',
    startCommand: 'java -jar paper.jar nogui',
    workDir: '/servers/creative-1',
    image: 'itzg/minecraft-server:latest',
    cpuLimit: 1.5,
    memLimitMb: 2048,
    diskLimitMb: 0,
    serverPort: 25566,
    autoStart: false,
    autoRestart: false,
    tags: '["env:test","creative"]',
    createdAt: '2026-01-03T00:00:00Z',
  },
])

const instanceGroups = db<MockInstanceGroup>('instanceGroups', () => [
  { id: 1, uuid: 'g-asia', name: '亚洲区', parentId: null, sort: 0 },
  { id: 2, uuid: 'g-survival', name: '生存', parentId: 1, sort: 0 },
  { id: 3, uuid: 'g-creative', name: '创造', parentId: 1, sort: 1 },
])

const groupMembers = db<MockGroupMembership>('instanceGroupMembers', () => [
  { id: 1, groupId: 2, instanceId: 1 },
  { id: 2, groupId: 3, instanceId: 3 },
])

/** 标签字符串列解析为数组（容错非 JSON），供按 env/tag 过滤。 */
function parseTags(raw: string): string[] {
  if (!raw || raw === 'null') return []
  try {
    const v: unknown = JSON.parse(raw)
    return Array.isArray(v) ? v.filter((t): t is string => typeof t === 'string') : []
  } catch {
    return []
  }
}

/** 子树（含自身及所有后代）去重的实例 ID 集合，对应 instanceCount / GET …/instances 语义。 */
function subtreeInstanceIds(groupId: number): number[] {
  const descendants = new Set<number>([groupId])
  let grew = true
  while (grew) {
    grew = false
    for (const g of instanceGroups.list()) {
      if (g.parentId != null && descendants.has(g.parentId) && !descendants.has(g.id)) {
        descendants.add(g.id)
        grew = true
      }
    }
  }
  const ids = new Set<number>()
  for (const m of groupMembers.list()) {
    if (descendants.has(m.groupId)) ids.add(m.instanceId)
  }
  return [...ids]
}

/** 实例概要（分组/群组成员视图复用）。 */
function memberView(instanceId: number) {
  const inst = instances.get(instanceId)
  return {
    instanceId,
    name: inst?.name ?? `#${instanceId}`,
    role: inst?.role ?? 'backend',
    nodeId: inst?.nodeId ?? 0,
    status: inst?.status ?? 'STOPPED',
  }
}

let groupSeq = 100
let memberSeq = 100

export const handlers = [
  // ---- 实例 CRUD ----
  domainRoute('get', '/instances', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const nodeId = url.searchParams.get('nodeId')
    const status = url.searchParams.get('status')
    const role = url.searchParams.get('role')
    const env = url.searchParams.get('env')
    const tag = url.searchParams.get('tag')
    const networkId = url.searchParams.get('networkId')
    const rows = instances.list((i) => {
      if (nodeId && String(i.nodeId) !== nodeId) return false
      if (status && i.status !== status) return false
      if (role && i.role !== role) return false
      const tags = parseTags(i.tags)
      if (env && !tags.includes(`env:${env}`)) return false
      if (tag && !tags.includes(tag)) return false
      // networkId 在假后端无群组关系映射，留作不收敛（仅鉴权/形参占位）。
      void networkId
      return true
    })
    return HttpResponse.json(rows)
  }),

  domainRoute('post', '/instances', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as Partial<MockInstance> & {
      tags?: string[]
      groupId?: number
    }
    const created = instances.insert({
      uuid: `i-${Date.now()}`,
      nodeId: body.nodeId ?? 1,
      name: body.name ?? 'new-instance',
      type: body.type ?? 'minecraft_java',
      role: body.role ?? 'backend',
      processType: body.processType ?? 'daemon',
      status: 'STOPPED',
      startCommand: body.startCommand ?? '',
      workDir: body.workDir ?? `/servers/${body.name ?? 'new-instance'}`,
      image: body.image ?? '',
      cpuLimit: body.cpuLimit ?? 0,
      memLimitMb: body.memLimitMb ?? 0,
      diskLimitMb: body.diskLimitMb ?? 0,
      serverPort: 25600 + instances.list().length,
      autoStart: body.autoStart ?? false,
      autoRestart: body.autoRestart ?? true,
      tags: Array.isArray(body.tags) ? JSON.stringify(body.tags) : '',
      createdAt: new Date().toISOString(),
    })
    if (body.groupId) {
      groupMembers.insert({ id: memberSeq++, groupId: body.groupId, instanceId: created.id })
    }
    return HttpResponse.json(created, { status: 201 })
  }),

  domainRoute('get', '/instances/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const inst = instances.get(Number(info.params.id))
    if (!inst) return HttpResponse.json({ error: 'NOT_FOUND', message: '实例不存在' }, { status: 404 })
    return HttpResponse.json(inst)
  }),

  domainRoute('put', '/instances/:id', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    if (!instances.get(id)) return HttpResponse.json({ error: 'NOT_FOUND', message: '实例不存在' }, { status: 404 })
    const body = (await info.request.json()) as Partial<MockInstance> & { tags?: string[] }
    const patch: Partial<MockInstance> = {}
    if (body.name !== undefined) patch.name = body.name
    if (body.startCommand !== undefined) patch.startCommand = body.startCommand
    if (body.autoStart !== undefined) patch.autoStart = body.autoStart
    if (body.autoRestart !== undefined) patch.autoRestart = body.autoRestart
    if (body.cpuLimit !== undefined) patch.cpuLimit = body.cpuLimit
    if (body.memLimitMb !== undefined) patch.memLimitMb = body.memLimitMb
    if (body.diskLimitMb !== undefined) patch.diskLimitMb = body.diskLimitMb
    if (Array.isArray(body.tags)) patch.tags = JSON.stringify(body.tags)
    return HttpResponse.json(instances.update(id, patch))
  }),

  domainRoute('delete', '/instances/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    instances.remove(id)
    groupMembers
      .list((m) => m.instanceId === id)
      .forEach((m) => groupMembers.remove(m.id))
    return new HttpResponse(null, { status: 204 })
  }),

  // ---- 状态机：start/stop/restart/kill 改 status，列表/详情联动 ----
  domainRoute('post', '/instances/:id/start', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    instances.update(Number(info.params.id), { status: 'RUNNING' })
    return HttpResponse.json({ message: '已启动' })
  }),

  domainRoute('post', '/instances/:id/stop', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    instances.update(Number(info.params.id), { status: 'STOPPED' })
    return HttpResponse.json({ message: '已停止' })
  }),

  domainRoute('post', '/instances/:id/restart', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    instances.update(Number(info.params.id), { status: 'RUNNING' })
    return HttpResponse.json({ message: '已重启' })
  }),

  domainRoute('post', '/instances/:id/kill', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    instances.update(Number(info.params.id), { status: 'STOPPED' })
    return HttpResponse.json({ message: '已终止' })
  }),

  domainRoute('post', '/instances/:id/command', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const { command } = (await info.request.json()) as { command?: string }
    if (!command) return HttpResponse.json({ error: 'INVALID_REQUEST', message: '缺少 command' }, { status: 400 })
    const inst = instances.get(Number(info.params.id))
    if (!inst) return HttpResponse.json({ error: 'NOT_FOUND', message: '实例不存在' }, { status: 404 })
    if (inst.status !== 'RUNNING')
      return HttpResponse.json({ error: 'INSTANCE_NOT_RUNNING', message: '实例非运行中' }, { status: 422 })
    return HttpResponse.json({ message: '已发送' })
  }),

  domainRoute('post', '/instances/batch', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as {
      action: string
      ids?: number[]
      filter?: { nodeId?: number; status?: string; role?: string }
      command?: string
    }
    let targets: MockInstance[]
    if (body.ids?.length) {
      targets = body.ids.map((id) => instances.get(id)).filter((i): i is MockInstance => !!i)
    } else {
      targets = instances.list((i) => {
        if (body.filter?.nodeId && i.nodeId !== body.filter.nodeId) return false
        if (body.filter?.status && i.status !== body.filter.status) return false
        if (body.filter?.role && i.role !== body.filter.role) return false
        return true
      })
    }
    const nextStatus: Record<string, string> = { start: 'RUNNING', stop: 'STOPPED', restart: 'RUNNING', kill: 'STOPPED' }
    let succeeded = 0
    for (const inst of targets) {
      if (body.action === 'command') {
        if (inst.status === 'RUNNING') succeeded++
      } else if (nextStatus[body.action]) {
        instances.update(inst.id, { status: nextStatus[body.action] })
        succeeded++
      }
    }
    return HttpResponse.json({
      action: body.action,
      requested: targets.length,
      succeeded,
      failed: 0,
      skipped: (body.ids?.length ?? 0) - targets.length,
      errors: [],
    })
  }),

  // ---- 服务器状态（FR-076，原样透传探针 JSON）----
  domainRoute('get', '/instances/:id/server-state', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const inst = instances.get(Number(info.params.id))
    if (!inst) return HttpResponse.json({ error: 'NOT_FOUND', message: '实例不存在' }, { status: 404 })
    if (inst.status !== 'RUNNING') {
      return HttpResponse.json({ instanceId: inst.id, connected: false, available: false, state: null, error: '探针未连入' })
    }
    return HttpResponse.json({
      instanceId: inst.id,
      connected: true,
      available: true,
      error: '',
      state: {
        collectedAt: Date.now(),
        server: { version: '1.20.4', motd: 'A Mock Server', onlinePlayers: 2, maxPlayers: 20, viewDistance: 10 },
        worlds: {
          items: [{ name: 'world', environment: 'NORMAL', difficulty: 'EASY', loadedChunks: 441, entities: 30, tileEntities: 12, players: 2, seed: 12345 }],
          total: 1,
          truncated: false,
        },
        jvm: { jvmName: 'OpenJDK 64-Bit Server VM', jvmVersion: '17.0.10', availableProcessors: 4, heapUsedBytes: 536870912, heapMaxBytes: 2147483648, threadCount: 59 },
        classloader: { counts: { loadedClassCount: 18000, totalLoadedClassCount: 18500, unloadedClassCount: 500 } },
        scheduler: { pendingTasks: 3, activeWorkers: 1 },
        listeners: { totalRegistered: 42 },
      },
    })
  }),

  // ---- 端口占用（FR-032）----
  domainRoute('get', '/nodes/:id/ports', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const nodeId = Number(info.params.id)
    const occupied = instances
      .list((i) => i.nodeId === nodeId)
      .map((i) => ({ instanceId: i.id, name: i.name, role: i.role, serverPort: i.serverPort, queryPort: 0 }))
    return HttpResponse.json({ nodeId, ranges: { serverPortBase: 25565, rangeSize: 100 }, occupied })
  }),

  // ---- 组织分组树（FR-165）----
  domainRoute('get', '/instance-groups', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const rows = instanceGroups.list().map((g) => ({
      id: g.id,
      uuid: g.uuid,
      name: g.name,
      parentId: g.parentId,
      sort: g.sort,
      instanceCount: subtreeInstanceIds(g.id).length,
    }))
    return HttpResponse.json(rows)
  }),

  domainRoute('post', '/instance-groups', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as { name?: string; parentId?: number | null }
    if (!body.name) return HttpResponse.json({ error: 'INVALID_REQUEST', message: '名称为空' }, { status: 400 })
    if (body.parentId != null && !instanceGroups.get(body.parentId))
      return HttpResponse.json({ error: 'INSTANCE_GROUP_PARENT_NOT_FOUND', message: '父分组不存在' }, { status: 400 })
    const created = instanceGroups.insert({
      id: groupSeq++,
      uuid: `g-${Date.now()}`,
      name: body.name,
      parentId: body.parentId ?? null,
      sort: 0,
    })
    return HttpResponse.json({ ...created, instanceCount: 0 }, { status: 201 })
  }),

  domainRoute('put', '/instance-groups/:id', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    if (!instanceGroups.get(id))
      return HttpResponse.json({ error: 'INSTANCE_GROUP_NOT_FOUND', message: '分组不存在' }, { status: 404 })
    const body = (await info.request.json()) as { name?: string; parentId?: number | null }
    const patch: Partial<MockInstanceGroup> = {}
    if (body.name !== undefined) patch.name = body.name
    if ('parentId' in body) patch.parentId = body.parentId ?? null
    const updated = instanceGroups.update(id, patch)
    return HttpResponse.json({ ...updated, instanceCount: subtreeInstanceIds(id).length })
  }),

  domainRoute('delete', '/instance-groups/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const hasChild = instanceGroups.list((g) => g.parentId === id).length > 0
    const hasMember = groupMembers.list((m) => m.groupId === id).length > 0
    if (hasChild || hasMember)
      return HttpResponse.json({ error: 'INSTANCE_GROUP_NOT_EMPTY', message: '分组非空' }, { status: 409 })
    instanceGroups.remove(id)
    return new HttpResponse(null, { status: 204 })
  }),

  domainRoute('get', '/instance-groups/:id/instances', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    if (!instanceGroups.get(id))
      return HttpResponse.json({ error: 'INSTANCE_GROUP_NOT_FOUND', message: '分组不存在' }, { status: 404 })
    return HttpResponse.json({ instanceIds: subtreeInstanceIds(id) })
  }),

  domainRoute('post', '/instance-groups/:id/members', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const { instanceIds = [] } = (await info.request.json()) as { instanceIds?: number[] }
    let added = 0
    for (const instanceId of instanceIds) {
      const exists = groupMembers.find((m) => m.groupId === id && m.instanceId === instanceId)
      if (!exists && instances.get(instanceId)) {
        groupMembers.insert({ id: memberSeq++, groupId: id, instanceId })
        added++
      }
    }
    return HttpResponse.json({ added, members: subtreeInstanceIds(id).map(memberView) })
  }),

  domainRoute('delete', '/instance-groups/:id/members', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const { instanceIds = [] } = (await info.request.json()) as { instanceIds?: number[] }
    for (const instanceId of instanceIds) {
      const m = groupMembers.find((x) => x.groupId === id && x.instanceId === instanceId)
      if (m) groupMembers.remove(m.id)
    }
    return new HttpResponse(null, { status: 204 })
  }),
]
