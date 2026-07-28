import { HttpResponse } from 'msw'
import { domainRoute } from '@jianmanager/devmock/inject'
import { requireAuth, requirePlatformAdmin } from '@jianmanager/devmock/auth-middleware'
import { db } from '@jianmanager/devmock/db'
import type { User } from '@jianmanager/devmock/handlers/domains/auth'

/**
 * 身份访问域 mock handler（FR-199）：setup / users / groups / audit。
 * 地基 auth.ts 已声明 users/sessions 集合并提供 /setup/status、/auth/login、/auth/refresh，
 * 本域不重定义这三者，仅追加 /setup、/auth/register 及 users/groups/audit 的全部端点。
 * 字段保真：返回结构匹配 web/src/api/{users,groups,audit}.ts 的 interface。
 */

/** 用户组成员（GroupMember，groups.ts）。 */
interface GroupMember {
  id: number
  userId: number
  role: number
  user?: { id: number; username: string }
}

/** 用户组配额（GroupQuota，groups.ts）。 */
interface GroupQuota {
  maxInstances: number
  maxBots: number
  maxStorageMb: number
}

/** 用户组（GroupInfo，groups.ts）。 */
interface Group {
  id: number
  uuid: string
  name: string
  description: string
  members: GroupMember[]
  quota: GroupQuota
  createdAt: string
}

/** 审计日志（AuditLogInfo，audit.ts）。 */
interface AuditLog {
  id: number
  uuid: string
  userId: number
  action: string
  targetType: string
  targetId: string
  detail: string
  ip: string
  /** FR-321：操作是否失败与失败错误内容。 */
  failed: boolean
  error: string
  createdAt: string
  user?: { id: number; username: string }
}

interface Invitation {
  id: number
  email: string
  role: 0
  expiresAt: string
  used: boolean
  usedAt?: string
  revoked: boolean
  createdBy: number
  emailSentAt?: string
  createdAt: string
  token: string
}

// users 集合由地基 auth.ts 带 seedFn 声明，本域只读写、绝不重复 seedFn（spec §7）。
const users = db<User>('users')

// groups/audit 为本域独有集合，在此唯一声明并播种。
const groups = db<Group>('groups', () => [
  {
    id: 1,
    uuid: 'g-default',
    name: '默认组',
    description: '系统默认用户组',
    members: [{ id: 1, userId: 1, role: 1, user: { id: 1, username: 'admin' } }],
    quota: { maxInstances: 10, maxBots: 50, maxStorageMb: 10240 },
    createdAt: '2026-06-01T08:00:00Z',
  },
  {
    id: 2,
    uuid: 'g-ops',
    name: '运营组',
    description: '日常运营团队',
    members: [{ id: 2, userId: 2, role: 0, user: { id: 2, username: 'operator' } }],
    quota: { maxInstances: 5, maxBots: 20, maxStorageMb: 5120 },
    createdAt: '2026-06-02T09:30:00Z',
  },
])

const audit = db<AuditLog>('audit', () => [
  {
    id: 1,
    uuid: 'a-1',
    userId: 1,
    action: 'user.login',
    targetType: 'user',
    targetId: '1',
    detail: '{"ua":"mock"}',
    ip: '127.0.0.1',
    failed: false,
    error: '',
    createdAt: '2026-06-28T10:00:00Z',
    user: { id: 1, username: 'admin' },
  },
  {
    id: 2,
    uuid: 'a-2',
    userId: 1,
    action: 'instance.start',
    targetType: 'instance',
    targetId: 'inst-001',
    detail: '{"reason":"manual"}',
    ip: '127.0.0.1',
    failed: true,
    error: '{"error":"MEM_GATE","message":"节点可用内存不足：已拒绝启动"}',
    createdAt: '2026-06-28T10:05:00Z',
    user: { id: 1, username: 'admin' },
  },
  {
    id: 3,
    uuid: 'a-3',
    userId: 2,
    action: 'group.create',
    targetType: 'group',
    targetId: '2',
    detail: '',
    ip: '10.0.0.2',
    failed: false,
    error: '',
    createdAt: '2026-06-28T10:10:00Z',
    user: { id: 2, username: 'operator' },
  },
])

const invitations = db<Invitation>('invitations', () => [])

/**
 * 把假后端 User 投影为前端 UserInfo（users.ts）。
 * 种子用户仅 {id,uuid,username,role}，此处补 UserInfo 所需的 status/createdAt
 * （disabled→status：禁用=1，启用=0；createdAt 缺省给固定值便于断言）。
 */
function toUserInfo(u: User): {
  id: number
  uuid: string
  username: string
  role: number
  status: number
  createdAt: string
} {
  return {
    id: u.id,
    uuid: u.uuid,
    username: u.username,
    role: u.role,
    status: u.disabled ? 1 : 0,
    createdAt: (u as User & { createdAt?: string }).createdAt ?? '2026-06-01T08:00:00Z',
  }
}

function notFound(): Response {
  return HttpResponse.json({ error: 'NOT_FOUND', message: '资源不存在' }, { status: 404 })
}

function filterAuditRows(url: URL): AuditLog[] {
  const userId = url.searchParams.get('userId')
  const action = url.searchParams.get('action')
  const targetType = url.searchParams.get('targetType')
  const from = url.searchParams.get('from')
  const to = url.searchParams.get('to')
  let rows = audit.list()
  if (userId) rows = rows.filter((r) => String(r.userId) === userId)
  if (action) rows = rows.filter((r) => r.action.includes(action))
  if (targetType) rows = rows.filter((r) => r.targetType === targetType)
  if (from) rows = rows.filter((r) => r.createdAt >= from)
  if (to) rows = rows.filter((r) => r.createdAt <= to)
  return rows.sort((a, b) => b.createdAt.localeCompare(a.createdAt))
}

export const handlers = [
  // --- setup（公开，无需 requireAuth；/setup/status 由地基 auth.ts 提供，此处只加 POST /setup）---
  domainRoute('post', '/setup', async ({ request }) => {
    const { username, password } = (await request.json()) as { username: string; password: string }
    // mock 内 setup 仅返回令牌（不真正落库管理员），令引导页提交后跳控制台。
    const u = users.insert({ uuid: `u-${username}`, username, password, role: 10 })
    return HttpResponse.json(
      { accessToken: `setup-token-${u.id}`, refreshToken: `setup-refresh-${u.id}`, expiresIn: 900 },
      { status: 201 },
    )
  }),

  // --- users ---
  domainRoute('post', '/users', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const { username, password, role, status } = (await info.request.json()) as { username: string; password: string; role: number; status: number }
    if (users.find((x) => x.username === username)) {
      return HttpResponse.json({ error: 'CONFLICT', message: 'username 已存在' }, { status: 409 })
    }
    const uuid = `u-${username}-${users.list().length + 1}`
    const u = users.insert({ uuid, username, password, role, disabled: status !== 0 })
    return HttpResponse.json(toUserInfo(u), { status: 201 })
  }),

  domainRoute('get', '/users', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    // FR-336 双形态（镜像 router/user.go）：q 用户名模糊；带 limit 返 {items,total,limit,offset} 信封，
    // 不带 limit 返旧裸数组（UsersPage/AuditPage 等既有消费方不破）。
    const url = new URL(info.request.url)
    const q = (url.searchParams.get('q') ?? '').trim().toLowerCase()
    let rows = users.list().map(toUserInfo)
    if (q) rows = rows.filter((u) => u.username.toLowerCase().includes(q))
    const limitRaw = url.searchParams.get('limit')
    if (limitRaw === null) return HttpResponse.json(rows)
    const limit = Math.min(Math.max(Number(limitRaw) || 1, 1), 500)
    const offset = Math.max(Number(url.searchParams.get('offset')) || 0, 0)
    return HttpResponse.json({ items: rows.slice(offset, offset + limit), total: rows.length, limit, offset })
  }),

  domainRoute('put', '/users/:id', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const patch = (await info.request.json()) as { role?: number; status?: number; password?: string }
    const u = users.get(id)
    if (!u) return notFound()
    const next: Partial<User> = {}
    if (patch.role !== undefined) next.role = patch.role
    if (patch.status !== undefined) next.disabled = patch.status !== 0
    if (patch.password) next.password = patch.password
    users.update(id, next)
    return HttpResponse.json(toUserInfo(users.get(id)!))
  }),

  domainRoute('delete', '/users/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    users.remove(Number(info.params.id))
    return new HttpResponse(null, { status: 204 })
  }),

  domainRoute('post', '/users/invitations', async (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const { email, sendEmail } = (await info.request.json()) as { email: string; sendEmail: boolean }
    const id = invitations.list().reduce((max, item) => Math.max(max, item.id), 0) + 1
    const token = `mock-invite-${id}`
    const invitation = invitations.insert({
      email,
      role: 0,
      expiresAt: '2026-08-04T00:00:00Z',
      used: false,
      revoked: false,
      createdBy: 1,
      emailSentAt: sendEmail ? '2026-07-28T00:00:00Z' : undefined,
      createdAt: '2026-07-28T00:00:00Z',
      token,
    })
    const { token: _, ...safeInvitation } = invitation
    return HttpResponse.json({ ...safeInvitation, invitationUrl: `https://example.test/invite#${token}`, emailDelivery: sendEmail ? 'sent' : 'not_configured' }, { status: 201 })
  }),

  domainRoute('get', '/users/invitations', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    return HttpResponse.json(invitations.list().map(({ token: _, ...invitation }) => invitation))
  }),

  domainRoute('delete', '/users/invitations/:id', (info) => {
    const denied = requirePlatformAdmin(info)
    if (denied) return denied
    const invitation = invitations.get(Number(info.params.id))
    if (!invitation) return notFound()
    if (invitation.used) return HttpResponse.json({ error: 'INVITATION_ALREADY_USED' }, { status: 409 })
    invitations.update(invitation.id, { revoked: true })
    return HttpResponse.json({ message: '已撤销' })
  }),

  domainRoute('post', '/auth/invitations/accept', async ({ request }) => {
    const { token, username, password } = (await request.json()) as { token: string; username: string; password: string }
    const invitation = invitations.find((item) => item.token === token && !item.used && !item.revoked)
    if (!invitation) return HttpResponse.json({ error: 'INVITATION_INVALID' }, { status: 401 })
    if (users.find((user) => user.username === username)) return HttpResponse.json({ error: 'USER_EXISTS' }, { status: 409 })
    const user = users.insert({ uuid: `u-${username}-${users.list().length + 1}`, username, password, role: 0 })
    invitations.update(invitation.id, { used: true, usedAt: '2026-07-28T00:00:00Z' })
    return HttpResponse.json({ id: user.id, username: user.username, createdAt: '2026-07-28T00:00:00Z' }, { status: 201 })
  }),

  // --- groups ---
  domainRoute('get', '/groups', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(groups.list())
  }),

  domainRoute('post', '/groups', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const { name, description } = (await info.request.json()) as { name: string; description?: string }
    const g = groups.insert({
      uuid: `g-${name}-${groups.list().length + 1}`,
      name,
      description: description ?? '',
      members: [],
      quota: { maxInstances: 0, maxBots: 0, maxStorageMb: 0 },
      createdAt: '2026-06-28T12:00:00Z',
    })
    return HttpResponse.json(g, { status: 201 })
  }),

  domainRoute('put', '/groups/:id', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const body = (await info.request.json()) as { name?: string; description?: string }
    const g = groups.get(id)
    if (!g) return notFound()
    const next: Partial<Group> = {}
    if (body.name !== undefined) next.name = body.name
    if (body.description !== undefined) next.description = body.description
    groups.update(id, next)
    return HttpResponse.json(groups.get(id))
  }),

  domainRoute('delete', '/groups/:id', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    groups.remove(Number(info.params.id))
    return new HttpResponse(null, { status: 204 })
  }),

  domainRoute('put', '/groups/:id/quota', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const body = (await info.request.json()) as Partial<GroupQuota>
    const g = groups.get(id)
    if (!g) return notFound()
    groups.update(id, { quota: { ...g.quota, ...body } })
    return HttpResponse.json(groups.get(id))
  }),

  domainRoute('post', '/groups/:id/members', async (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const { userId, role } = (await info.request.json()) as { userId: number; role?: number }
    const g = groups.get(id)
    if (!g) return notFound()
    const u = users.get(userId)
    const memberId = g.members.reduce((m, x) => Math.max(m, x.id), 0) + 1
    g.members.push({
      id: memberId,
      userId,
      role: role ?? 0,
      user: u ? { id: u.id, username: u.username } : undefined,
    })
    groups.update(id, { members: g.members })
    return HttpResponse.json(groups.get(id))
  }),

  domainRoute('delete', '/groups/:id/members/:userId', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const id = Number(info.params.id)
    const userId = Number(info.params.userId)
    const g = groups.get(id)
    if (!g) return notFound()
    groups.update(id, { members: g.members.filter((m) => m.userId !== userId) })
    return new HttpResponse(null, { status: 204 })
  }),

  // --- audit（支持 userId/action/targetType/from/to/page/pageSize/limit 筛选）---
  domainRoute('get', '/audit', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const rows = filterAuditRows(url)
    if (url.searchParams.has('page') || url.searchParams.has('pageSize')) {
      const page = Math.max(1, Number(url.searchParams.get('page') ?? '1'))
      const pageSize = Math.min(500, Math.max(1, Number(url.searchParams.get('pageSize') ?? '50')))
      const start = (page - 1) * pageSize
      return HttpResponse.json({ items: rows.slice(start, start + pageSize), total: rows.length, page, pageSize })
    }
    const limit = Number(url.searchParams.get('limit') ?? '100')
    return HttpResponse.json(rows.slice(0, limit))
  }),

  domainRoute('get', '/audit/export', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const rows = filterAuditRows(url)
    const body = rows.map((r) => JSON.stringify(r)).join('\n')
    return new HttpResponse(body, { headers: { 'Content-Type': 'application/x-ndjson' } })
  }),
]

/**
 * 播种本域独有集合（groups/audit）。users 由地基 auth.ts 播种，此处不碰。
 * handlers/index.ts 聚合时调用；resetDb 经集合自身 reset 重播。
 */
export function seed(): void {
  groups.seed()
  audit.seed()
  invitations.seed()
}
