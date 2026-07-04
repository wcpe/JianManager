import { HttpResponse } from 'msw'
import { db } from './db'
import type { Session, User } from './handlers/domains/auth'

/**
 * 鉴权中间件（FR-197）。受保护 handler 首行调用：
 *   const denied = requireAuth(info); if (denied) return denied
 * 校验 Authorization: Bearer <token> 是否对应一个有效 session（login 时写入）。
 * 公共端点（/auth/login、/auth/refresh、/setup/*）不调用。
 */
export function requireAuth(info: { request: Request }): Response | null {
  const token = info.request.headers.get('Authorization')?.replace(/^Bearer /, '')
  if (!token || !db<Session>('sessions').find((s) => s.accessToken === token)) {
    return HttpResponse.json({ error: 'UNAUTHORIZED', message: '未授权' }, { status: 401 })
  }
  return null
}

/** 平台管理员守卫：role=10 才可访问平台级资源；未登录仍返回 401，已登录但权限不足返回 403。 */
export function requirePlatformAdmin(info: { request: Request }): Response | null {
  const denied = requireAuth(info)
  if (denied) return denied
  const token = info.request.headers.get('Authorization')?.replace(/^Bearer /, '')
  const session = db<Session>('sessions').find((s) => s.accessToken === token)
  const user = session ? db<User>('users').get(session.userId) : undefined
  if (!user || user.role < 10) {
    return HttpResponse.json({ error: 'FORBIDDEN', message: '权限不足' }, { status: 403 })
  }
  return null
}
