import { describe, it, expect, beforeAll, afterAll, afterEach } from 'vitest'
import { server } from '@jianmanager/devmock/server'
import { resetDb } from '@jianmanager/devmock/db'

/**
 * QQ 扫码接入（FR-495）三端点的 mock 契约测试。
 *
 * 存在的理由：`GET /alerts/qq/groups` 的真实后端（internal/controlplane/router/alert.go，
 * docs/API.md 同）返回分页信封 `{items,total}`，而 devmock 曾返回纯数组——前端
 * `normalizeQQDiscoveredGroups` 的双兼容因此只走到数组分支，信封形状从未被真实形状覆盖。
 * 这里钉住 mock 与后端同形状，并顺带守住「响应不含 appSecret」这条密钥安全验收。
 */

const BASE = 'http://localhost/api/v1'

async function login(): Promise<string> {
  const r = await fetch(`${BASE}/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: 'admin', password: 'admin123' }),
  })
  expect(r.status).toBe(200)
  return ((await r.json()) as { accessToken: string }).accessToken
}

const auth = (token: string) => ({ Authorization: `Bearer ${token}` })

/** 已发现群条目（对齐前端 api/alerts.ts QQDiscoveredGroup 与后端 JSON tag）。 */
interface DiscoveredGroup {
  id: number
  groupOpenid: string
  opMemberOpenid: string
  firstSeenAt: string
  lastSeenAt: string
  sourceAppId: string
}

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => {
  server.resetHandlers()
  resetDb()
})
afterAll(() => server.close())

describe('QQ 接入 mock 契约（FR-495）', () => {
  it('GET /alerts/qq/groups 返回 {items,total} 分页信封（非纯数组）', async () => {
    const token = await login()
    const r = await fetch(`${BASE}/alerts/qq/groups`, { headers: auth(token) })
    expect(r.status).toBe(200)

    const body = (await r.json()) as { items: DiscoveredGroup[]; total: number }
    // 形状闸：纯数组会让前端的信封解析分支彻底失去覆盖。
    expect(Array.isArray(body)).toBe(false)
    expect(Array.isArray(body.items)).toBe(true)
    expect(body.total).toBe(body.items.length)
    expect(body.items).toHaveLength(2)
    expect(body.items[0]).toMatchObject({ groupOpenid: 'GROUP_OPENID_DEMO_1', sourceAppId: '102000001' })
  })

  it('GET /alerts/qq/gateway/status 与 POST /alerts/qq/share-link 响应不含 appSecret', async () => {
    const token = await login()

    const statusRes = await fetch(`${BASE}/alerts/qq/gateway/status`, { headers: auth(token) })
    expect(statusRes.status).toBe(200)
    const statusBody = (await statusRes.json()) as Record<string, unknown>
    expect(statusBody).toMatchObject({ appId: '102000001', status: 'connected' })
    expect(Object.keys(statusBody)).not.toContain('appSecret')

    const linkRes = await fetch(`${BASE}/alerts/qq/share-link`, {
      method: 'POST',
      headers: { ...auth(token), 'Content-Type': 'application/json' },
      body: JSON.stringify({}),
    })
    expect(linkRes.status).toBe(200)
    const linkBody = (await linkRes.json()) as { url: string }
    expect(linkBody.url).toContain('qunshare')
    expect(JSON.stringify(linkBody)).not.toContain('appSecret')
  })
})

/**
 * FR-494 接入体验改造：扫码绑定的两个假端点。
 *
 * 存在的理由：前端「扫码 → 自动填表」整条链路只在 mock 上跑得起来，mock 的形状一旦偏离
 * 后端契约（qrUrl 缺 task_id、completed 缺 secretEnv、密钥明文外泄），前端的回归就全成了
 * 自证。这里钉住形状 + 钉住「响应绝不含 appSecret 明文」这条密钥安全验收。
 */
describe('QQ 扫码绑定 mock 契约（FR-494）', () => {
  /** 建一个绑定任务，返回其 taskId（每个用例都从零开始：afterEach 已 resetDb）。 */
  async function createBindTask(token: string): Promise<{ taskId: string; qrUrl: string }> {
    const r = await fetch(`${BASE}/alerts/qq/bind-task`, {
      method: 'POST',
      headers: { ...auth(token), 'Content-Type': 'application/json' },
    })
    expect(r.status).toBe(200)
    return (await r.json()) as { taskId: string; qrUrl: string }
  }

  it('POST /alerts/qq/bind-task 返回 taskId 与「连接页」qrUrl，响应不含 appSecret', async () => {
    const token = await login()
    const body = await createBindTask(token)

    expect(body.taskId).toBeTruthy()
    // qrUrl 形状与后端一致：官方连接页 + task_id + source=JianManager + _wv=2。
    expect(body.qrUrl).toBe(
      `https://q.qq.com/qqbot/openclaw/connect.html?task_id=${body.taskId}&source=JianManager&_wv=2`,
    )
    expect(JSON.stringify(body)).not.toContain('appSecret')
  })

  it('GET /alerts/qq/bind-task/:taskId 先 pending 后 completed；completed 只给 secretEnv', async () => {
    const token = await login()
    const created = await createBindTask(token)

    const first = await fetch(`${BASE}/alerts/qq/bind-task/${created.taskId}`, { headers: auth(token) })
    expect(first.status).toBe(200)
    // 首次 pending：让前端「等待扫码」分支有真实数据可跑。
    expect(await first.json()).toEqual({ status: 'pending' })

    const second = await fetch(`${BASE}/alerts/qq/bind-task/${created.taskId}`, { headers: auth(token) })
    expect(second.status).toBe(200)
    const done = (await second.json()) as Record<string, unknown>
    expect(done).toMatchObject({
      status: 'completed',
      appId: '102000001',
      userOpenid: 'USER_OPENID_DEMO_1',
      // 引用名的形状（带连字符）正是后端放宽 ${ENV} 字符集的原因，前端据此填表单。
      secretEnv: '${QQ-102000001}',
    })
    // 密钥安全：只给引用名，绝不给明文。
    expect(Object.keys(done)).not.toContain('appSecret')
  })

  it('未知 taskId → 404 BIND_TASK_EXPIRED（前端据此自动重新申请二维码）', async () => {
    const token = await login()
    const r = await fetch(`${BASE}/alerts/qq/bind-task/not-a-task`, { headers: auth(token) })
    expect(r.status).toBe(404)
    expect(await r.json()).toMatchObject({ error: 'BIND_TASK_EXPIRED' })
  })

  it('两个端点都要求鉴权：无 token → 401', async () => {
    const created = await (async () => {
      const token = await login()
      return createBindTask(token)
    })()

    const create = await fetch(`${BASE}/alerts/qq/bind-task`, { method: 'POST' })
    expect(create.status).toBe(401)

    const poll = await fetch(`${BASE}/alerts/qq/bind-task/${created.taskId}`)
    expect(poll.status).toBe(401)
  })
})
