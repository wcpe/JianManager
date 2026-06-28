import { HttpResponse } from 'msw'
import { db } from './db'
import type { Session } from './handlers/domains/auth'

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
