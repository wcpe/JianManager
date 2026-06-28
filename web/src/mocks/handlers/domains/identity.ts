import { HttpResponse } from 'msw'
import { domainRoute } from '@/mocks/inject'
import { requireAuth } from '@/mocks/auth-middleware'
import { db } from '@/mocks/db'
import type { User } from '@/mocks/handlers/domains/auth'

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
  createdAt: string
  user?: { id: number; username: string }
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
    createdAt: '2026-06-28T10:10:00Z',
    user: { id: 2, username: 'operator' },
  },
])

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

  // --- auth/register（公开：CreateUserDialog 先注册再按需升角色）---
  domainRoute('post', '/auth/register', async ({ request }) => {
    const { username, password } = (await request.json()) as { username: string; password: string }
    if (users.find((x) => x.username === username)) {
      return HttpResponse.json({ error: 'CONFLICT', message: 'username 已存在' }, { status: 409 })
    }
    const uuid = `u-${username}-${users.list().length + 1}`
    const u = users.insert({ uuid, username, password, role: 0 })
    return HttpResponse.json(
      { id: u.uuid, username: u.username, createdAt: '2026-06-28T12:00:00Z' },
      { status: 201 },
    )
  }),

  // --- users ---
  domainRoute('get', '/users', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    return HttpResponse.json(users.list().map(toUserInfo))
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

  // --- audit（支持 userId/action/targetType/from/to/limit 筛选）---
  domainRoute('get', '/audit', (info) => {
    const denied = requireAuth(info)
    if (denied) return denied
    const url = new URL(info.request.url)
    const userId = url.searchParams.get('userId')
    const action = url.searchParams.get('action')
    const targetType = url.searchParams.get('targetType')
    const from = url.searchParams.get('from')
    const to = url.searchParams.get('to')
    const limit = Number(url.searchParams.get('limit') ?? '100')

    let rows = audit.list()
    if (userId) rows = rows.filter((r) => String(r.userId) === userId)
    if (action) rows = rows.filter((r) => r.action.includes(action))
    if (targetType) rows = rows.filter((r) => r.targetType === targetType)
    if (from) rows = rows.filter((r) => r.createdAt >= from)
    if (to) rows = rows.filter((r) => r.createdAt <= to)
    // 默认按时间倒序（新在前），与「时间线」语义一致。
    rows = rows.sort((a, b) => b.createdAt.localeCompare(a.createdAt)).slice(0, limit)
    return HttpResponse.json(rows)
  }),
]

/**
 * 播种本域独有集合（groups/audit）。users 由地基 auth.ts 播种，此处不碰。
 * handlers/index.ts 聚合时调用；resetDb 经集合自身 reset 重播。
 */
export function seed(): void {
  groups.seed()
  audit.seed()
}
