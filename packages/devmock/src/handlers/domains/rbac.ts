import { HttpResponse } from 'msw'
import { domainRoute } from '@jianmanager/devmock/inject'
import { requireAuth, requirePlatformAdmin } from '@jianmanager/devmock/auth-middleware'
import { db } from '@jianmanager/devmock/db'
import type { Session, User } from '@jianmanager/devmock/handlers/domains/auth'

/**
 * RBAC 域 mock（FR-432）：/auth/me + /rbac/* 管理面。
 * 节点集合与 control-plane service.PermissionCatalog / systemRoleNodes 对齐（前端测试用精简副本）。
 */

export interface RbacRole {
  id: number
  key: string
  name: string
  description: string
  isSystem: boolean
  nodes: string[]
  createdAt: string
  updatedAt: string
}

export interface UserPermissionOverrideRow {
  id: number
  userId: number
  node: string
  effect: 'allow' | 'deny'
}

export interface UserRoleBinding {
  id: number
  userId: number
  roleId: number
}

const ALL_NODES = [
  'user.read', 'user.manage', 'group.read', 'group.manage', 'group.member.write', 'group.quota.write',
  'node.read', 'node.manage', 'settings.read', 'settings.write', 'system.update', 'system.db.browse',
  'license.read', 'audit.read', 'rbac.read', 'rbac.manage',
  'instance.read', 'instance.create', 'instance.write', 'instance.operate', 'instance.delete',
  'instance.launchspec.write', 'instance.business.write', 'file.read', 'file.write', 'terminal.access',
  'bot.read', 'bot.manage', 'backup.read', 'backup.write', 'schedule.read', 'schedule.write',
  'task.read', 'task.manage',
  'monitor.read', 'log.read', 'stats.read', 'alert.read', 'alert.manage', 'notification.read',
  'template.read', 'template.manage', 'channel.read', 'channel.write', 'dist.publish', 'dist.ops.read', 'dist.ops.write',
  'agent.token.read', 'agent.token.manage', 'agent.mcp.read', 'agent.calllog.read',
  'network.read', 'network.manage', 'player.read', 'player.manage', 'super.read', 'director.read',
]

const SEED_BY_ROLE_KEY: Record<string, string[]> = {
  platform_admin: [...ALL_NODES],
  group_admin: [
    'instance.read', 'instance.create', 'instance.write', 'instance.operate', 'instance.delete',
    'instance.launchspec.write', 'instance.business.write', 'file.read', 'file.write', 'terminal.access',
    'bot.read', 'bot.manage', 'backup.read', 'backup.write', 'schedule.read', 'schedule.write',
    'task.read', 'task.manage', 'monitor.read', 'log.read', 'stats.read', 'alert.read', 'alert.manage',
    'notification.read', 'player.read', 'player.manage', 'network.read', 'network.manage',
    'group.read', 'group.member.write', 'super.read', 'director.read',
  ],
  group_operator: [
    'instance.read', 'instance.write', 'instance.operate', 'instance.create', 'instance.delete',
    'file.read', 'file.write', 'terminal.access', 'bot.read', 'bot.manage',
    'backup.read', 'backup.write', 'schedule.read', 'schedule.write', 'task.read', 'task.manage',
    'monitor.read', 'log.read', 'stats.read', 'alert.read', 'notification.read',
    'player.read', 'network.read', 'super.read', 'director.read', 'group.read',
  ],
  group_viewer: [
    'instance.read', 'file.read', 'bot.read', 'backup.read', 'schedule.read', 'task.read',
    'monitor.read', 'log.read', 'stats.read', 'alert.read', 'notification.read',
    'player.read', 'network.read', 'group.read',
  ],
  member: [
    'instance.read', 'instance.write', 'instance.operate', 'file.read', 'file.write', 'terminal.access',
    'bot.read', 'bot.manage', 'backup.read', 'schedule.read', 'task.read',
    'monitor.read', 'log.read', 'notification.read', 'player.read', 'group.read',
  ],
}

const LEGACY_ROLE_KEY: Record<number, string> = {
  0: 'member',
  1: 'group_admin',
  2: 'group_operator',
  3: 'group_viewer',
  10: 'platform_admin',
}

const CATALOG = [
  {
    domain: 'platform',
    label: '平台',
    nodes: [
      { id: 'user.read', label: '用户读取' },
      { id: 'user.manage', label: '用户管理' },
      { id: 'group.read', label: '用户组读取' },
      { id: 'group.manage', label: '用户组管理' },
      { id: 'group.member.write', label: '组成员写入' },
      { id: 'group.quota.write', label: '配额写入' },
      { id: 'node.read', label: '节点读取' },
      { id: 'node.manage', label: '节点管理' },
      { id: 'settings.read', label: '设置读取' },
      { id: 'settings.write', label: '设置写入' },
      { id: 'system.update', label: '系统更新' },
      { id: 'system.db.browse', label: '数据库浏览' },
      { id: 'license.read', label: '开源许可' },
      { id: 'audit.read', label: '审计读取' },
      { id: 'rbac.read', label: '权限配置读取' },
      { id: 'rbac.manage', label: '权限配置管理' },
    ],
  },
  {
    domain: 'runtime',
    label: '运行时',
    nodes: [
      { id: 'instance.read', label: '实例读取' },
      { id: 'instance.create', label: '创建实例' },
      { id: 'instance.write', label: '实例写入' },
      { id: 'instance.operate', label: '实例启停' },
      { id: 'instance.delete', label: '删除实例' },
      { id: 'instance.launchspec.write', label: '启动规格 / 环境变量', risk: true },
      { id: 'instance.business.write', label: '业务高危写', risk: true },
      { id: 'file.read', label: '文件读取' },
      { id: 'file.write', label: '文件写入' },
      { id: 'terminal.access', label: '终端访问' },
      { id: 'bot.read', label: 'Bot 读取' },
      { id: 'bot.manage', label: 'Bot 管理' },
      { id: 'backup.read', label: '备份读取' },
      { id: 'backup.write', label: '备份写入' },
      { id: 'schedule.read', label: '定时读取' },
      { id: 'schedule.write', label: '定时写入' },
      { id: 'task.read', label: '任务中心' },
      { id: 'task.manage', label: '任务管理' },
    ],
  },
  {
    domain: 'observability',
    label: '观测',
    nodes: [
      { id: 'monitor.read', label: '监控' },
      { id: 'log.read', label: '日志' },
      { id: 'stats.read', label: '统计' },
      { id: 'alert.read', label: '告警读取' },
      { id: 'alert.manage', label: '告警管理' },
      { id: 'notification.read', label: '通知中心' },
    ],
  },
  {
    domain: 'distribution',
    label: '客户端分发',
    nodes: [
      { id: 'template.read', label: '模板读取' },
      { id: 'template.manage', label: '模板管理' },
      { id: 'channel.read', label: '分发频道' },
      { id: 'channel.write', label: '频道写入' },
      { id: 'dist.publish', label: '版本发布' },
      { id: 'dist.ops.read', label: '分发运维读取' },
      { id: 'dist.ops.write', label: '分发安全处置' },
    ],
  },
  {
    domain: 'agent',
    label: 'Agent',
    nodes: [
      { id: 'agent.token.read', label: 'Token 读取' },
      { id: 'agent.token.manage', label: 'Token 管理' },
      { id: 'agent.mcp.read', label: 'MCP 会话' },
      { id: 'agent.calllog.read', label: '调用流水' },
    ],
  },
  {
    domain: 'workspace',
    label: '工作区',
    nodes: [
      { id: 'network.read', label: '网络读取' },
      { id: 'network.manage', label: '网络管理' },
      { id: 'player.read', label: '玩家读取' },
      { id: 'player.manage', label: '玩家管理' },
      { id: 'super.read', label: '超级工作台' },
      { id: 'director.read', label: '导播台' },
    ],
  },
]

const roles = db<RbacRole>('rbac_roles', () => {
  const now = '2026-06-01T08:00:00Z'
  return [
    { id: 10, key: 'platform_admin', name: '平台管理员', description: '平台全量能力', isSystem: true, nodes: [...ALL_NODES], createdAt: now, updatedAt: now },
    { id: 1, key: 'group_admin', name: '组管理员', description: '组内全权 + 管理成员', isSystem: true, nodes: [...SEED_BY_ROLE_KEY.group_admin], createdAt: now, updatedAt: now },
    { id: 2, key: 'group_operator', name: '组运维', description: '实例运维操作', isSystem: true, nodes: [...SEED_BY_ROLE_KEY.group_operator], createdAt: now, updatedAt: now },
    { id: 3, key: 'group_viewer', name: '组只读分析', description: '只读实例/指标/日志', isSystem: true, nodes: [...SEED_BY_ROLE_KEY.group_viewer], createdAt: now, updatedAt: now },
    { id: 0, key: 'member', name: '组成员', description: '兼容历史成员', isSystem: true, nodes: [...SEED_BY_ROLE_KEY.member], createdAt: now, updatedAt: now },
  ]
})

const overrides = db<UserPermissionOverrideRow>('rbac_overrides', () => [])
const bindings = db<UserRoleBinding>('rbac_bindings', () => [
  { id: 1, userId: 1, roleId: 10 },
  { id: 2, userId: 2, roleId: 1 },
])

function currentSessionUser(info: { request: Request }): User | null {
  const token = info.request.headers.get('Authorization')?.replace(/^Bearer /, '')
  const session = token ? db<Session>('sessions').find((s) => s.accessToken === token) : undefined
  if (!session) return null
  return db<User>('users').get(session.userId) ?? null
}

function effectiveNodes(roleKey: string, roleNodes: string[], userOverrides: { node: string; effect: string }[], isPlatformAdmin: boolean): string[] {
  if (isPlatformAdmin || roleKey === 'platform_admin') return [...ALL_NODES]
  const set = new Set(roleNodes)
  for (const o of userOverrides) if (o.effect === 'allow') set.add(o.node)
  for (const o of userOverrides) if (o.effect === 'deny') set.delete(o.node)
  return [...set]
}

export const handlers = [
  domainRoute('get', '/auth/me', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const user = currentSessionUser(info)
    if (!user) return HttpResponse.json({ error: 'UNAUTHORIZED', message: '未授权' }, { status: 401 })
    const roleKey = LEGACY_ROLE_KEY[user.role] ?? 'member'
    const binding = bindings.find((b) => b.userId === user.id)
    const role = binding ? roles.get(binding.roleId) : undefined
    const key = role?.key ?? roleKey
    const isPlatformAdmin = user.role === 10 || key === 'platform_admin'
    const userOverrides = overrides.list((o) => o.userId === user.id)
    const nodes = effectiveNodes(key, role?.nodes ?? SEED_BY_ROLE_KEY[key] ?? [], userOverrides, isPlatformAdmin)
    return HttpResponse.json({
      userId: user.id,
      username: user.username,
      role: user.role,
      roleKey: key,
      isPlatformAdmin,
      nodes,
    })
  }),

  domainRoute('get', '/rbac/catalog', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({ domains: CATALOG })
  }),

  domainRoute('get', '/rbac/roles', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json({ items: roles.list() })
  }),

  domainRoute('post', '/rbac/roles', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const body = (await info.request.json()) as { name: string; description?: string; nodes?: string[] }
    const id = Math.max(100, ...roles.list().map((r) => r.id)) + 1
    const now = new Date().toISOString()
    const role = roles.insert({
      id,
      key: `custom_${id}`,
      name: body.name,
      description: body.description ?? '',
      isSystem: false,
      nodes: body.nodes ?? [],
      createdAt: now,
      updatedAt: now,
    })
    return HttpResponse.json(role, { status: 201 })
  }),

  domainRoute('patch', '/rbac/roles/:id', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const existing = roles.get(id)
    if (!existing) return HttpResponse.json({ error: 'NOT_FOUND', message: '角色不存在' }, { status: 404 })
    const body = (await info.request.json()) as { name?: string; description?: string; nodes?: string[] }
    roles.update(id, {
      name: body.name ?? existing.name,
      description: body.description ?? existing.description,
      nodes: body.nodes ?? existing.nodes,
      updatedAt: new Date().toISOString(),
    })
    return HttpResponse.json(roles.get(id))
  }),

  domainRoute('delete', '/rbac/roles/:id', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const existing = roles.get(id)
    if (!existing) return HttpResponse.json({ error: 'NOT_FOUND', message: '角色不存在' }, { status: 404 })
    if (existing.isSystem) return HttpResponse.json({ error: 'FORBIDDEN', message: '系统角色不可删除' }, { status: 403 })
    roles.remove(id)
    return HttpResponse.json({ ok: true })
  }),

  domainRoute('put', '/rbac/roles/:id/permissions', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const existing = roles.get(id)
    if (!existing) return HttpResponse.json({ error: 'NOT_FOUND', message: '角色不存在' }, { status: 404 })
    const body = (await info.request.json()) as { nodes: string[] }
    roles.update(id, { nodes: body.nodes ?? [], updatedAt: new Date().toISOString() })
    return HttpResponse.json({ ok: true })
  }),

  domainRoute('get', '/rbac/users/:id/permissions', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const userId = Number(info.params.id)
    const user = db<User>('users').get(userId)
    if (!user) return HttpResponse.json({ error: 'NOT_FOUND', message: '用户不存在' }, { status: 404 })
    const binding = bindings.find((b) => b.userId === userId)
    const role = binding ? roles.get(binding.roleId) : undefined
    const roleKey = role?.key ?? LEGACY_ROLE_KEY[user.role] ?? 'member'
    const isPlatformAdmin = user.role === 10 || roleKey === 'platform_admin'
    const userOverrides = overrides.list((o) => o.userId === userId)
    const nodes = effectiveNodes(roleKey, role?.nodes ?? SEED_BY_ROLE_KEY[roleKey] ?? [], userOverrides, isPlatformAdmin)
    return HttpResponse.json({
      userId,
      roleKey,
      roleId: role?.id,
      nodes,
      overrides: userOverrides.map((o) => ({ node: o.node, effect: o.effect })),
      isPlatformAdmin,
    })
  }),

  domainRoute('put', '/rbac/users/:id/role', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const userId = Number(info.params.id)
    const body = (await info.request.json()) as { roleId: number }
    const existing = bindings.find((b) => b.userId === userId)
    if (existing) bindings.update(existing.id, { roleId: body.roleId })
    else bindings.insert({ userId, roleId: body.roleId })
    return HttpResponse.json({ ok: true })
  }),

  domainRoute('put', '/rbac/users/:id/overrides', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const userId = Number(info.params.id)
    const body = (await info.request.json()) as { overrides: { node: string; effect: string }[] }
    for (const row of overrides.list((o) => o.userId === userId)) overrides.remove(row.id)
    for (const o of body.overrides ?? []) {
      overrides.insert({ userId, node: o.node, effect: o.effect as 'allow' | 'deny' })
    }
    return HttpResponse.json({ ok: true })
  }),
]
