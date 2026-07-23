import type { BotLoadLatencySummary, BotLoadMetricPoint } from './types'

/** 最近窗口最大点数（SSE 内存环）。 */
export const LIVE_METRIC_MAX_POINTS = 300

/** 追加 metric 点并裁剪窗口。 */
export function appendMetricPoints(
  current: BotLoadMetricPoint[],
  next: BotLoadMetricPoint | BotLoadMetricPoint[],
  max = LIVE_METRIC_MAX_POINTS,
): BotLoadMetricPoint[] {
  const list = Array.isArray(next) ? next : [next]
  const merged = [...current, ...list]
  if (merged.length <= max) return merged
  return merged.slice(merged.length - max)
}

/** 图表序列点数上限（验收 ≤1200）。 */
export const CHART_MAX_POINTS = 1200

export function clampChartPoints<T>(items: T[], max = CHART_MAX_POINTS): T[] {
  if (items.length <= max) return items
  // 均匀抽样保留首尾。
  const step = (items.length - 1) / (max - 1)
  const out: T[] = []
  for (let i = 0; i < max; i++) {
    out.push(items[Math.round(i * step)]!)
  }
  return out
}

export function formatLatencyMs(v: number | null | undefined): string {
  if (v == null || Number.isNaN(v)) return '—'
  if (v < 1000) return `${Math.round(v)} ms`
  return `${(v / 1000).toFixed(2)} s`
}

export function formatRatio(v: number | null | undefined): string {
  if (v == null || Number.isNaN(v)) return '—'
  return `${(v * 100).toFixed(1)}%`
}

export function formatBytes(v: number | null | undefined): string {
  if (v == null || Number.isNaN(v)) return '—'
  if (v < 1024) return `${v} B`
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(1)} KB`
  if (v < 1024 * 1024 * 1024) return `${(v / (1024 * 1024)).toFixed(1)} MB`
  return `${(v / (1024 * 1024 * 1024)).toFixed(2)} GB`
}

export function pickLatency(
  latency: BotLoadLatencySummary | undefined,
  key: keyof BotLoadLatencySummary,
): number | null {
  if (!latency) return null
  const v = latency[key]
  return typeof v === 'number' ? v : null
}

/** null 不画 0：过滤无效点。 */
export function seriesWithNulls(
  points: BotLoadMetricPoint[],
  pick: (p: BotLoadMetricPoint) => number | null | undefined,
): Array<{ t: string; v: number | null }> {
  return points.map((p) => {
    const v = pick(p)
    return { t: p.timestamp, v: v == null || Number.isNaN(v) ? null : v }
  })
}
