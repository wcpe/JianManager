/* eslint-disable react-refresh/only-export-components -- 共享展示件与格式化/常量同文件导出（仅影响 Fast Refresh） */
import { useTranslation } from 'react-i18next'
import { Badge } from '@jianmanager/ui/components/badge'
import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import { Panel } from '@jianmanager/ui/components/panel'
import type { MetricRange } from '@jianmanager/ui'
import { TimeSeriesChart, type ChartSeries } from '@jianmanager/ui'
import type { ClientDistStats } from '@/api/clientStats'
import type { ClientDistEvent, ClientDistRealtime } from '@/api/clientDistEvents'
import type { ClientRuntimeOverview, RuntimeUpdateSeriesPoint } from '@/api/clientRuntimeStates'
import type { DistBucket } from '@/lib/platform-stats'

/**
 * 页面 B「客户端分发运维」共享展示件与格式化（FR-430 / ADR-088）。
 * 由旧监控页（`ClientDistMonitoringPage.tsx`）迁移而来，供
 * `OpsStatisticsTab` / `OpsRealtimeTab` / `OpsClientsTab` 复用；文案沿用既有 `clientDistOps.*` 键。
 */

/** 空值占位符。 */
export const OPS_EMPTY = '—'

/** 全频道哨兵值（频道选择器）。 */
export const OPS_ALL_CHANNELS = '__all__'
/** 分档「全部」哨兵值。 */
export const OPS_ALL = '__all__'

/** 跨页/页内联动链接上下文（运行态/日志维度）。 */
export type RuntimeLink = {
  machineId?: string
  runtimeVersion?: number
  version?: number
  coreVersion?: string
  platform?: string
  lag?: number
  errCode?: string
  ip?: string
}

/** 预设档 → 下游观测端点 range 枚举。 */
export function toApiRange(r: MetricRange): string {
  switch (r) {
    case '1h':
    case '6h':
    case '24h':
      return '24h'
    case '7d':
      return '7d'
    case '30d':
      return '30d'
    case '90d':
      return '90d'
    case '1y':
      return '180d'
    default:
      return '7d'
  }
}

/** 预设档 → FR-095 日看板天数。 */
export function toStatsDays(r: MetricRange): number {
  switch (r) {
    case '1h':
    case '6h':
    case '24h':
      return 1
    case '7d':
      return 7
    case '30d':
      return 30
    case '90d':
      return 90
    case '1y':
      return 180
    default:
      return 7
  }
}

/** 自定义窗口跨度 → 最接近的 MetricRange 档（供暂不支持 from/to 的下游查询继续工作）。 */
export function rangeForSpan(fromIso?: string, toIso?: string): MetricRange | null {
  if (!fromIso || !toIso) return null
  const f = new Date(fromIso)
  const t = new Date(toIso)
  if (Number.isNaN(f.getTime()) || Number.isNaN(t.getTime()) || t <= f) return null
  const hours = (t.getTime() - f.getTime()) / 3_600_000
  if (hours <= 24) return '24h'
  if (hours <= 24 * 7) return '7d'
  if (hours <= 24 * 30) return '30d'
  if (hours <= 24 * 90) return '90d'
  return '1y'
}

/** 字节数（紧凑）→ 人类可读。 */
export function fmtBytes(b: number): string {
  if (!Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1e9) return `${(b / 1024 / 1024 / 1024).toFixed(1)}G`
  if (b >= 1e6) return `${(b / 1024 / 1024).toFixed(0)}M`
  if (b >= 1e3) return `${(b / 1024).toFixed(0)}K`
  return String(b)
}

/** 比率 → 百分比字符串。 */
export function fmtRate(r: number): string {
  return `${((Number.isFinite(r) ? r : 0) * 100).toFixed(1)}%`
}

/** ISO 时间 → 本地化字符串（非法值原样返回）。 */
export function fmtTime(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

/** 平台 os 标识 → 展示名。 */
export function platformLabel(os: string): string {
  if (!os) return OPS_EMPTY
  const map: Record<string, string> = { windows: 'Windows', macos: 'macOS', linux: 'Linux' }
  return map[os] ?? os
}

/** 展示名 → 平台 os 标识（平台分布下钻用）。 */
export function reversePlatformLabel(label: string): string {
  const map: Record<string, string> = { Windows: 'windows', macOS: 'macos', Linux: 'linux' }
  return map[label] ?? label
}

/** 把一组条目聚合为占比桶（按计数降序）。 */
export function distBuckets<T>(
  items: T[],
  countOf: (it: T) => number,
  label: (it: T) => string,
): DistBucket[] {
  const total = items.reduce((s, it) => s + countOf(it), 0)
  return items
    .map((it) => ({ key: label(it), count: countOf(it), pct: total > 0 ? countOf(it) / total : 0 }))
    .sort((a, b) => b.count - a.count)
}

/** 请求侧 manifest 拉取数。 */
export function kindRequests(stats: ClientDistStats | undefined, kind: 'manifest' | 'artifact'): number {
  if (kind === 'manifest') return (stats?.versions ?? []).reduce((sum, v) => sum + v.requests, 0)
  return Math.max(0, (stats?.downloads ?? []).reduce((sum, d) => sum + d.requests, 0) - kindRequests(stats, 'manifest'))
}

/** 下载字节合计。 */
export function totalBytes(stats: ClientDistStats | undefined): number {
  return (stats?.downloads ?? []).reduce((sum, p) => sum + p.bytes, 0)
}

/** 结果标识 → 本地化标签。 */
export function resultLabel(result: string, t: (key: string) => string): string {
  if (result === 'success') return t('clientDistOps.resultSuccess')
  if (result === 'failure') return t('clientDistOps.resultFailure')
  return result || OPS_EMPTY
}

/** 更新事件种类 → 本地化标签。 */
export function kindLabel(kind: string, t: (key: string) => string): string {
  if (kind === 'manifest') return t('clientDistOps.kindManifest')
  if (kind === 'artifact') return t('clientDistOps.kindArtifact')
  return kind || OPS_EMPTY
}

/** 更新事件目标（版本号 / 制品前缀）。 */
export function targetOf(e: ClientDistEvent): string {
  if (e.kind === 'artifact') return e.artifactSha ? e.artifactSha.slice(0, 12) : OPS_EMPTY
  return e.version > 0 ? `v${e.version}` : OPS_EMPTY
}

/** 版本滞后 → 本地化标签。 */
export function lagLabel(lag: number, t: (key: string, opts?: Record<string, number>) => string): string {
  return lag === 0 ? t('clientDistOps.lagLatest') : t('clientDistOps.lagBehind', { n: lag })
}

/** 版本滞后标签 → 数值（下钻用）。 */
export function parseLagLabel(label: string): number {
  const n = Number(label.replace(/\D+/g, ''))
  return Number.isFinite(n) ? n : 0
}

/** 运行态更新结果序列 → 图表序列。 */
export function runtimeUpdateSeries(
  points: RuntimeUpdateSeriesPoint[],
  t: (key: string) => string,
): ChartSeries[] {
  return [
    { key: 'success', name: t('clientDistOps.updateSuccess'), points: points.map((p) => ({ ts: p.ts, value: p.success })) },
    { key: 'failStatic', name: t('clientDistOps.updateFailStatic'), points: points.map((p) => ({ ts: p.ts, value: p.failStatic })) },
    { key: 'rolledBack', name: t('clientDistOps.updateRolledBack'), points: points.map((p) => ({ ts: p.ts, value: p.rolledBack })) },
    { key: 'error', name: t('clientDistOps.updateError'), points: points.map((p) => ({ ts: p.ts, value: p.error })) },
  ]
}

/** 请求结果徽标（按状态码着色）。 */
export function ResultBadge({ status }: { status: number }) {
  const { t } = useTranslation()
  const failure = status >= 400
  return (
    <Badge variant={failure ? 'destructive' : 'secondary'}>
      {failure ? t('clientDistOps.resultFailure') : t('clientDistOps.resultSuccess')}
    </Badge>
  )
}

/** 纯展示分布面板（不可点）。 */
export function DistPanel({ title, buckets, empty }: { title: string; buckets: DistBucket[]; empty: string }) {
  return (
    <Panel title={title}>
      {buckets.length === 0 ? (
        <p className="py-6 text-center text-sm text-muted-foreground">{empty}</p>
      ) : (
        <ul className="space-y-2.5">
          {buckets.map((b) => (
            <li key={b.key} className="space-y-1">
              <div className="flex items-baseline justify-between text-sm">
                <span className="font-medium">{b.key}</span>
                <span className="tabular-nums text-muted-foreground">
                  {b.count}
                  <span className="ml-1.5 text-xs">{(b.pct * 100).toFixed(0)}%</span>
                </span>
              </div>
              <MiniBar value={b.pct * 100} level="info" />
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}

/** 可点分布面板（点条目回调下钻）。 */
export function LinkableDistPanel({
  title,
  buckets,
  empty,
  onPick,
}: {
  title: string
  buckets: DistBucket[]
  empty: string
  onPick: (key: string) => void
}) {
  return (
    <Panel title={title}>
      {buckets.length === 0 ? (
        <p className="py-6 text-center text-sm text-muted-foreground">{empty}</p>
      ) : (
        <ul className="space-y-2.5">
          {buckets.map((b) => (
            <li key={b.key} className="space-y-1">
              <button type="button" className="flex w-full items-baseline justify-between text-left text-sm" onClick={() => onPick(b.key)}>
                <span className="font-medium text-primary underline-offset-2 hover:underline">{b.key}</span>
                <span className="tabular-nums text-muted-foreground">
                  {b.count}
                  <span className="ml-1.5 text-xs">{(b.pct * 100).toFixed(0)}%</span>
                </span>
              </button>
              <MiniBar value={b.pct * 100} level="info" />
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}

/** 趋势卡（时序图 + 空态）。 */
export function TrendCard({
  title,
  series,
  valueFormatter,
  yDomain,
  empty,
}: {
  title: string
  series: ChartSeries[]
  valueFormatter?: (v: number) => string
  yDomain?: [number | 'auto', number | 'auto']
  empty: string
}) {
  return (
    <Panel title={title} bodyClassName="p-3">
      <TimeSeriesChart series={series} valueFormatter={valueFormatter} yDomain={yDomain} emptyHint={empty} />
    </Panel>
  )
}

/** 错误态面板（标题 + 消息）。 */
export function ErrorPanel({ title, message }: { title: string; message: string }) {
  return (
    <Panel title={title}>
      <p className="py-10 text-center text-sm text-muted-foreground">{message}</p>
    </Panel>
  )
}

/** 客户端运行态总览类型别名（供 wrapper props 复用）。 */
export type { ClientRuntimeOverview, ClientDistRealtime }
