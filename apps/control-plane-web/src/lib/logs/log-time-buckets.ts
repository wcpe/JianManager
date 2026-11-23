import type { FederationStatsPoint } from '@/api/logFederation'

/** Keep rendering bounded without dropping the end of a long query window. */
export const MAX_LOG_TIME_BUCKETS = 256

export interface LogTimeBucket {
  label: string
  count: number
  /** The count is only the observed subtotal; missing intervals remain unknown. */
  missing?: boolean
}

export interface LogTimeBucketOptions {
  from?: string
  to?: string
  timeBucket: string
  exact: boolean
}

/** Older coordinators may carry the timestamp outside dimensions. */
export type LogTimeStatsPoint = FederationStatsPoint & { time_bucket_utc?: string }

const DURATION_UNITS: Record<string, number> = {
  ms: 1,
  s: 1_000,
  m: 60_000,
  h: 3_600_000,
  d: 86_400_000,
  w: 604_800_000,
}

function durationMilliseconds(value: string): number | null {
  const match = /^(\d+(?:\.\d+)?)(ms|s|m|h|d|w)$/.exec(value)
  if (!match) return null
  const unit = DURATION_UNITS[match[2] ?? '']
  if (!unit) return null
  const milliseconds = Number(match[1]) * unit
  return Number.isSafeInteger(milliseconds) && milliseconds > 0 ? milliseconds : null
}

/** Reject relative/locale dates and Date.parse's normalization of invalid days. */
function timestampMilliseconds(value: unknown): number | null {
  if (typeof value !== 'string') return null
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(Z|[+-]\d{2}:\d{2})$/i.exec(value)
  if (!match) return null
  const [, year, month, day, hour, minute, second] = match
  const leapYear = Number(year) % 4 === 0 && (Number(year) % 100 !== 0 || Number(year) % 400 === 0)
  const daysInMonth = [31, leapYear ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][Number(month) - 1]
  if (!daysInMonth || Number(day) < 1 || Number(day) > daysInMonth
    || Number(hour) > 23 || Number(minute) > 59 || Number(second) > 59) return null
  const milliseconds = Date.parse(value)
  return Number.isFinite(milliseconds) ? milliseconds : null
}

function pointTimestamp(point: LogTimeStatsPoint): unknown {
  return point.dimensions?._time ?? point.dimensions?.time
    ?? point.dimensions?.time_bucket_utc ?? point.dimensions?.time_bucket ?? point.time_bucket_utc
}

/**
 * Build the requested [from, to) time axis, or the returned extent for open ends.
 * Only an exact aggregate can turn omitted buckets into zero. A coarse bucket
 * stays unknown if any constituent interval was omitted by an inexact response.
 * Invalid input fails the whole chart instead of silently discarding bad points.
 */
export function buildLogTimeBuckets(
  points: readonly LogTimeStatsPoint[],
  { from, to, timeBucket, exact }: LogTimeBucketOptions,
): { buckets: LogTimeBucket[]; invalid: boolean } {
  const invalid = { buckets: [], invalid: true }
  const step = durationMilliseconds(timeBucket)
  const fromTime = from === undefined ? null : timestampMilliseconds(from)
  const toTime = to === undefined ? null : timestampMilliseconds(to)
  if (step === null || (from !== undefined && fromTime === null) || (to !== undefined && toTime === null)
    || (fromTime !== null && toTime !== null && fromTime >= toTime)) return invalid

  const counts = new Map<number, number>()
  let firstReturned = Infinity
  let lastReturned = -Infinity
  for (const point of points) {
    if (!point) return invalid
    const time = timestampMilliseconds(pointTimestamp(point))
    const count = point.agg?.count ?? point.count
    if (time === null || typeof count !== 'number' || !Number.isFinite(count) || count < 0
      || (toTime !== null && time >= toTime)) return invalid
    const index = Math.floor(time / step)
    // Stats timestamps identify bucket starts; the first bucket may straddle from.
    if (fromTime !== null && index < Math.floor(fromTime / step)) return invalid
    const sum = (counts.get(index) ?? 0) + count
    if (!Number.isFinite(sum)) return invalid
    counts.set(index, sum)
    firstReturned = Math.min(firstReturned, index)
    lastReturned = Math.max(lastReturned, index)
  }

  const first = fromTime === null ? firstReturned : Math.floor(fromTime / step)
  const last = toTime === null ? lastReturned : Math.ceil(toTime / step) - 1
  if (!Number.isFinite(first) || !Number.isFinite(last)) return { buckets: [], invalid: false }
  const bucketCount = last - first + 1
  if (!Number.isSafeInteger(bucketCount) || bucketCount <= 0) return invalid
  const multiple = Math.ceil(bucketCount / MAX_LOG_TIME_BUCKETS)
  const observed = new Map<number, { count: number; intervals: number }>()
  for (const [index, count] of counts) {
    const group = Math.floor((index - first) / multiple)
    const previous = observed.get(group)
    const sum = (previous?.count ?? 0) + count
    if (!Number.isFinite(sum)) return invalid
    observed.set(group, { count: sum, intervals: (previous?.intervals ?? 0) + 1 })
  }

  const buckets: LogTimeBucket[] = []
  for (let group = 0; group < Math.ceil(bucketCount / multiple); group += 1) {
    const start = (first + group * multiple) * step
    const date = new Date(start)
    if (!Number.isFinite(date.getTime())) return invalid
    const aggregate = observed.get(group)
    const intervals = Math.min(multiple, bucketCount - group * multiple)
    buckets.push({
      label: date.toISOString().replace('.000Z', 'Z'),
      count: aggregate?.count ?? 0,
      ...(exact !== true && (aggregate?.intervals ?? 0) < intervals ? { missing: true } : {}),
    })
  }
  return { buckets, invalid: false }
}
