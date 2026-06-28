import { db } from '@/mocks/db'
import type { Session } from '@/mocks/handlers/domains/auth'

/**
 * 让后续渲染的页面处于已登录态（FR-196 测试工具）。
 * 在假后端 sessions 放一个会话并让 api 客户端带上其 token，使受 requireAuth 保护的
 * 端点放行。页面 *.dom.test.tsx 渲染受保护页前调用一次。token 非 JWT 无妨——
 * requireAuth 仅按 sessions 匹配；decodeJwt 解不出 exp 时按未过期处理，不会触发刷新。
 */
export function loginMockUser(token = 'test-access-token'): void {
  db<Session>('sessions').insert({ accessToken: token, refreshToken: 'test-refresh-token', userId: 1 })
  localStorage.setItem('accessToken', token)
}
