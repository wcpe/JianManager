import { HttpResponse } from 'msw'
import { domainRoute } from '@/mocks/inject'
import { requireAuth } from '@/mocks/auth-middleware'
import { db } from '@/mocks/db'
import type {
  NetworkSummary,
  NetworkDetail,
  NetworkMember,
  BatchActionResult,
} from '@/api/networks'
import type { Registration, CreateRegistrationBody } from '@/api/registrations'
import type { ProvisionProxyBody, ProvisionProxyResult } from '@/api/proxy'
import type { InstanceInfo } from '@/api/instances'

/**
 * 群组服网络域 mock handler（FR-203，照 spec §7 范式）。
 * 覆盖 networks（群组软标签）/ registrations（proxy↔backend M:N 注册）/ proxy（搭建·resync）三组 API。
 * 所有端点均为平台运维操作，首行 requireAuth。
 */

/**
 * 假后端群组行：去规范化持有 members 快照（NetworkDetail.members），
 * 列表 memberCount 由 members 长度派生，避免与实例域跨集合耦合（并行隔离）。
 */
interface NetworkRow {
  id: number
  uuid: string
  name: string
  description: string
  members: NetworkMember[]
  createdAt: string
}

/**
 * 假后端注册行（ADR-007 的 server_registrations，M:N）：
 * proxyId×backendId 为联合身份；去规范化内嵌 backend 概要（贴合 Registration.backend），
 * 一个 backend 可被多个 proxy 注册 → 多行单后端，体现 M:N。
 */
interface RegistrationRow {
  id: number
  proxyId: number
  backendId: number
  alias: string
  priority: number
  forcedHost: string
  restricted: boolean
  enabled: boolean
  backend: NonNullable<Registration['backend']>
}

// 集合在所属域 handler 模块顶层带 seedFn 唯一声明（import 即播种，resetDb 重播）。
const networks = db<NetworkRow>('networks', () => [
  {
    id: 1,
    uuid: 'net-survival',
    name: 'survival',
    description: '生存群组：1 代理 + 2 后端',
    createdAt: '2026-06-01T08:00:00Z',
    members: [
      { instanceId: 10, name: 'survival-proxy', role: 'proxy', nodeId: 1, status: 'RUNNING' },
      { instanceId: 11, name: 'survival-lobby', role: 'backend', nodeId: 1, status: 'RUNNING' },
      { instanceId: 12, name: 'survival-world', role: 'backend', nodeId: 2, status: 'CRASHED' },
    ],
  },
  {
    id: 2,
    uuid: 'net-creative',
    name: 'creative',
    description: '创造群组',
    createdAt: '2026-06-02T09:30:00Z',
    members: [
      { instanceId: 20, name: 'creative-proxy', role: 'proxy', nodeId: 1, status: 'RUNNING' },
      { instanceId: 21, name: 'creative-plot', role: 'backend', nodeId: 1, status: 'STOPPED' },
    ],
  },
  {
    id: 3,
    uuid: 'net-empty',
    name: 'minigames',
    description: '小游戏群组（暂无成员）',
    createdAt: '2026-06-03T10:00:00Z',
    members: [],
  },
])

const registrations = db<RegistrationRow>('registrations', () => [
  // survival-proxy(10) 注册了 lobby(11) 与 world(12)。
  {
    id: 1,
    proxyId: 10,
    backendId: 11,
    alias: 'lobby',
    priority: 100,
    forcedHost: '',
    restricted: false,
    enabled: true,
    backend: { id: 11, name: 'survival-lobby', role: 'backend', nodeId: 1, serverPort: 25566, status: 'RUNNING' },
  },
  {
    id: 2,
    proxyId: 10,
    backendId: 12,
    alias: 'world',
    priority: 50,
    forcedHost: 'world.example.com',
    restricted: false,
    enabled: true,
    backend: { id: 12, name: 'survival-world', role: 'backend', nodeId: 2, serverPort: 25567, status: 'CRASHED' },
  },
  // creative-proxy(20) 也注册了 lobby(11) —— 同一后端被多个 proxy 注册，体现 M:N。
  {
    id: 3,
    proxyId: 20,
    backendId: 11,
    alias: 'shared-lobby',
    priority: 80,
    forcedHost: '',
    restricted: true,
    enabled: false,
    backend: { id: 11, name: 'survival-lobby', role: 'backend', nodeId: 1, serverPort: 25566, status: 'RUNNING' },
  },
])

/** 群组行 → 列表概要（剥离 members，仅暴露计数）。 */
function toSummary(n: NetworkRow): NetworkSummary {
  return {
    id: n.id,
    uuid: n.uuid,
    name: n.name,
    description: n.description,
    memberCount: n.members.length,
    createdAt: n.createdAt,
  }
}

/** 群组行 → 详情（含成员）。 */
function toDetail(n: NetworkRow): NetworkDetail {
  return { id: n.id, uuid: n.uuid, name: n.name, description: n.description, members: n.members }
}

/** 注册行 → 响应（剥离去规范化内部表示外的形状不变）。 */
function toRegistration(r: RegistrationRow): Registration {
  return {
    id: r.id,
    proxyId: r.proxyId,
    backendId: r.backendId,
    alias: r.alias,
    priority: r.priority,
    forcedHost: r.forcedHost,
    restricted: r.restricted,
    enabled: r.enabled,
    backend: r.backend,
  }
}

export const handlers = [
  // ---- 群组（Network 软标签） ----
  domainRoute('get', '/networks', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(networks.list().map(toSummary))
  }),

  domainRoute('get', '/networks/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const n = networks.get(id)
    if (!n) return HttpResponse.json({ error: 'NETWORK_NOT_FOUND', message: '群组不存在' }, { status: 404 })
    return HttpResponse.json(toDetail(n))
  }),

  domainRoute('post', '/networks', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as { name: string; description?: string }
    if (networks.find((n) => n.name === body.name)) {
      return HttpResponse.json({ error: 'NETWORK_NAME_CONFLICT', message: '群组名称已存在' }, { status: 409 })
    }
    const row = networks.insert({
      uuid: `net-${body.name}`,
      name: body.name,
      description: body.description ?? '',
      members: [],
      createdAt: new Date().toISOString(),
    })
    return HttpResponse.json(toSummary(row), { status: 201 })
  }),

  domainRoute('patch', '/networks/:id', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const n = networks.get(id)
    if (!n) return HttpResponse.json({ error: 'NETWORK_NOT_FOUND', message: '群组不存在' }, { status: 404 })
    const body = (await info.request.json()) as { name?: string; description?: string }
    const patch: Partial<NetworkRow> = {}
    if (body.name !== undefined) patch.name = body.name
    if (body.description !== undefined) patch.description = body.description
    const updated = networks.update(id, patch)!
    return HttpResponse.json(toDetail(updated))
  }),

  domainRoute('delete', '/networks/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    if (!networks.get(id)) {
      return HttpResponse.json({ error: 'NETWORK_NOT_FOUND', message: '群组不存在' }, { status: 404 })
    }
    networks.remove(id)
    return new HttpResponse(null, { status: 204 })
  }),

  // 批量加入成员：从实例集合（若存在）取名/角色/状态，否则合成占位（并行隔离不依赖实例域）。
  domainRoute('post', '/networks/:id/members', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const n = networks.get(id)
    if (!n) return HttpResponse.json({ error: 'NETWORK_NOT_FOUND', message: '群组不存在' }, { status: 404 })
    const { instanceIds } = (await info.request.json()) as { instanceIds: number[] }
    const instances = db<InstanceInfo>('instances')
    const existing = new Set(n.members.map((m) => m.instanceId))
    const added: NetworkMember[] = []
    for (const iid of instanceIds) {
      if (existing.has(iid)) continue
      const inst = instances.get(iid)
      added.push({
        instanceId: iid,
        name: inst?.name ?? `instance-${iid}`,
        role: inst?.role ?? 'universal',
        nodeId: inst?.nodeId ?? 0,
        status: inst?.status ?? 'STOPPED',
      })
      existing.add(iid)
    }
    networks.update(id, { members: [...n.members, ...added] })
    return HttpResponse.json(toDetail(networks.get(id)!))
  }),

  domainRoute('delete', '/networks/:id/members/:instanceId', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const params = info.params as { id: string; instanceId: string }
    const id = Number(params.id)
    const instanceId = Number(params.instanceId)
    const n = networks.get(id)
    if (!n) return HttpResponse.json({ error: 'NETWORK_NOT_FOUND', message: '群组不存在' }, { status: 404 })
    networks.update(id, { members: n.members.filter((m) => m.instanceId !== instanceId) })
    return new HttpResponse(null, { status: 204 })
  }),

  // 成员批量启停：对群组成员逐个置态并回报计数（按标签批量运维）。
  domainRoute('post', '/networks/:id/actions', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number((info.params as { id: string }).id)
    const n = networks.get(id)
    if (!n) return HttpResponse.json({ error: 'NETWORK_NOT_FOUND', message: '群组不存在' }, { status: 404 })
    const { action } = (await info.request.json()) as { action: 'start' | 'stop' | 'restart' }
    const nextStatus = action === 'stop' ? 'STOPPED' : 'RUNNING'
    const instances = db<InstanceInfo>('instances')
    const members = n.members.map((m) => {
      instances.update(m.instanceId, { status: nextStatus })
      return { ...m, status: nextStatus }
    })
    networks.update(id, { members })
    const result: BatchActionResult = {
      action,
      total: members.length,
      succeeded: members.length,
      failed: 0,
      results: members.map((m) => ({ instanceId: m.instanceId, ok: true })),
    }
    return HttpResponse.json(result)
  }),

  // ---- proxy↔backend 注册（M:N） ----
  domainRoute('get', '/proxies/:proxyId/registrations', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const proxyId = Number((info.params as { proxyId: string }).proxyId)
    return HttpResponse.json(registrations.list((r) => r.proxyId === proxyId).map(toRegistration))
  }),

  domainRoute('post', '/proxies/:proxyId/registrations', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const proxyId = Number((info.params as { proxyId: string }).proxyId)
    const body = (await info.request.json()) as CreateRegistrationBody
    if (registrations.find((r) => r.proxyId === proxyId && r.backendId === body.backendId)) {
      return HttpResponse.json(
        { error: 'REGISTRATION_CONFLICT', message: '该后端已注册到此代理' },
        { status: 409 },
      )
    }
    const inst = db<InstanceInfo>('instances').get(body.backendId)
    const row = registrations.insert({
      proxyId,
      backendId: body.backendId,
      alias: body.alias ?? inst?.name ?? `backend-${body.backendId}`,
      priority: body.priority ?? 0,
      forcedHost: body.forcedHost ?? '',
      restricted: body.restricted ?? false,
      enabled: body.enabled ?? true,
      backend: {
        id: body.backendId,
        name: inst?.name ?? `backend-${body.backendId}`,
        role: inst?.role ?? 'backend',
        nodeId: inst?.nodeId ?? 0,
        serverPort: inst?.serverPort ?? 0,
        status: inst?.status ?? 'STOPPED',
      },
    })
    return HttpResponse.json(toRegistration(row), { status: 201 })
  }),

  domainRoute('patch', '/proxies/:proxyId/registrations/:rid', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const rid = Number((info.params as { rid: string }).rid)
    const r = registrations.get(rid)
    if (!r) return HttpResponse.json({ error: 'REGISTRATION_NOT_FOUND', message: '注册不存在' }, { status: 404 })
    const body = (await info.request.json()) as Partial<Omit<CreateRegistrationBody, 'backendId'>>
    const patch: Partial<RegistrationRow> = {}
    if (body.alias !== undefined) patch.alias = body.alias
    if (body.priority !== undefined) patch.priority = body.priority
    if (body.forcedHost !== undefined) patch.forcedHost = body.forcedHost
    if (body.restricted !== undefined) patch.restricted = body.restricted
    if (body.enabled !== undefined) patch.enabled = body.enabled
    return HttpResponse.json(toRegistration(registrations.update(rid, patch)!))
  }),

  domainRoute('delete', '/proxies/:proxyId/registrations/:rid', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const rid = Number((info.params as { rid: string }).rid)
    if (!registrations.get(rid)) {
      return HttpResponse.json({ error: 'REGISTRATION_NOT_FOUND', message: '注册不存在' }, { status: 404 })
    }
    registrations.remove(rid)
    return new HttpResponse(null, { status: 204 })
  }),

  // ---- 搭建代理 / resync ----
  domainRoute('post', '/instances/provision/proxy', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const body = (await info.request.json()) as ProvisionProxyBody
    const instances = db<InstanceInfo>('instances')
    const instance = instances.insert({
      uuid: `proxy-${Date.now()}`,
      nodeId: body.nodeId,
      name: body.name,
      type: 'minecraft',
      role: 'proxy',
      processType: 'daemon',
      status: 'STOPPED',
      startCommand: '',
      workDir: `/data/${body.name}`,
      serverPort: 25565,
      autoStart: false,
      autoRestart: false,
      tags: '',
      createdAt: new Date().toISOString(),
    })
    const result: ProvisionProxyResult = {
      instance,
      forwardingSecret: 'mock-forwarding-secret',
      registrations: [],
      warnings: [],
    }
    return HttpResponse.json(result, { status: 201 })
  }),

  domainRoute('post', '/proxies/:proxyId/resync', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({ synced: true, secretConsistent: true, warnings: [] })
  }),
]
