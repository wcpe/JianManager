/** Field identifiers accepted by the current Worker stats/facets adapter. */
export function isQueryableLogField(field: string): boolean {
  return /^[A-Za-z_][A-Za-z0-9_]*$/.test(field)
}

export const LOG_FIELD_TIME_BUCKETS = ['', '1m', '5m', '15m', '1h', '6h', '1d'] as const

export function fieldValueExpression(field: string, value: string, exclude = false): string {
  if (!isQueryableLogField(field)) throw new Error('Invalid log field')
  // JSON quotes also escape backslashes, newlines and control characters. OR stays inside the literal.
  const exact = `(${field}:=${JSON.stringify(value)})`
  return exclude ? `NOT ${exact}` : exact
}

export function buildFieldFacetParams(params: Record<string, unknown>, field: string) {
  if (!isQueryableLogField(field)) throw new Error('Invalid log field')
  return { ...params, dimensions: field, dimensionLimit: 20 }
}

export function buildFieldStatsParams(
  params: Record<string, unknown>,
  metricField: string,
  groupBy: string,
  timeBucket: string,
) {
  if (!isQueryableLogField(metricField) || (groupBy && !isQueryableLogField(groupBy))) throw new Error('Invalid log field')
  if (!(LOG_FIELD_TIME_BUCKETS as readonly string[]).includes(timeBucket)) throw new Error('Invalid time bucket')
  return { ...params, metricField, groupBy, timeBucket }
}

function record(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === 'object' ? value as Record<string, unknown> : {}
}

function finite(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

export type FieldExplorerIssue = 'failed' | 'unsupported' | 'partial' | 'truncated' | 'pending' | 'approximate' | 'unverified'

/** Do not let a failed/unverified empty response become “no values” success. */
export function fieldExplorerStatus(result: unknown, error?: unknown): { issues: FieldExplorerIssue[]; reasons: string[]; detail: string } {
  const body = record(result)
  const coverage = record(body.coverage)
  const errorResponse = record(record(error).response)
  const failure = record(errorResponse.data)
  const targets = Array.isArray(coverage.targets) ? coverage.targets : []
  const codes = [
    ...(Array.isArray(coverage.partialReasons) ? coverage.partialReasons : []),
    ...(Array.isArray(coverage.partial_reasons) ? coverage.partial_reasons : []),
    ...targets.flatMap((target) => Array.isArray(record(target).reasons) ? record(target).reasons as unknown[] : []),
    ...(Array.isArray(body.notes) ? body.notes : []),
    body.error, body.code, body.error_code, failure.error, failure.code, failure.error_code,
  ]
  const reasons = [...new Set(codes.filter((code): code is string => typeof code === 'string' && code.length > 0))]
  const issues: FieldExplorerIssue[] = []
  const detail = typeof failure.message === 'string' ? failure.message : error instanceof Error ? error.message : ''
  if (error || body.ok === false || (typeof body.error === 'string' && body.error.length > 0)) issues.push('failed')
  if (errorResponse.status === 501 || reasons.some((code) => /unsupported|unimplemented/i.test(code)) || /unsupported|unimplemented/i.test(detail)) issues.push('unsupported')
  if (body.truncated === true || reasons.some((code) => /truncat/i.test(code))) issues.push('truncated')
  if (coverage.pending === true) issues.push('pending')
  if (reasons.some((code) => !/unsupported|unimplemented|truncat/i.test(code)) || (coverage.complete === false && coverage.pending !== true)) issues.push('partial')
  const quality = record(body.quality)
  const duplicate = String(quality.duplicate_quality ?? quality.duplicateQuality ?? '').toUpperCase()
  const stats = String(quality.stats_quality ?? quality.statsQuality ?? '').toUpperCase()
  if ((duplicate && duplicate !== 'EXACT') || (stats && stats !== 'EXACT')) issues.push('approximate')
  if ((!duplicate || !stats || coverage.complete !== true) && !issues.length) issues.push('unverified')
  return { issues: [...new Set(issues)], reasons, detail }
}

/** Current CP aggregate contract defines avg as weighted sum/count, never mean-of-means. */
export function fieldStatsValues(point: unknown) {
  const aggregate = record(record(point).agg)
  const rawCount = finite(aggregate.count ?? record(point).count)
  const count = rawCount !== undefined && rawCount >= 0 ? rawCount : undefined
  const numeric = count !== undefined && count > 0 && aggregate.has_min_max !== false
  const sum = numeric ? finite(aggregate.sum) : undefined
  const min = numeric ? finite(aggregate.min) : undefined
  const max = numeric ? finite(aggregate.max) : undefined
  const avg = numeric ? finite(finite(aggregate.avg) ?? (sum !== undefined ? sum / count : undefined)) : undefined
  return { count, sum, min, max, avg }
}
