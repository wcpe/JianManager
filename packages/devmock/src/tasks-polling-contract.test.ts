import { describe, it, expect, beforeAll, afterAll, afterEach } from 'vitest'
import { server } from '@jianmanager/devmock/server'
import { resetDb } from '@jianmanager/devmock/db'

/**
 * 任务列表的「活跃态会收敛」契约（FR-329 轮询能停）。
 *
 * 存在的理由：前端 `api/tasks.ts` 的 `tasksRefetchInterval` 是「**只要有一条非终态任务
 * 就一直 2s 轮询**」。而旧的任务种子按状态池轮转生成，large 档 1500 条里就有 600 条被种成
 * pending/running——它们靠 `advanceTaskState` 按时间推导回 succeeded，代价却是**每个**
 * `/tasks` 请求都要遍历全量重算，而 MSW 跑在浏览器主线程上，等于把这份成本转嫁给了页面
 * （实测静止无操作时每 2s 触发约 196ms 长任务）。
 *
 * 这里守两件**互相拉扯**的事，缺一个都是回归：
 * 1. 首窗**必须能取到**活跃任务——否则任务中心与顶栏看板看不到「有任务在跑」；
 * 2. 活跃任务**只能是近窗口的少数几条**——否则轮询停不下来、每个请求都要全量重算。
 */
const BASE = 'http://localhost/api/v1'
const TERMINAL = new Set(['succeeded', 'failed', 'canceled'])
/** 近窗口内允许非终态的批量任务条数；加上手工种子里那条 running，首窗最多 4 条活跃。 */
const MAX_ACTIVE_IN_FIRST_WINDOW = 4

interface TaskRow {
  taskId: string
  state: string
  createdAt: string
}

async function login(): Promise<string> {
  const r = await fetch(`${BASE}/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: 'admin', password: 'admin123' }),
  })
  expect(r.status).toBe(200)
  return ((await r.json()) as { accessToken: string }).accessToken
}

async function fetchTasks(token: string, limit: number): Promise<{ items: TaskRow[]; total: number }> {
  const r = await fetch(`${BASE}/tasks?limit=${limit}`, { headers: { Authorization: `Bearer ${token}` } })
  expect(r.status).toBe(200)
  return (await r.json()) as { items: TaskRow[]; total: number }
}

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => {
  server.resetHandlers()
  resetDb()
})
afterAll(() => server.close())

describe('任务列表活跃态收敛（FR-329）', () => {
  it('首窗能取到活跃任务，且只有近窗口的少数几条', async () => {
    const token = await login()
    const page = await fetchTasks(token, 100)
    const active = page.items.filter((t) => !TERMINAL.has(t.state))
    expect(active.length).toBeGreaterThan(0)
    expect(active.length).toBeLessThanOrEqual(MAX_ACTIVE_IN_FIRST_WINDOW)
  })

  it('活跃任务都落在 90s 活跃窗口内，到期即 succeeded（轮询自然停）', async () => {
    const token = await login()
    const page = await fetchTasks(token, 100)
    const active = page.items.filter((t) => !TERMINAL.has(t.state))
    const now = Date.now()
    for (const t of active) {
      expect(now - new Date(t.createdAt).getTime()).toBeLessThan(90_000)
    }
  })

  it('不把历史批量任务种成永久活跃：首窗活跃占比远低于一成', async () => {
    const token = await login()
    const page = await fetchTasks(token, 100)
    expect(page.items.length).toBeGreaterThan(0)
    const active = page.items.filter((t) => !TERMINAL.has(t.state))
    expect(active.length / page.items.length).toBeLessThan(0.1)
  })

  it('历史批量任务一律终态，且成功占多数（贴合任务中心的真实观感）', async () => {
    const token = await login()
    const page = await fetchTasks(token, 500)
    // 跳过手工种子 4 条 + 近窗口刻意保留的活跃任务 3 条，只看「历史批量任务」部分
    const generated = page.items.slice(4 + 3)
    expect(generated.length).toBeGreaterThan(0)
    expect(generated.every((t) => TERMINAL.has(t.state))).toBe(true)
    const succeeded = generated.filter((t) => t.state === 'succeeded').length
    expect(succeeded / generated.length).toBeGreaterThan(0.5)
  })
})
