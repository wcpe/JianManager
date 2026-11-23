/** Bounded, seed-backed projections for the development-only log query facade. */
export interface MockLogDocument {
  id: number
  source: string
  level: string
  instanceId: number
  nodeId: number
  instanceUuid?: string
  stream?: string
  log_source_id?: string
  fields?: Record<string, unknown>
  message: string
  time: string
}

type Scalar = string | number | boolean
export class MockLogQueryError extends Error {
  constructor(message: string, public status = 501, public code = 'UNSUPPORTED') { super(message) }
}

export function projectMockLog(row: MockLogDocument): Record<string, Scalar> {
  const stream = row.stream || 'file'
  const sourceID = row.log_source_id ?? (row.source === 'instance' ? `inst:${row.instanceId}/${stream}` : row.source === 'worker' ? `node:${row.nodeId}/${stream}` : `control_plane:${row.nodeId}/${stream}`)
  const fields: Record<string, Scalar> = Object.create(null)
  for (const [key, value] of Object.entries(row.fields ?? {})) {
    if (typeof value === 'string' || typeof value === 'boolean' || (typeof value === 'number' && Number.isFinite(value))) fields[key] = value
  }
  // A document payload cannot overwrite the source/time identity used by the fixed view.
  Object.assign(fields, { id: row.id, source: row.source, level: row.level.toUpperCase(), nodeId: row.nodeId, instanceId: row.instanceId, _time: row.time, _msg: row.message, stream, log_source_id: sourceID })
  if (row.instanceUuid) fields.instanceUuid = row.instanceUuid
  return fields
}

const knownFields = new Set(['id', 'source', 'level', 'nodeId', 'instanceId', 'instanceUuid', '_time', '_msg', 'stream', 'log_source_id'])
export function mockLogFields(rows: MockLogDocument[]) {
  return [...new Set(rows.flatMap((row) => Object.keys(projectMockLog(row))))].sort()
}
function checkField(rows: MockLogDocument[], field: string) {
  if (!/^[A-Za-z_][A-Za-z0-9_.-]*$/.test(field)) throw new MockLogQueryError('字段名不符合当前 mock 查询契约', 400, 'INVALID_REQUEST')
  if (!knownFields.has(field) && !rows.some((row) => Object.hasOwn(projectMockLog(row), field))) throw new MockLogQueryError(`种子数据中不存在字段 ${field}`)
}
function boundedLimit(raw: string | null, fallback: number, maximum = 100) {
  const limit = raw === null ? fallback : Number(raw)
  if (!Number.isInteger(limit) || limit < 1 || limit > maximum) throw new MockLogQueryError('查询数量参数无效', 400, 'INVALID_REQUEST')
  return limit
}
function valueCounts(rows: MockLogDocument[], field: string, limit: number) {
  checkField(rows, field)
  const counts = new Map<string, number>()
  for (const row of rows) {
    const value = projectMockLog(row)[field]
    if (value === undefined) continue
    const label = String(value)
    counts.set(label, (counts.get(label) ?? 0) + 1)
  }
  const values = [...counts].sort(([a, ac], [b, bc]) => bc - ac || a.localeCompare(b)).map(([value, count]) => ({ value, count }))
  return { values: values.slice(0, limit), truncated: values.length > limit }
}
export function mockLogFacets(rows: MockLogDocument[], url: URL) {
  if (url.searchParams.get('explore') === 'true') return mockLogExploreFacets(rows, url)
  const names = [...new Set((url.searchParams.get('dimensions') ?? 'level').split(',').map((name) => name.trim()).filter(Boolean))]
  if (names.length === 0 || names.length > 16) throw new MockLogQueryError('维度列表为空或超出 mock 上限', 400, 'INVALID_REQUEST')
  const limit = boundedLimit(url.searchParams.get('dimensionLimit'), 20)
  const dimensions = names.map((dimension) => ({ dimension, ...valueCounts(rows, dimension, limit) }))
  return { dimensions, truncated: dimensions.some((dimension) => dimension.truncated) }
}
export function mockLogStreams(rows: MockLogDocument[], url: URL) {
  const groups = new Map<string, { log_source_id: string; stream: string; hits: number }>()
  for (const row of rows) {
    const fields = projectMockLog(row)
    const log_source_id = String(fields.log_source_id)
    const stream = String(fields.stream)
    const key = JSON.stringify([log_source_id, stream])
    const group = groups.get(key) ?? { log_source_id, stream, hits: 0 }
    group.hits++
    groups.set(key, group)
  }
  const limit = boundedLimit(url.searchParams.get('limit'), 100)
  const values = [...groups.values()].sort((a, b) => b.hits - a.hits || a.log_source_id.localeCompare(b.log_source_id))
  return { streams: values.slice(0, limit), complete: values.length <= limit, truncated: values.length > limit }
}
export function mockLogStreamValues(rows: MockLogDocument[], url: URL) {
  const field = url.searchParams.get('field') ?? ''
  if (field !== 'stream' && field !== 'log_source_id') throw new MockLogQueryError('mock 只支持已投影的 stream/log_source_id 流标签')
  const result = valueCounts(rows, field, boundedLimit(url.searchParams.get('limit'), 100))
  return { field, ...result, complete: !result.truncated }
}

/** At most 256 populated groups/time buckets; omitted groups are reported as truncated, never exact. */
export function mockLogStats(rows: MockLogDocument[], url: URL) {
  const groupBy = (url.searchParams.get('groupBy') ?? '').split(',').map((field) => field.trim()).filter(Boolean)
  if (groupBy.length > 4) throw new MockLogQueryError('mock 数值统计最多支持四个分组字段')
  for (const field of groupBy) checkField(rows, field)
  const metric = url.searchParams.get('metricField') ?? ''
  if (metric) checkField(rows, metric)
  const bucket = url.searchParams.get('timeBucket') ?? ''
  const duration = /^([1-9]\d{0,8})(ms|s|m|h|d)$/.exec(bucket)
  if (bucket && !duration) throw new MockLogQueryError('时间桶必须是正时长', 400, 'INVALID_REQUEST')
  const unit = { ms: 1, s: 1000, m: 60000, h: 3600000, d: 86400000 }
  const step = duration ? Number(duration[1]) * unit[duration[2] as keyof typeof unit] : 0
  type Point = { dimensions?: Record<string, string>; time_bucket_utc?: string; agg: { count: number; sum?: number; min?: number; max?: number; avg?: number; has_min_max?: boolean } }
  const points = new Map<string, Point>()
  let truncated = false
  for (const row of rows) {
    const projection = projectMockLog(row)
    const numeric = metric ? projection[metric] : undefined
    if (metric && (typeof numeric !== 'number' || !Number.isFinite(numeric))) throw new MockLogQueryError(`字段 ${metric} 不是所有匹配行中均存在的数值字段`, 422, 'INVALID_METRIC_FIELD')
    const dimensions: Record<string, string> = {}
    for (const field of groupBy) if (projection[field] !== undefined) dimensions[field] = String(projection[field])
    const timestamp = Date.parse(row.time)
    if (step && !Number.isFinite(timestamp)) throw new MockLogQueryError('种子行缺少合法时间，不能伪造时间桶')
    const time = step ? new Date(Math.floor(timestamp / step) * step).toISOString() : undefined
    const key = JSON.stringify([time, groupBy.map((field) => dimensions[field] ?? null)])
    if (!points.has(key) && points.size >= 256) { truncated = true; continue }
    const point = points.get(key) ?? { ...(groupBy.length ? { dimensions } : {}), ...(time ? { time_bucket_utc: time } : {}), agg: { count: 0 } }
    point.agg.count++
    if (metric) {
      const value = numeric as number
      point.agg.sum = (point.agg.sum ?? 0) + value
      if (!Number.isFinite(point.agg.sum)) throw new MockLogQueryError('数值聚合超出可表示范围', 422, 'INVALID_METRIC_FIELD')
      point.agg.min = point.agg.min === undefined ? value : Math.min(point.agg.min, value)
      point.agg.max = point.agg.max === undefined ? value : Math.max(point.agg.max, value)
      point.agg.avg = point.agg.sum / point.agg.count
      point.agg.has_min_max = true
    }
    points.set(key, point)
  }
  if (points.size === 0 && !bucket && groupBy.length === 0) points.set('empty', { agg: { count: 0 } })
  return { points: [...points.values()].sort((a, b) => (a.time_bucket_utc ?? '').localeCompare(b.time_bucket_utc ?? '') || JSON.stringify(a.dimensions ?? {}).localeCompare(JSON.stringify(b.dimensions ?? {}))), truncated }
}

const reservedExplorationFields = new Set([
  '_time', '_msg', '_stream', '_stream_id', 'message', 'event_id', 'canonical_content_hash',
  'source_generation', 'record_start', 'record_end', 'event_time', 'event_time_utc',
  'ingest_time', 'ingest_time_utc', 'projection_generation', 'storage_namespace',
  'closed_visible_seq', 'catalog_generation', 'id', 'source', 'nodeId', 'instanceId', 'instanceUuid',
])
function explorationFieldAllowed(field: string) {
  return field.length <= 128 && /^[A-Za-z_][A-Za-z0-9_.-]*$/.test(field) && !reservedExplorationFields.has(field)
}
function boundedExplorationRows(rows: MockLogDocument[], url: URL) {
  const from = Date.parse(url.searchParams.get('from') ?? '')
  const to = Date.parse(url.searchParams.get('to') ?? '')
  if (!Number.isFinite(from) || !Number.isFinite(to) || from >= to) throw new MockLogQueryError('字段探索需要有限绝对时间范围', 400, 'INVALID_REQUEST')
  const selected: MockLogDocument[] = []
  const encoder = new TextEncoder()
  let bytes = 0
  const started = Date.now()
  for (const row of rows) {
    const size = encoder.encode(JSON.stringify(projectMockLog(row))).length
    if (bytes + size > 1 << 20 || selected.length >= 10000 || Date.now() - started >= 5000) break
    bytes += size
    selected.push(row)
  }
  return { rows: selected, truncated: selected.length < rows.length }
}

/** Only acknowledge exploration after applying its bounded row/field policy. */
export function mockLogExploreFields(rows: MockLogDocument[], url: URL) {
  const selected = boundedExplorationRows(rows, url)
  const fields = mockLogFields(selected.rows).filter(explorationFieldAllowed)
  return { fields: fields.slice(0, 256), truncated: selected.truncated || fields.length > 256, explore_applied: true }
}
function mockLogExploreFacets(rows: MockLogDocument[], url: URL) {
  const names = (url.searchParams.get('dimensions') ?? '').split(',')
  if (names.length !== 1 || !explorationFieldAllowed(names[0]!)) throw new MockLogQueryError('字段探索仅支持单个合法非保留字段', 400, 'INVALID_REQUEST')
  if (!url.searchParams.has('dimensionLimit')) throw new MockLogQueryError('字段探索必须指定 dimensionLimit', 400, 'INVALID_REQUEST')
  const limit = boundedLimit(url.searchParams.get('dimensionLimit'), 20, 20)
  const selected = boundedExplorationRows(rows, url)
  const field = names[0]!
  if (!mockLogFields(selected.rows).includes(field)) {
    if (selected.truncated) throw new MockLogQueryError('预算内未能证实字段存在', 422, 'LOG_BUDGET_EXCEEDED')
    throw new MockLogQueryError(`固定视图内不存在字段 ${field}`)
  }
  const dimension = { dimension: field, ...valueCounts(selected.rows, field, limit) }
  return { dimensions: [dimension], truncated: selected.truncated || dimension.truncated, explore_applied: true }
}
