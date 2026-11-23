import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { server } from './server'
import { db, resetDb } from './db'
import { clearInjections } from './inject'

const BASE = 'http://localhost/api/v1'
let auth: Record<string, string>
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterAll(() => server.close())
afterEach(() => { server.resetHandlers(); resetDb(); clearInjections() })
beforeEach(async () => {
  const response = await fetch(`${BASE}/auth/login`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: 'admin', password: 'admin123' }) })
  const body = await response.json() as { accessToken: string }
  auth = { Authorization: `Bearer ${body.accessToken}` }
})
interface Page {
  items: { id: number }[]
  view: { view_id: string }
  next_cursor?: string
}
async function search(params: Record<string, string>): Promise<Page> {
  const response = await fetch(`${BASE}/logs/federation?${new URLSearchParams(params)}`, { headers: auth })
  expect(response.status).toBe(200)
  return await response.json() as Page
}
async function exportRows(params: Record<string, string>) {
  return fetch(`${BASE}/logs/federation/export?${new URLSearchParams(params)}`, { method: 'POST', headers: auth })
}

describe('日志mock必须验证scope/view而非恒成功', () => {
  it('同view固定页、整窗真实区分，并拒绝错配scope/入页游标/页大小', async () => {
    const collection = db<{ id: number; source: string; level: string; message: string; time: string; nodeId: number; instanceId: number }>('logs')
    for (const row of collection.list()) collection.remove(row.id)
    for (let index = 0; index < 161; index++) collection.insert({ id: index + 1, source: 'instance', level: 'info', message: `mock-${index}`, time: new Date(Date.now() - index).toISOString(), nodeId: 1, instanceId: 1 })
    db<{ id: number; status: number }>('nodes').insert({ id: 99, status: 0 })
    collection.insert({ id: 1000, source: 'instance', level: 'info', message: 'mock-offline', time: new Date().toISOString(), nodeId: 99, instanceId: 99 })
    const scope = { view: 'node_instance', onlineOnly: 'true', keyword: 'mock-', limit: '100' }
    const first = await search(scope)
    expect(first.items).toHaveLength(100)
    const next = await search({ ...scope, viewId: first.view.view_id, cursor: first.next_cursor! })
    expect(next.items).toHaveLength(61)
    const page = await exportRows({ ...scope, viewId: first.view.view_id, cursor: first.next_cursor!, range: 'page' })
    expect(page.status).toBe(200)
    expect((await page.text()).split('\n').filter(Boolean)).toHaveLength(61)
    const all = await exportRows({ ...scope, viewId: first.view.view_id, cursor: first.next_cursor!, range: 'all', limit: '1000000' })
    expect(all.status).toBe(200)
    expect((await all.text()).split('\n').filter(Boolean)).toHaveLength(161)
    expect((await exportRows({ ...scope, viewId: first.view.view_id, range: 'page', onlineOnly: 'false' })).status).toBe(409)
    expect((await exportRows({ ...scope, viewId: first.view.view_id, range: 'page', cursor: 'forged-cursor' })).status).toBe(400)
    expect((await exportRows({ ...scope, viewId: first.view.view_id, range: 'page', limit: '99' })).status).toBe(409)
    expect((await exportRows({ ...scope, range: 'page' })).status).toBe(400)
    expect((await exportRows({ ...scope, range: 'unexpected' })).status).toBe(400)
    const unfiltered = await exportRows({ view: 'node_instance', keyword: 'mock-', range: 'all' })
    expect((await unfiltered.text()).split('\n').filter(Boolean)).toHaveLength(162)
  })

  it('联邦严格[from,to)包含from排除to，经典/logs仍保留闭区间', async () => {
    const from = '2026-10-21T10:00:00.000Z'
    const to = '2026-10-21T10:00:01.000Z'
    const rows = [from, '2026-10-21T10:00:00.500Z', to].map((time, index) => ({
      id: index + 1, source: 'instance', level: 'info', message: `boundary-${index}`, time, nodeId: 1, instanceId: 1,
      event_id: `boundary-event-${index}`, fields: { original: `UTC原始边界-${index}` },
    }))
    const collection = db<(typeof rows)[number]>('logs')
    for (const row of collection.list()) collection.remove(row.id)
    for (const row of rows) collection.insert(row)
    const scope = { view: 'node_instance', from, to, limit: '100' }
    const expected = [rows[1], rows[0]]
    const page = await search(scope)
    expect(page.items).toEqual(expected)
    const artifact = await exportRows({ ...scope, viewId: page.view.view_id, range: 'all' })
    expect(artifact.status).toBe(200)
    const exported = (await artifact.text()).split('\n').filter(Boolean).map((line) => JSON.parse(line) as { id: number; event_id: string })
    expect(exported).toEqual(expected)
    expect(exported).toHaveLength(2)
    expect(exported.map((row) => row.event_id)).toEqual(['boundary-event-1', 'boundary-event-0'])
    expect(exported).not.toContainEqual(rows[2])
    const tail = await fetch(`${BASE}/logs/federation/tail?${new URLSearchParams(scope)}`, { headers: auth })
    expect(((await tail.json()) as Page).items).toEqual(expected)
    const stats = await fetch(`${BASE}/logs/federation/stats?${new URLSearchParams({ ...scope, viewId: page.view.view_id })}`, { headers: auth })
    expect(await stats.json()).toMatchObject({ points: [{ agg: { count: 2 } }] })
    const facets = await fetch(`${BASE}/logs/federation/facets?${new URLSearchParams({ ...scope, viewId: page.view.view_id })}`, { headers: auth })
    expect(await facets.json()).toMatchObject({ dimensions: [{ values: [{ value: 'INFO', count: 2 }] }] })
    const classic = await fetch(`${BASE}/logs?${new URLSearchParams({ source: 'instance', from, to })}`, { headers: auth })
    expect(((await classic.json()) as Page).items).toEqual([rows[2], rows[1], rows[0]])
  })

  it('固定view聚合与授权复核同scope，权限撤销不会给成功附件', async () => {
    const scope = { view: 'node_instance', limit: '100' }
    const page = await search(scope)
    const query = new URLSearchParams({ view: 'node_instance', viewId: page.view.view_id })
    const stats = await fetch(`${BASE}/logs/federation/stats?${query}`, { headers: auth })
    expect(stats.status).toBe(200)
    expect(await stats.json()).toMatchObject({ coverage: { complete: true }, quality: { duplicate_quality: 'exact', stats_quality: 'exact' }, truncated: false })
    query.set('onlineOnly', 'true')
    expect((await fetch(`${BASE}/logs/federation/facets?${query}`, { headers: auth })).status).toBe(409)
    const sessions = db<{ id: number; accessToken: string }>('sessions')
    for (const session of sessions.list()) sessions.remove(session.id)
    expect((await exportRows({ ...scope, viewId: page.view.view_id, range: 'page' })).status).toBe(401)
  })
})
