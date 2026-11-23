import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { describe, expect, it } from 'vitest'
import { TimeDistributionCard } from '@/components/logs/LogsInsightCards'
import en from '@/i18n/en.json'
import zh from '@/i18n/zh.json'
import {
  buildLogTimeBuckets,
  MAX_LOG_TIME_BUCKETS,
  type LogTimeStatsPoint,
} from './log-time-buckets'

const from = '2026-11-11T12:00:00Z'
const to = '2026-11-11T12:05:00Z'
const options = { from, to, timeBucket: '1m', exact: true }
const point = (time: string, count: number): LogTimeStatsPoint => ({ dimensions: { _time: time }, agg: { count } })
const labels = ['00', '01', '02', '03', '04'].map((minute) => `2026-11-11T12:${minute}:00Z`)

describe('buildLogTimeBuckets', () => {
  it('sorts sparse responses, merges points in the same interval, and covers both window edges', () => {
    const result = buildLogTimeBuckets([
      point('2026-11-11T12:04:00Z', 7),
      point('2026-11-11T12:01:45Z', 2),
      { dimensions: { _time: '2026-11-11T12:01:00Z', level: 'warn' }, agg: { count: 3 } },
    ], options)

    expect(result).toEqual({
      buckets: labels.map((label, index) => ({ label, count: [0, 5, 0, 0, 7][index] })),
      invalid: false,
    })
  })

  it('marks only omitted intervals as unknown when completeness is not exact, including leading and trailing gaps', () => {
    const result = buildLogTimeBuckets([
      point('2026-11-11T12:01:00Z', 0),
      point('2026-11-11T12:03:00Z', 7),
    ], { ...options, exact: false })

    expect(result).toEqual({
      buckets: [
        { label: labels[0], count: 0, missing: true },
        { label: labels[1], count: 0 },
        { label: labels[2], count: 0, missing: true },
        { label: labels[3], count: 7 },
        { label: labels[4], count: 0, missing: true },
      ],
      invalid: false,
    })
  })

  it('uses the returned extent when request bounds are absent', () => {
    const result = buildLogTimeBuckets([
      point('2026-11-11T12:04:00Z', 7),
      point('2026-11-11T12:01:00Z', 3),
    ], { timeBucket: '1m', exact: true })

    expect(result.invalid).toBe(false)
    expect(result.buckets).toEqual(labels.slice(1).map((label, index) => ({ label, count: [3, 0, 0, 7][index] })))
  })

  it('uses the actual extent only for the open end of a one-sided request', () => {
    const points = [point('2026-11-11T12:02:00Z', 3)]
    expect(buildLogTimeBuckets(points, { from, timeBucket: '1m', exact: true }).buckets.map((bucket) => bucket.label))
      .toEqual(labels.slice(0, 3))
    expect(buildLogTimeBuckets(points, { to, timeBucket: '1m', exact: true }).buckets.map((bucket) => bucket.label))
      .toEqual(labels.slice(2))
  })

  it('prefers _time then time over the compatibility timestamp and never reads arbitrary groups', () => {
    const result = buildLogTimeBuckets([
      { dimensions: { source: 'inst:1', _time: labels[1], time: labels[4] }, time_bucket_utc: 'bad', agg: { count: 2 }, count: 99 },
      { dimensions: { source: 'inst:2', time: labels[2] }, time_bucket_utc: labels[4], count: 3 },
      { dimensions: { source: 'inst:3' }, time_bucket_utc: labels[3], count: 4 },
      { dimensions: { time_bucket_utc: labels[4] }, count: 5 },
    ], options)

    expect(result.invalid).toBe(false)
    expect(result.buckets.map((bucket) => bucket.count)).toEqual([0, 2, 3, 4, 5])
  })

  it('normalizes absolute timestamps with an offset onto the same UTC axis', () => {
    const result = buildLogTimeBuckets([point('2026-11-11T20:02:00+08:00', 3)], options)
    expect(result.invalid).toBe(false)
    expect(result.buckets[2]).toEqual({ label: labels[2], count: 3 })
  })

  it.each([
    {},
    { dimensions: { source: from }, count: 2 },
    point('', 2),
    point('not-a-time', 2),
    point('1762862400000', 2),
    point('2026-11-11T12:02:00', 2),
    point('2026-02-30T12:00:00Z', 2),
    point('2026-11-11T24:00:00Z', 2),
    { dimensions: { _time: 'bad', time: labels[2] }, time_bucket_utc: labels[2], count: 2 },
  ])('rejects missing, ambiguous, or invalid time without drawing a successful subset: %j', (invalidPoint) => {
    expect(buildLogTimeBuckets([point(labels[2], 1), invalidPoint], options)).toEqual({ buckets: [], invalid: true })
  })

  it.each([
    '2026-11-11T11:59:59Z',
    to,
    '2026-11-11T12:05:01Z',
  ])('rejects a point outside the HTTP [from, to) window: %s', (time) => {
    expect(buildLogTimeBuckets([point(labels[2], 1), point(time, 2)], options)).toEqual({ buckets: [], invalid: true })
  })

  it('keeps the first intersecting Stats bucket when from is not aligned to the bucket width', () => {
    const result = buildLogTimeBuckets([
      point('2026-11-11T08:00:00Z', 3),
      point('2026-11-11T08:02:00Z', 7),
    ], { ...options, from: '2026-11-11T08:00:30Z', to: '2026-11-11T08:02:30Z' })
    expect(result).toEqual({
      buckets: [
        { label: '2026-11-11T08:00:00Z', count: 3 },
        { label: '2026-11-11T08:01:00Z', count: 0 },
        { label: '2026-11-11T08:02:00Z', count: 7 },
      ],
      invalid: false,
    })
  })

  it('rejects a bucket strictly before the first intersecting bucket for an unaligned from', () => {
    expect(buildLogTimeBuckets([
      point('2026-11-11T08:00:00Z', 3),
      point('2026-11-11T07:59:00Z', 7),
    ], { ...options, from: '2026-11-11T08:00:30Z', to: '2026-11-11T08:02:30Z' }))
      .toEqual({ buckets: [], invalid: true })
  })

  it.each([undefined, Number.NaN, Infinity, -1, '7', null])('rejects a non-finite or non-numeric count: %s', (count) => {
    expect(buildLogTimeBuckets([{ dimensions: { _time: labels[2] }, count } as LogTimeStatsPoint], options))
      .toEqual({ buckets: [], invalid: true })
  })

  it('rejects count overflow while merging points', () => {
    expect(buildLogTimeBuckets([point(labels[2], Number.MAX_VALUE), point(labels[2], Number.MAX_VALUE)], options))
      .toEqual({ buckets: [], invalid: true })
  })

  it.each([
    { from: 'bad' },
    { to: '' },
    { from: to, to: from },
    { from, to: from },
    { timeBucket: '' },
    { timeBucket: '0m' },
    { timeBucket: 'month' },
    { timeBucket: '999999999999999999999d' },
  ])('rejects invalid request bounds or bucket widths: %j', (override) => {
    expect(buildLogTimeBuckets([point(labels[2], 1)], { ...options, ...override })).toEqual({ buckets: [], invalid: true })
  })

  it('keeps empty results explicit, filling bounded exact windows with zero and inexact windows with unknowns', () => {
    expect(buildLogTimeBuckets([], { timeBucket: '1m', exact: true })).toEqual({ buckets: [], invalid: false })
    expect(buildLogTimeBuckets([], options)).toEqual({ buckets: labels.map((label) => ({ label, count: 0 })), invalid: false })
    expect(buildLogTimeBuckets([], { ...options, exact: false })).toEqual({
      buckets: labels.map((label) => ({ label, count: 0, missing: true })),
      invalid: false,
    })
  })

  it('bounds large windows by merging integer multiples of buckets, preserving counts at the tail', () => {
    const start = Date.parse(from)
    const atMinute = (minute: number) => new Date(start + minute * 60_000).toISOString()
    const result = buildLogTimeBuckets([point(atMinute(0), 2), point(atMinute(9_999), 7)], {
      ...options,
      to: atMinute(10_000),
    })
    const multiple = Math.ceil(10_000 / MAX_LOG_TIME_BUCKETS)
    expect(result.invalid).toBe(false)
    expect(result.buckets).toHaveLength(Math.ceil(10_000 / multiple))
    expect(result.buckets.length).toBeLessThanOrEqual(MAX_LOG_TIME_BUCKETS)
    expect(result.buckets.reduce((sum, bucket) => sum + bucket.count, 0)).toBe(9)
    expect(result.buckets.at(-1)?.count).toBe(7)
    expect(Date.parse(result.buckets[1].label) - Date.parse(result.buckets[0].label)).toBe(multiple * 60_000)
    expect(result.buckets.every((bucket) => !bucket.missing)).toBe(true)
  })

  it('keeps a merged inexact bucket unknown if any of its constituent intervals is missing', () => {
    const start = Date.parse(from)
    const atMinute = (minute: number) => new Date(start + minute * 60_000).toISOString()
    const result = buildLogTimeBuckets([
      ...Array.from({ length: 6 }, (_, index) => point(atMinute(index), 1)),
      point(atMinute(1_439), 7),
    ], { ...options, exact: false, to: atMinute(1_440) })

    expect(result.invalid).toBe(false)
    expect(result.buckets).toHaveLength(240)
    expect(result.buckets[0]).toEqual({ label: from, count: 6 })
    expect(result.buckets[1]).toEqual({ label: '2026-11-11T12:06:00Z', count: 0, missing: true })
    expect(result.buckets.at(-1)).toEqual({ label: '2026-11-12T11:54:00Z', count: 7, missing: true })
    expect(result.buckets.reduce((sum, bucket) => sum + bucket.count, 0)).toBe(13)
  })

  it('handles a century-spanning window without allocating every source bucket or losing the last point', () => {
    const result = buildLogTimeBuckets([
      point('1900-01-01T00:00:00Z', 2),
      point('2099-12-31T23:59:59Z', 7),
    ], { from: '1900-01-01T00:00:00Z', to: '2100-01-01T00:00:00Z', timeBucket: '1s', exact: true })

    expect(result.invalid).toBe(false)
    expect(result.buckets.length).toBeLessThanOrEqual(MAX_LOG_TIME_BUCKETS)
    expect(result.buckets.reduce((sum, bucket) => sum + bucket.count, 0)).toBe(9)
    expect(result.buckets.at(-1)?.count).toBe(7)
  })
})

const translator = createInstance()
await translator.init({
  lng: 'en',
  fallbackLng: false,
  resources: { en: { translation: en }, zh: { translation: zh } },
})

function renderTimeCard(props: Parameters<typeof TimeDistributionCard>[0]) {
  return renderToStaticMarkup(createElement(I18nextProvider, { i18n: translator }, createElement(TimeDistributionCard, props)))
}

describe('TimeDistributionCard', () => {
  it.each([
    { buckets: [], state: 'empty', message: 'No logs' },
    { buckets: [{ label: from, count: 0 }], state: 'empty', message: 'No logs' },
    { buckets: [{ label: from, count: 7 }], failed: true, state: 'failed', message: 'Time distribution failed; count unknown' },
    { buckets: [{ label: from, count: 7 }], invalid: true, state: 'unsupported', message: 'Time distribution unavailable' },
    { buckets: [{ label: from, count: Number.NaN }], state: 'unsupported', message: 'Time distribution unavailable' },
    { buckets: Array.from({ length: MAX_LOG_TIME_BUCKETS + 1 }, (_, index) => ({ label: String(index), count: 1 })), state: 'unsupported', message: 'Time distribution unavailable' },
  ])('keeps a visible, fixed-height slot for $state', ({ state, message, ...props }) => {
    const markup = renderTimeCard(props)
    const slot = markup.match(/<div[^>]*data-testid="logs-time-distribution"[^>]*>/)?.[0]
    expect(markup).toContain('data-testid="logs-time-card"')
    expect(slot).toContain(`data-state="${state}"`)
    expect(slot).toContain('h-8')
    expect(markup).toContain(message)
    expect(markup).not.toContain('role="img"')
    if (state !== 'empty') expect(slot).toContain('data-time-detail=""')
  })

  it('renders unknown gaps differently from zero, including the detail attribute and hover title', () => {
    const markup = renderTimeCard({
      buckets: [{ label: from, count: 7 }, { label: labels[1], count: 0, missing: true }, { label: labels[2], count: 0 }],
      uncertain: true,
    })
    const gap = markup.match(/<div[^>]*data-missing="true"[^>]*>/)?.[0]
    expect(markup).toContain('data-state="unknown"')
    expect(markup).toContain('h-8')
    expect(markup).toContain(`data-time-detail="${from}:7,${labels[1]}:?,${labels[2]}:0"`)
    expect(gap).toContain('border-dashed')
    expect(gap).toContain(`title="${labels[1]}: ? · Completeness unknown"`)
    expect(gap).not.toContain(`${labels[1]}: 0`)
    expect(markup).toContain(`title="${labels[2]}: ≈0"`)
  })

  it('keeps a known nonzero time distribution in the same slot', () => {
    const markup = renderTimeCard({ buckets: [{ label: from, count: 7 }] })
    expect(markup).toContain('data-state="ready"')
    expect(markup).toContain('h-8')
    expect(markup).toContain(`data-time-detail="${from}:7"`)
    expect(markup).toContain('role="img"')
  })

  it('provides a visible Chinese fallback for unsupported time buckets without a new translation resource', async () => {
    await translator.changeLanguage('zh')
    try {
      expect(renderTimeCard({ buckets: [], invalid: true })).toContain('时间分布不可用：时间桶无效或不受支持')
    } finally {
      await translator.changeLanguage('en')
    }
  })
})
