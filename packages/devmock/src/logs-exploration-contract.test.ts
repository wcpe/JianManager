import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { server } from './server'
import { db, resetDb } from './db'
import { clearInjections } from './inject'

const BASE = 'http://localhost/api/v1'
const scope = { view: 'node_instance', source: 'instance', from: '2026-11-10T00:00:00.000Z', to: '2026-11-10T00:02:00.000Z', limit: '100' }
let auth: Record<string, string>
let viewId = ''
const rows = [
  { id: 1, instanceId: 1, stream: 'stdout', time: '2026-11-10T00:00:05.000Z', fields: { shard: 'blue', duration_ms: 2 } },
  { id: 2, instanceId: 1, stream: 'stdout', time: '2026-11-10T00:00:45.000Z', fields: { shard: 'blue', duration_ms: 8 } },
  { id: 3, instanceId: 1, stream: 'stderr', time: '2026-11-10T00:01:05.000Z', fields: { shard: 'red', duration_ms: 10 } },
  { id: 4, instanceId: 2, stream: 'stdout', time: '2026-11-10T00:01:30.000Z', fields: { shard: 'red', duration_ms: 20 } },
].map((row) => ({ ...row, source: 'instance', level: 'info', nodeId: 1, message: `event-${row.id}`, instanceUuid: `inst-${row.instanceId}` }))

async function login(username = 'admin', password = 'admin123') {
  const response = await fetch(`${BASE}/auth/login`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username, password }) })
  expect(response.status).toBe(200)
  return { Authorization: `Bearer ${((await response.json()) as { accessToken: string }).accessToken}` }
}
function get(path: string, extra: Record<string, string> = {}, headers = auth) {
  return fetch(`${BASE}/logs/federation${path}?${new URLSearchParams({ ...scope, viewId, ...extra })}`, { headers })
}

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterAll(() => server.close())
afterEach(() => { server.resetHandlers(); resetDb(); clearInjections() })
beforeEach(async () => {
  const collection = db<(typeof rows)[number]>('logs')
  for (const row of collection.list()) collection.remove(row.id)
  for (const row of rows) collection.insert(row)
  auth = await login()
  const response = await fetch(`${BASE}/logs/federation?${new URLSearchParams(scope)}`, { headers: auth })
  expect(response.status).toBe(200)
  viewId = ((await response.json()) as { view: { view_id: string } }).view.view_id
})

describe('日志域桩查询能力', () => {
  it('显式explore执行有界字段发现并确认，按需单字段不改stream标签', async () => {
    const fields = await get('/fields', { explore: 'true' })
    expect(fields.status).toBe(200)
    const body = await fields.json() as { fields: string[]; explore_applied?: boolean }
    expect(body.explore_applied).toBe(true)
    expect(body.fields).toEqual(expect.arrayContaining(['shard', 'duration_ms']))
    expect(body.fields).not.toContain('_time')
    expect(body.fields).not.toContain('_msg')
    const facets = await get('/facets', { explore: 'true', dimensions: 'shard', dimensionLimit: '1' })
    expect(await facets.json()).toMatchObject({ explore_applied: true, truncated: true, coverage: { complete: false }, dimensions: [{ dimension: 'shard', truncated: true }] })
    expect(await (await get('/stream-field-names')).json()).toMatchObject({ fields: ['log_source_id', 'stream'] })
    expect((await get('/fields', { explore: 'true', viewId: '', from: '', to: '' })).status).toBe(400)
    for (const dimensions of ['shard,stream', '_msg', 'event_id', 'bad|stats']) {
      expect((await get('/facets', { explore: 'true', dimensions, dimensionLimit: '20' })).status).toBe(400)
    }
    expect((await get('/facets', { explore: 'true', dimensions: 'not_present', dimensionLimit: '20' })).status).toBe(501)
    expect((await get('/facets', { explore: 'true', dimensions: 'shard', dimensionLimit: '21' })).status).toBe(400)
    const legacy = await (await get('/fields')).json() as { explore_applied?: boolean }
    expect(legacy.explore_applied).toBeUndefined()
  })

  it('点和连字符字段保持发现、分面与数值分组一致，未实现QL明确拒绝', async () => {
    db<(typeof rows)[number]>('logs').insert({ ...rows[0]!, id: 99, message: 'punctuated-marker', fields: { ...rows[0]!.fields, 'http.status_code': 200, 'trace-id': 'edge-a' } } as (typeof rows)[number])
    const query = { explore: 'true', viewId: '', keyword: 'punctuated-marker' }
    const fields = await (await get('/fields', query)).json() as { explore_applied: boolean; fields: string[]; view: { view_id: string } }
    expect(fields.explore_applied).toBe(true)
    expect(fields.fields).toEqual(expect.arrayContaining(['http.status_code', 'trace-id']))
    query.viewId = fields.view.view_id
    const facets = await get('/facets', { ...query, dimensions: 'trace-id', dimensionLimit: '20' })
    expect(await facets.json()).toMatchObject({ explore_applied: true, dimensions: [{ dimension: 'trace-id', values: [{ value: 'edge-a', count: 1 }] }] })
    const stats = await get('/stats', { ...query, metricField: 'http.status_code', groupBy: 'trace-id' })
    expect(await stats.json()).toMatchObject({ points: [{ dimensions: { 'trace-id': 'edge-a' }, agg: { count: 1, sum: 200, min: 200, max: 200, avg: 200 } }] })
    expect((await get('', { viewId: '', keyword: '("http.status_code":="200")' })).status).toBe(501)
  })

  it('字段从固定view的真实行投影，不杜撰schema或把后加行混进来', async () => {
    db<(typeof rows)[number]>('logs').insert({ ...rows[0]!, id: 99, fields: { shard: 'later', duration_ms: 99, later_only: true } } as (typeof rows)[number])
    const response = await get('/fields')
    expect(response.status).toBe(200)
    const body = await response.json() as { fields: string[] }
    expect(body.fields).toEqual(expect.arrayContaining(['_time', '_msg', 'level', 'stream', 'log_source_id', 'shard', 'duration_ms']))
    expect(body.fields).not.toContain('later_only')
    expect(body.fields).not.toContain('parser_version')
  })

  it('streams与stream字段值按实际源和流聚合', async () => {
    const streams = await get('/streams')
    expect(streams.status).toBe(200)
    expect(await streams.json()).toMatchObject({ complete: true, streams: [
      { log_source_id: 'inst:1/stdout', stream: 'stdout', hits: 2 },
      { log_source_id: 'inst:1/stderr', stream: 'stderr', hits: 1 },
      { log_source_id: 'inst:2/stdout', stream: 'stdout', hits: 1 },
    ] })
    expect(await (await get('/stream-field-names')).json()).toMatchObject({ fields: ['log_source_id', 'stream'] })
    expect(await (await get('/stream-field-values', { field: 'stream' })).json()).toMatchObject({ field: 'stream', values: [{ value: 'stdout', count: 3 }, { value: 'stderr', count: 1 }] })
    expect((await get('/stream-field-values', { field: 'duration_ms' })).status).toBe(501)
  })

  it('Facets支持指定文档字段及多个维度；未知字段明确拒绝', async () => {
    const response = await get('/facets', { dimensions: 'shard,stream', dimensionLimit: '20' })
    expect(response.status).toBe(200)
    expect(await response.json()).toMatchObject({ dimensions: [
      { dimension: 'shard', values: [{ value: 'blue', count: 2 }, { value: 'red', count: 2 }] },
      { dimension: 'stream', values: [{ value: 'stdout', count: 3 }, { value: 'stderr', count: 1 }] },
    ] })
    expect((await get('/facets', { dimensions: 'not_in_seed' })).status).toBe(501)
    expect((await get('/facets', { dimensions: 'shard OR *' })).status).toBe(400)
    const truncated = await get('/facets', { dimensions: 'shard', dimensionLimit: '1' })
    expect(await truncated.json()).toMatchObject({ truncated: true, coverage: { complete: false }, dimensions: [{ truncated: true }] })
  })

  it('Stats按seed真实时间与分组生成时间桶，数值字段给出真实聚合', async () => {
    const response = await get('/stats', { timeBucket: '1m', groupBy: 'shard', metricField: 'duration_ms' })
    expect(response.status).toBe(200)
    const body = await response.json() as { points: unknown[] }
    expect(body.points).toEqual([
      { time_bucket_utc: '2026-11-10T00:00:00.000Z', dimensions: { shard: 'blue' }, agg: { count: 2, sum: 10, min: 2, max: 8, avg: 5, has_min_max: true } },
      { time_bucket_utc: '2026-11-10T00:01:00.000Z', dimensions: { shard: 'red' }, agg: { count: 2, sum: 30, min: 10, max: 20, avg: 15, has_min_max: true } },
    ])
    expect((await get('/stats', { metricField: '_msg' })).status).toBe(422)
    expect((await get('/stats', { metricField: 'missing_number' })).status).toBe(501)
    expect((await get('/stats', { timeBucket: 'bad' })).status).toBe(400)
  })

  it('时间桶数量有界，超出上限诚实截断而非伪造精确完整', async () => {
    const collection = db<(typeof rows)[number]>('logs')
    for (const row of collection.list()) collection.remove(row.id)
    for (let index = 0; index < 300; index++) collection.insert({ ...rows[0]!, id: index + 1, time: new Date(Date.parse(scope.from) + index * 1000).toISOString() })
    const response = await get('/stats', { viewId: '', timeBucket: '1s', to: '2026-11-10T00:05:00.000Z' })
    expect(response.status).toBe(200)
    const body = await response.json() as { points: { time_bucket_utc: string; agg: { count: number } }[]; coverage: { complete: boolean }; truncated: boolean }
    expect(body.points).toHaveLength(256)
    expect(body.coverage.complete).toBe(false)
    expect(body.truncated).toBe(true)
    expect(body.points.every((point) => Number.isFinite(Date.parse(point.time_bucket_utc)) && point.agg.count === 1)).toBe(true)
  })

  it('新查询域全部鉴权且验证同scope/view，不允许其他主体复用', async () => {
    const operator = await login('operator', 'op123456')
    for (const path of ['/fields', '/streams', '/stream-field-names', '/stream-field-values', '/stats', '/facets']) {
      const extra = path === '/stream-field-values' ? { field: 'stream' } : {}
      expect((await get(path, extra, {})).status).toBe(401)
      expect((await get(path, { ...extra, source: 'worker' })).status).toBe(409)
      expect((await get(path, extra, operator)).status).toBe(403)
    }
  })

  it('native live和高级查询显式501，不能返回伪造流或query_meta成功', async () => {
    for (const path of ['', '/tail', '/fields', '/stats', '/facets', '/streams', '/stream-field-names', '/stream-field-values', '/live']) {
      const response = await get(path, { advanced: 'true' })
      expect(response.status).toBe(501)
      expect(await response.json()).toMatchObject({ error: 'UNSUPPORTED' })
    }
    const live = await get('/live')
    expect(live.status).toBe(501)
    expect(live.headers.get('content-type')).toContain('application/json')
    expect((await live.json() as Record<string, unknown>).query_meta).toBeUndefined()
  })
})
