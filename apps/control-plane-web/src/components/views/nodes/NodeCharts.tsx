/* eslint-disable react-refresh/only-export-components -- 视图与随其导出的纯逻辑/变体同文件（沿用原组织方式），非组件导出按仓库约定在此豁免 */
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Panel } from '@jianmanager/ui/components/panel'
import { Segments, Segment } from '@jianmanager/ui/components/layout'
import { Badge } from '@jianmanager/ui/components/badge'
import { CalendarRange } from 'lucide-react'
import { RangePicker, type MetricRange } from '@jianmanager/ui/charts/RangePicker'
import { TimeSeriesChart, type ChartSeries } from '@jianmanager/ui/charts/TimeSeriesChart'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { formatFileSize } from '@/lib/shared/format-file-size'
import { useProcessTop, type ProcessTopItem } from '@/api/metrics'

/** 单个对比指标的规格（#2）。 */
export interface CompareMetric {
  key: string
  labelKey: string
  fmt: (v: number) => string
}

/** 各实例对比图可切的指标（#2：节点上各实例 TPS/MSPT/堆/线程对比）。
 * 声明为非空元组：首项恒存在，`NodeInstanceCompare` 的默认指标无需额外判空。 */
export const COMPARE_METRICS: [CompareMetric, ...CompareMetric[]] = [
  { key: 'inst_tps', labelKey: 'metrics.tps', fmt: (v) => v.toFixed(1) },
  { key: 'inst_mspt', labelKey: 'metrics.mspt', fmt: (v) => `${v.toFixed(1)}ms` },
  { key: 'inst_heap_used', labelKey: 'metrics.heap', fmt: formatFileSize },
  { key: 'inst_threads', labelKey: 'metrics.threads', fmt: (v) => v.toFixed(0) },
]

/** 对比图可读性上限：一图最多 12 条线（超出仅取名称升序前 12）。50 是后端硬上限。 */
export const COMPARE_TARGET_CAP = 12

export interface NodeInstanceCompareProps {
  /** 当前指标键（容器持有：它决定批量取数）。 */
  metric: string
  onMetricChange: (key: string) => void
  /** 该节点实例总数（用于截断提示）。 */
  total: number
  /** 当前指标下各实例的序列（容器取数后组装）。 */
  series: ChartSeries[]
  /** 实例清单是否仍在加载（容器注入）。 */
  isLoading?: boolean
  /** 取数是否失败（容器注入）。 */
  isError?: boolean
}

export interface NodeMonitorChartsProps {
  /** 时间范围状态（容器持有：它决定取数，并渲染控件）。 */
  range: NodeMetricRangeState
  /** 节点序列数据（容器经 useMetricSeries 取数）。 */
  series: { metricKey: string; points: { ts: string; avg: number | null }[] }[]
  /** 
   * 节点 UUID：用于悬浮时按「这一刻」查该节点的进程 TOPN。
   * 省略则不做归属查询，图表行为与改造前一致。
   */
  nodeUuid?: string
  /** 
   * 序列档位（后端 `resolution`）：raw / 5m / 1h。
   * 仅用于「区间总消耗」的桶宽兜底——点数太少时无法从序列推出桶宽。
   */
  resolution?: string
}

/** 
 * 节点上各实例同一指标对比：每实例一条线，可切 TPS/MSPT/堆/线程（#2）。
 *：实例清单走服务端按节点分页（`/instances/search`）取前 12（名称升序），
 * 指标一次批量查询（`/metrics/series/batch`）拆分为各线，消 N+1 请求风暴。
 */
export function NodeInstanceCompare({
  metric,
  onMetricChange,
  total,
  series,
  isLoading,
  isError,
}: NodeInstanceCompareProps) {
  const { t } = useTranslation()
  const spec = COMPARE_METRICS.find((m) => m.key === metric) ?? COMPARE_METRICS[0]

  return (
    <Panel
      title={t('nodes.instanceCompare')}
      actions={
        // 轨道名复用面板既有标题键：本分段切换的正是「对比哪个实例指标」。
        // `role=tablist` 需要可达名，否则读屏只念「标签页列表」。
        <Segments aria-label={t('nodes.instanceCompare')}>
          {COMPARE_METRICS.map((m) => (
            <Segment key={m.key} active={metric === m.key} onClick={() => onMetricChange(m.key)}>
              {t(m.labelKey)}
            </Segment>
          ))}
        </Segments>
      }
    >
      {/* 错误态优先于空态：取数失败时列表为空，若直接显示「暂无实例」就是把故障说成没数据。 */}
      {isError ? (
        <p role="alert" className="py-10 text-center text-sm text-destructive">
          {t('common.error')}
        </p>
      ) : isLoading ? (
        // 骨架而非空态：加载中显示「暂无实例」会让人以为这节点真没有实例。
        <Skeleton className="h-[180px] w-full" />
      ) : (
        <>
          {total > COMPARE_TARGET_CAP && (
            <p className="mb-2 text-xs text-muted-foreground">
              {t('nodes.compareCap', { shown: COMPARE_TARGET_CAP, total })}
            </p>
          )}
          <TimeSeriesChart
            series={series}
            height={180}
            valueFormatter={spec.fmt}
            // 这里空的是「本节点的实例」，不是「平台没有节点」，不能复用 nodes.empty。
            emptyHint={t('nodes.instanceCompareEmpty')}
          />
        </>
      )}
    </Panel>
  )
}

/** 详情「监控」分段：节点历史曲线组（CPU/内存/磁盘/网络/负载）。 */
/** 进程归属的排序维度，与后端 `sort` 参数一致（后端只支持这三档）。 */
type ProcSort = 'cpu' | 'memory' | 'io'

/** 该项的数值展示：CPU 百分比、内存常驻、磁盘读写速率。 */
function procValue(p: ProcessTopItem, sort: ProcSort): string {
  if (sort === 'cpu') return `${p.cpuPercent.toFixed(1)}%`
  if (sort === 'memory') return formatFileSize(p.rssBytes)
  return `${formatFileSize(p.readBytesPerSec)}/s · ${formatFileSize(p.writeBytesPerSec)}/s`
}

/** 
 * 进程 TOPN 列表：PID + 应用名 + 该项占用，第二行给「位置」线索（运行用户 + 命令行摘要）。
 *
 * 【位置为何这样取】进程快照没有文件系统路径字段，`commandSummary` 里的 jar / 工作目录
 * 与 `user` 是最接近「位置」的信息；两者拼一行，过长截断，悬停 title 看全文。
 */
function ProcessTopList({ items, sort }: { items: ProcessTopItem[]; sort: ProcSort }) {
  const { t } = useTranslation()
  return (
    <>
      {items.map((p) => (
        <div key={`${p.instanceUuid}:${p.pid}`} className="text-[11px] leading-snug">
          <div className="flex items-baseline justify-between gap-2">
            <span className="truncate">
              <span className="font-mono text-muted-foreground">{p.pid}</span>{' '}
              <span className="font-medium">{p.name}</span>
            </span>
            <span className="shrink-0 tabular-nums">{procValue(p, sort)}</span>
          </div>
          <div className="truncate text-[10px] text-muted-foreground" title={p.commandSummary}>
            {t('nodes.procLocation', { user: p.user, cmd: p.commandSummary || '—' })}
          </div>
        </div>
      ))}
    </>
  )
}

/** 
 * 带进程归属的曲线：鼠标悬浮时按该时刻查节点进程 TOPN 并渲染进浮层。
 *
 * 【为什么单独成组件】`useState` + `useProcessTop` 必须跟着「这一条曲线」走；
 * 若写在 `NodeMonitorCharts` 里，等于按图表数量硬编码多个 hook，加图就得改代码。
 *
 * 【取数时机】`at` 为空时 `enabled=false`，鼠标移开即停；`at` 进入 queryKey，
 * 不同时刻是不同缓存条目，来回悬浮不会重复打后端。
 */
function AttributedChart({
  series,
  nodeUuid,
  sort,
  height,
  valueFormatter,
  title,
}: {
  series: ChartSeries[]
  nodeUuid?: string
  sort: ProcSort
  height: number
  valueFormatter: (v: number) => string
  title: string
}) {
  const { t } = useTranslation()
  const [at, setAt] = useState<string | null>(null)
  const { data, isFetching } = useProcessTop({
    nodeId: nodeUuid,
    sort,
    limit: 5,
    at: at ?? undefined,
    enabled: !!nodeUuid && !!at,
  })

  if (!nodeUuid) {
    return <TimeSeriesChart series={series} height={height} valueFormatter={valueFormatter} />
  }

  return (
    <TimeSeriesChart
      series={series}
      height={height}
      valueFormatter={valueFormatter}
      onHoverTs={setAt}
      tooltipExtra={() => (
        <div className="mt-1 space-y-1 border-t border-border pt-1.5">
          <div className="text-[11px] font-medium text-muted-foreground">{title}</div>
          {data && data.length > 0 ? (
            <ProcessTopList items={data} sort={sort} />
          ) : (
            <div className="text-[11px] text-muted-foreground">
              {isFetching ? t('nodes.procQuerying') : t('nodes.procEmpty')}
            </div>
          )}
        </div>
      )}
    />
  )
}

/** 档位 → 名义桶宽（秒），与后端 `selectResolution` 的三档一致。 */
const RESOLUTION_STEP_SEC: Record<string, number> = { raw: 30, '5m': 300, '1h': 3600 }

/** raw 档的兜底桶宽（秒）：与后端注释里的心跳采样节奏一致。 */
const RAW_STEP_FALLBACK_SEC = 30

/** 相邻点间隔的中位数；没有可用的正间隔返回 0。 */
function medianStepSec(points: { ts: string }[]): number {
  const diffs: number[] = []
  for (let i = 1; i < points.length; i += 1) {
    const prev = points[i - 1]
    const cur = points[i]
    if (!prev || !cur) continue
    const d = (Date.parse(cur.ts) - Date.parse(prev.ts)) / 1000
    if (Number.isFinite(d) && d > 0) diffs.push(d)
  }
  if (diffs.length === 0) return 0
  diffs.sort((a, b) => a - b)
  return diffs[Math.floor(diffs.length / 2)] ?? 0
}

/** 
 * 定出「一个点代表多长时间」，也就是积分用的桶宽。
 *
 * 【为什么分档位走不同路子】5m / 1h 桶宽是后端写死的（300s / 3600s），直接用名义值最准；
 * raw 档存的是心跳那一瞬的采样，真实间隔由 Worker 心跳周期决定（配置可变），所以用相邻点
 * 间隔的**中位数**去量，个别缺口带不偏它（均值会被带偏）。
 */
export function resolveBucketSec(
  points: { ts: string }[],
  resolution: string | undefined,
  nominalRawSec: number = RAW_STEP_FALLBACK_SEC,
): number {
  const nominal = resolution ? RESOLUTION_STEP_SEC[resolution] : undefined
  if (nominal && resolution !== 'raw') return nominal
  return medianStepSec(points) || nominal || nominalRawSec
}

/** 
 * 把速率序列（bytes/s）积成区间总消耗（字节）：网络面板用它回答「这段时间用了多少流量」。
 *
 * 【口径】5m / 1h 降采样存的是桶内 `AVG(rate)`，所以「每个点 × 桶宽」正是该桶的字节数；
 * raw 档是瞬时采样，按桶宽做左矩形近似。
 *
 * 【缺口】缺失的桶根本没有点，自然不计入——宁可少算，也不拿一个点的均值去乘跨越缺口的
 * 大间隔，那会把缺失时段的流量凭空编出来。
 */
export function integrateRateBytes(
  points: { ts: string; value: number | null }[],
  stepSec: number,
): number {
  if (!Number.isFinite(stepSec) || stepSec <= 0) return 0
  let total = 0
  for (const p of points) {
    if (p.value == null || !Number.isFinite(p.value)) continue
    total += p.value * stepSec
  }
  return total
}

/** 「YYYY-MM-DDTHH:mm」→ RFC3339（UTC）；空值或非法值给 undefined。 */
function toIso(v: string): string | undefined {
  if (!v) return undefined
  const d = new Date(v)
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString()
}

/** 监控页签的时间范围状态（容器持有，视图只渲染）。 */
export interface NodeMetricRangeState {
  range: MetricRange
  customFrom: string
  customTo: string
  /** 自定义窗口是否成立（填齐且结束晚于开始）——成立时才优先于预设。 */
  customValid: boolean
  /** 已生效的自定义窗口（RFC3339）；未成立时为 undefined。 */
  from?: string
  to?: string
  /** 正在取数（用于在控件右侧显示「加载中」）。 */
  isFetching?: boolean
  onRangeChange: (r: MetricRange) => void
  onCustomFrom: (v: string) => void
  onCustomTo: (v: string) => void
}

/** 
 * 监控页签的时间范围：预设档位 + 自定义起止窗口。
 *
 * 【为什么预设与自定义互斥】两套条件同时生效时，用户无法判断眼前这条曲线按哪个算。
 * 选预设即清空自定义窗口，是这里唯一的取舍点。
 */
/** 
 * 自定义窗口是否可用：两个输入都合法、且结束严格晚于开始。
 *
 * 抽成纯函数是为了能脱离 DOM 测——「开始晚于结束」「只填了一头」「乱填」这三种
 * 都必须是不可用（否则会拿半截条件去打后端）。
 */
export function resolveCustomWindow(
  customFrom: string,
  customTo: string,
): { customValid: boolean; from?: string; to?: string } {
  const from = toIso(customFrom)
  const to = toIso(customTo)
  const customValid = !!from && !!to && new Date(to) > new Date(from)
  return customValid ? { customValid, from, to } : { customValid }
}

export function useNodeMetricRange(defaultRange: MetricRange = '24h'): Omit<NodeMetricRangeState, 'isFetching'> {
  const [range, setRange] = useState<MetricRange>(defaultRange)
  const [customFrom, setCustomFrom] = useState('')
  const [customTo, setCustomTo] = useState('')
  const { customValid, from, to } = resolveCustomWindow(customFrom, customTo)
  return {
    range,
    customFrom,
    customTo,
    customValid,
    ...(from && to ? { from, to } : {}),
    onRangeChange: (next) => {
      // 选预设即放弃自定义窗口，避免两套条件同时生效。
      setCustomFrom('')
      setCustomTo('')
      setRange(next)
    },
    onCustomFrom: setCustomFrom,
    onCustomTo: setCustomTo,
  }
}

/** 
 * 时间范围控件：预设档位 + 两个 datetime-local + 非法提示 + 保留期说明。
 *
 * 原先是概览页签四张图上方那条，随「曲线统一收进监控」一起搬过来——
 * 用户要的「时间可以自己选」应该出现在看历史的那一页，而不是概览。
 */
export function MetricRangeBar({ state }: { state: NodeMetricRangeState }) {
  const { t } = useTranslation()
  const { range, customFrom, customTo, customValid, isFetching } = state
  return (
    <div className="flex flex-wrap items-center gap-2" data-testid="node-metric-range-bar">
      <span className="flex items-center gap-1 text-[11px] text-muted-foreground">
        <CalendarRange className="size-3.5" aria-hidden />
        {t('nodes.timeRange', '时间范围')}
      </span>
      <RangePicker value={range} onChange={state.onRangeChange} />
      <span className="flex items-center gap-1 text-[11px] text-muted-foreground">
        <input
          type="datetime-local"
          value={customFrom}
          onChange={(e) => state.onCustomFrom(e.target.value)}
          aria-label={t('nodes.rangeFrom', '起始时间')}
          className="h-7 rounded border bg-background px-1.5 text-[11px] text-foreground"
        />
        <span aria-hidden>–</span>
        <input
          type="datetime-local"
          value={customTo}
          onChange={(e) => state.onCustomTo(e.target.value)}
          aria-label={t('nodes.rangeTo', '结束时间')}
          className="h-7 rounded border bg-background px-1.5 text-[11px] text-foreground"
        />
      </span>
      {(customFrom || customTo) && !customValid && (
        <span className="text-[11px] text-status-warning">{t('nodes.rangeInvalid', '结束时间需晚于起始时间')}</span>
      )}
      {customValid && (
        <Badge variant="outline" className="h-5 px-1.5 text-[10px] text-primary">
          {t('nodes.rangeCustom', '自定义')}
        </Badge>
      )}
      {isFetching && <span className="text-[11px] text-muted-foreground">{t('common.loading', '加载中…')}</span>}
      <span
        className="ml-auto text-[11px] text-muted-foreground/70"
        title={t('nodes.retentionHint', '保留期由平台统一的降采样与清理策略决定，当前不可在线修改')}
      >
        {t('nodes.retentionNote', '保留：原始 48h · 5m 聚合 30d · 1h 聚合 400d')}
      </span>
    </div>
  )
}

export function NodeMonitorCharts({
  range,
  series: nodeSeries,
  nodeUuid,
  resolution,
}: NodeMonitorChartsProps) {
  const { t } = useTranslation()

  const pointsOf = (metricKey: string) => nodeSeries.find((x) => x.metricKey === metricKey)?.points ?? []
  const seriesOf = (metricKey: string, name: string): ChartSeries[] => {
    const pts = pointsOf(metricKey)
    if (pts.length === 0) return []
    return [{ key: metricKey, name, points: pts.map((p) => ({ ts: p.ts, value: p.avg })) }]
  }
  const netSeries: ChartSeries[] = [
    ...seriesOf('node_net_rx_rate', t('nodes.netRx')),
    ...seriesOf('node_net_tx_rate', t('nodes.netTx')),
  ]
  // 区间总消耗（用户口径：网络只要「当前速率 + 总消耗」，不做进程归属）。
  const rxPoints = pointsOf('node_net_rx_rate')
  const txPoints = pointsOf('node_net_tx_rate')
  // 上下行同档位、同区间，桶宽共用一个（按点多的那条量，缺测时更稳）。
  const netStepSec = resolveBucketSec(rxPoints.length >= txPoints.length ? rxPoints : txPoints, resolution)
  const netRxTotal = integrateRateBytes(
    rxPoints.map((p) => ({ ts: p.ts, value: p.avg })),
    netStepSec,
  )
  const netTxTotal = integrateRateBytes(
    txPoints.map((p) => ({ ts: p.ts, value: p.avg })),
    netStepSec,
  )

  return (
    <div className="space-y-3">
      <MetricRangeBar state={range} />
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        {/*
          负载置顶且占满两列：负载是「这台机器还扛不扛得住」的第一信号，
          横跨整行后曲线形态（尖峰/持续爬升）才看得清；其余四张按双列排在下面。
        */}
        <Panel className="lg:col-span-2" title={t('nodes.loadTrend')}>
          <TimeSeriesChart
            series={seriesOf('node_load', t('nodes.load'))}
            height={160}
            valueFormatter={(v) => v.toFixed(2)}
          />
        </Panel>
        <Panel title={t('dashboard.cpuTrend')}>
          <AttributedChart
            series={seriesOf('node_cpu_pct', t('nodes.cpu'))}
            nodeUuid={nodeUuid}
            sort="cpu"
            height={160}
            valueFormatter={(v) => `${v.toFixed(0)}%`}
            title={t('nodes.procCpuTop')}
          />
        </Panel>
        <Panel title={t('dashboard.memTrend')}>
          <AttributedChart
            series={seriesOf('node_mem_used', t('nodes.memory'))}
            nodeUuid={nodeUuid}
            sort="memory"
            height={160}
            valueFormatter={(v) => formatFileSize(v)}
            title={t('nodes.procMemTop')}
          />
        </Panel>
        <Panel title={t('nodes.diskTrend')}>
          <AttributedChart
            series={seriesOf('node_disk_used', t('nodes.disk'))}
            nodeUuid={nodeUuid}
            sort="io"
            height={160}
            valueFormatter={(v) => formatFileSize(v)}
            title={t('nodes.procDiskTop')}
          />
        </Panel>
        <Panel title={t('nodes.netTrend')}>
          {/* 网络只报速率与区间总消耗：不按进程归属（快照没有 per-process 网络字段）。 */}
          <TimeSeriesChart
            series={netSeries}
            height={160}
            valueFormatter={(v) => `${formatFileSize(v)}/s`}
          />
          <div
            className="mt-1 flex flex-wrap items-baseline gap-x-3 text-[11px] text-muted-foreground"
            data-testid="net-range-total"
          >
            <span>{t('nodes.netRangeTotal')}</span>
            <span className="tabular-nums">
              {t('nodes.netRx')} {netSeries.length > 0 ? formatFileSize(netRxTotal) : '--'}
            </span>
            <span className="tabular-nums">
              {t('nodes.netTx')} {netSeries.length > 0 ? formatFileSize(netTxTotal) : '--'}
            </span>
          </div>
        </Panel>
      </div>
    </div>
  )
}
