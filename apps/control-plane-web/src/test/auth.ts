import { decodeJwt } from '@/lib/auth/jwt'
import { db } from '@jianmanager/devmock/db'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import type { Session } from '@jianmanager/devmock/handlers/domains/auth'
import { ALL_PERMISSION_NODE_IDS, DEFAULT_ROLE_NODES, ROLE_KEY_BY_VALUE, isPlatformAdmin } from '@/lib/shared/roles'

/**
 * 让后续渲染的页面处于已登录态（FR-196 测试工具）。
 * 在假后端 sessions 放一个会话并让 api 客户端带上其 token，使受 requireAuth 保护的
 * 端点放行。页面 *.dom.test.tsx 渲染受保护页前调用一次。
 *
 * FR-432：同时预置权限 store，避免写门禁（instance.operate / terminal.access）在
 * 未拉到 /auth/me 前把既有用例按钮误禁。非 JWT token（decode 不出 role）按平台管理员开放。
 */
export function loginMockUser(token = 'test-access-token'): void {
  const claims = decodeJwt(token)
  const role = claims?.role
  const userId = (claims as { userId?: number } | null)?.userId ?? 1
  db<Session>('sessions').insert({ accessToken: token, refreshToken: 'test-refresh-token', userId })
  useAuthStore.getState().login(token, 'test-refresh-token')
  if (role == null || isPlatformAdmin(role)) {
    usePermissionsStore.setState({
      nodes: new Set(ALL_PERMISSION_NODE_IDS),
      roleKey: 'platform_admin',
      isPlatformAdmin: true,
      loaded: true,
    })
  } else {
    usePermissionsStore.setState({
      nodes: new Set(DEFAULT_ROLE_NODES[role] ?? DEFAULT_ROLE_NODES[0]),
      roleKey: ROLE_KEY_BY_VALUE[role] ?? 'member',
      isPlatformAdmin: false,
      loaded: true,
    })
  }
}

/**
 * 构造仅供前端解码角色声明的测试 JWT（结构最小，不校验签名）。
 *
 * 角色得是真的 JWT 声明才解得出来——这是重点，见 `loginMockUserAs`。
 */
export function makeTestJwt(role: number): string {
  const b64url = (obj: Record<string, unknown>) =>
    btoa(JSON.stringify(obj)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  return `${b64url({ alg: 'HS256', typ: 'JWT' })}.${b64url({ userId: 1, username: 'tester', role })}.sig`
}

/**
 * 以指定角色登录（FR-059 危险操作门禁依赖 token 里的 `role` 声明）。
 *
 * `loginMockUser()` 的默认 token 不是 JWT，`decodeJwt` 解不出 role，而 `canRunDanger`
 * 对未登录（role=null）判为拒绝。这对「未登录」语义是对的，但渲染带 `scope` 的
 * 危险确认弹窗的用例必须给真实角色，否则弹窗一律落到越权态、输入框根本不渲染。
 */
export function loginMockUserAs(role: number): void {
  loginMockUser(makeTestJwt(role))
}
