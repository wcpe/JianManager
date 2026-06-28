import { HttpResponse } from 'msw'
import { domainRoute } from '@/mocks/inject'
import { db } from '@/mocks/db'

/**
 * 身份访问域 mock handler（地基随 FR-199 交付的纵切样例）。
 * 其余域簇（FR-200~210）照本文件结构在 domains/ 各加一个文件，详见 spec §7。
 */

/** 假后端用户。password 明文仅 mock 用；role：0 组员 / 1 组管理员 / 10 平台管理员（与后端 model.UserRole 对齐）。 */
export interface User {
  id: number
  uuid: string
  username: string
  password: string
  role: number
  disabled?: boolean
}

/** 登录会话：token→user 映射，供 requireAuth 校验跨 endpoint 联动。 */
export interface Session {
  id: number
  accessToken: string
  refreshToken: string
  userId: number
}

// 集合在所属域 handler 模块顶层带 seedFn 唯一声明（import 即播种，resetDb 重播）。
const users = db<User>('users', () => [
  { id: 1, uuid: 'u-admin', username: 'admin', password: 'admin123', role: 10 },
  { id: 2, uuid: 'u-op', username: 'operator', password: 'op123456', role: 1 },
])
const sessions = db<Session>('sessions', () => [])

/** 简化 JWT：mock 不验签，payload 内嵌 role/username/exp 供前端 decodeJwt 读取（lib/jwt.ts）。 */
function fakeJwt(u: User): string {
  const payload = btoa(
    JSON.stringify({ userId: u.id, username: u.username, role: u.role, exp: Math.floor(Date.now() / 1000) + 900 }),
  )
  return `mock.${payload}.sig`
}

export const handlers = [
  // setup 状态：mock 默认「已初始化」，使 LoginPage 渲染登录表单（FR-199 可注入 setupRequired:true 验引导跳转）。
  domainRoute('get', '/setup/status', () => HttpResponse.json({ setupRequired: false })),

  domainRoute('post', '/auth/login', async ({ request }) => {
    const { username, password } = (await request.json()) as { username: string; password: string }
    const u = users.find((x) => x.username === username && x.password === password && !x.disabled)
    if (!u) return HttpResponse.json({ error: 'UNAUTHORIZED', message: '用户名或密码错误' }, { status: 401 })
    const s = sessions.insert({
      accessToken: fakeJwt(u),
      refreshToken: `r-${u.id}-${sessions.list().length + 1}`,
      userId: u.id,
    })
    return HttpResponse.json({ accessToken: s.accessToken, refreshToken: s.refreshToken, expiresIn: 900 })
  }),

  domainRoute('post', '/auth/refresh', async ({ request }) => {
    const { refreshToken } = (await request.json()) as { refreshToken: string }
    const s = sessions.find((x) => x.refreshToken === refreshToken)
    if (!s) return HttpResponse.json({ error: 'UNAUTHORIZED', message: 'refreshToken 无效或已过期' }, { status: 401 })
    const u = users.get(s.userId)
    if (u) sessions.update(s.id, { accessToken: fakeJwt(u) })
    return HttpResponse.json({ accessToken: s.accessToken, refreshToken: s.refreshToken, expiresIn: 900 })
  }),
]
