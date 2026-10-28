import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Activity,
  ChevronDown,
  ChevronRight,
  Cpu,
  HardDrive,
  MemoryStick,
  TrendingUp,
  Gauge,
} from 'lucide-react'

import { cn } from '@jianmanager/ui'
import { TimeSeriesChart, type ChartSeries } from '@jianmanager/ui/charts/TimeSeriesChart'
import { Button } from '@jianmanager/ui/components/button'
import { useMetricOverview } from '@/api/metrics'
import type { NodeInfo } from '@/lib/nodes/node-types'
import type { NodeClusterSummary } from '@/lib/nodes/node-summary'

/**
 * 节点运行概览（页面级第二行）：集群平均水位 + **CPU 趋势（均值线 + 当前峰值标注）**。
 *
 * ### 为什么不是「每台一条线」
 * 上一版画 Top 5 逐台折线。在 30~100 台的规模下这不成立：
 * 100 台时只能覆盖 5 台（静默丢弃 95%），图例比线还长，可读性反而下降。
 * 这个区域要回答的是「**整个集群健不健康**」，单台历史属于详情页「监控」页签。
 *
 * ### 为什么是「均值 + 瞬时峰值标注」而不是「均值 + P95 曲线」
 * 均值趋势有真实数据源（`useMetricOverview` 的 `node_cpu_pct`，一次请求、与节点数无关）。
 * 但后端**没有**节点 CPU 的 P95 时间序列——把瞬时值画成一条「看起来是分位曲线」的线是伪造数据，
 * 所以峰值只作为**水平参考线**标注在图上（`referenceLines`），语义诚实：
 * 「均值这么走，当前最忙那台在这个水平」。
 *
 * ### 可折叠
 * KPI + 图表原占 508px（900 视口下 56%），导致表格一屏只能看 8 行。
 * 折叠后把高度还给表格，选择持久化到 `jm.nodes.overviewCollapsed`。
 */
const COLLAPSE_KEY = 'jm.nodes.overviewCollapsed'

function readCollapsed(): boolean {
  try {
    return localStorage.getItem(COLLAPSE_KEY) === '1'
  } catch {
    return false
  }
}

export function NodeRuntimeOverview({
  nodes,
  summary,
  className,
}: {
  nodes: NodeInfo[]
  summary: NodeClusterSummary
  className?: string
}) {
  const { t } = useTranslation()
  const [collapsed, setCollapsed] = useState(readCollapsed)

  const toggle = () => {
    setCollapsed((prev) => {
      const next = !prev
      try {
        localStorage.setItem(COLLAPSE_KEY, next ? '1' : '0')
      } catch {
        /* 忽略：无存储能力时仅本次会话有效 */
      }
      return next
    })
  }

  // 在线节点：离线节点没有实时水位，参与统计只会把均值拉低、把峰值算错。
  const onlineNodes = useMemo(() => nodes.filter((n) => n.status === 1), [nodes])

  // 当前最忙的那台（CPU 最高）：作为图上的瞬时峰值标注。
  const busiest = useMemo(() => {
    if (onlineNodes.length === 0) return null
    return onlineNodes.reduce((a, b) => ((b.cpuUsage ?? 0) > (a.cpuUsage ?? 0) ? b : a))
  }, [onlineNodes])
  const busiestPct = busiest ? Math.round((busiest.cpuUsage ?? 0) * 100) : null

  /*
    六项集群指标（两行三列）：
    上排 = CPU / 内存 / 磁盘的**集群均值**（summarizeNodes 按在线节点求均值）；
    下排 = 同样三项的**当前峰值**（在线节点里最高的那台）。
    峰值没有历史序列可用（后端不提供节点 CPU 分位时间序列），所以这里是瞬时快照，
    标签里明确写「峰值」而不是「P95」，避免被读成分位数统计。
  */
  const peak = (pick: (n: NodeInfo) => number | null | undefined): number | null => {
    if (onlineNodes.length === 0) return null
    const vals = onlineNodes.map((n) => (pick(n) ?? 0) * 100)
    return vals.length > 0 ? Math.max(...vals) : null
  }
  /*
    每项配图标（用户要求「节点运行概览页带上图标」）：
    上排均值用「部件图标」（Cpu / MemoryStick / HardDrive），下排峰值用 TrendingUp——
    同一部件的均值与峰值靠「部件图 + 箭头图」组合区分，比只看文字标签更快分辨。
  */
  const clusterMetrics: Array<{
    key: string
    label: string
    value: number | null
    hint: string
    icon: typeof Cpu
  }> = [
    { key: 'cpu', label: t('nodes.avgCpu', '平均 CPU'), value: summary.cpuPct, hint: t('nodes.avgCpuHint', '在线节点 CPU 使用率均值'), icon: Cpu },
    { key: 'mem', label: t('nodes.avgMem', '平均内存'), value: summary.memPct, hint: t('nodes.avgMemHint', '在线节点内存使用率均值'), icon: MemoryStick },
    { key: 'disk', label: t('nodes.avgDisk', '平均磁盘'), value: summary.diskPct, hint: t('nodes.avgDiskHint', '在线节点磁盘使用率均值'), icon: HardDrive },
    { key: 'cpuPeak', label: t('nodes.peakCpu', '峰值 CPU'), value: peak((n) => n.cpuUsage), hint: t('nodes.peakHint', '在线节点中的最高值（瞬时）'), icon: TrendingUp },
    { key: 'memPeak', label: t('nodes.peakMem', '峰值内存'), value: peak((n) => n.memoryUsage), hint: t('nodes.peakHint', '在线节点中的最高值（瞬时）'), icon: TrendingUp },
    { key: 'diskPeak', label: t('nodes.peakDisk', '峰值磁盘'), value: peak((n) => n.diskUsage), hint: t('nodes.peakHint', '在线节点中的最高值（瞬时）'), icon: TrendingUp },
  ]

  return (
    <section className={cn('rounded-lg border bg-card', className)} aria-label={t('nodes.runtimeOverview', '节点运行概览')}>
      {/* 折叠头：折叠后只剩这一行（约 32px），把高度还给表格。 */}
      <div className="flex items-center gap-2 px-3 py-1.5">
        <Button
          type="button"
          variant="invisible"
          size="auto"
          onClick={toggle}
          aria-expanded={!collapsed}
          className="inline-flex items-center gap-1 rounded px-1 py-0.5 text-xs font-semibold text-muted-foreground hover:text-foreground"
        >
          {collapsed ? <ChevronRight className="size-3.5" aria-hidden /> : <ChevronDown className="size-3.5" aria-hidden />}
          {/* 折叠头也带图标：它是整块区域的「身份标识」，收起后仍能一眼认出这是什么区。 */}
          <Gauge className="size-3.5 text-muted-foreground" aria-hidden />
          {t('nodes.runtimeOverview', '节点运行概览')}
        </Button>
        {/* 折叠态仍给出关键结论，避免「收起来就什么都不知道」。 */}
        {collapsed && (
          <span className="flex items-center gap-3 text-[11px] text-muted-foreground tabular-nums">
            <span>
              {t('nodes.clusterAvg', '集群平均水位')} CPU {summary.cpuPct == null ? '--' : `${Math.round(summary.cpuPct)}%`}
            </span>
            <span>
              {t('nodes.memory')} {summary.memPct == null ? '--' : `${Math.round(summary.memPct)}%`}
            </span>
            <span>
              {t('nodes.disk')} {summary.diskPct == null ? '--' : `${Math.round(summary.diskPct)}%`}
            </span>
            {busiest && busiestPct != null && (
              <span className="text-status-warning">
                {t('nodes.busiestHint', { name: busiest.name, pct: busiestPct, defaultValue: '最忙 {{name}} {{pct}}%' })}
              </span>
            )}
          </span>
        )}
      </div>

      {!collapsed && (
        <div className="grid gap-2.5 border-t border-border/60 p-3 lg:grid-cols-[minmax(0,1fr)_minmax(0,2.4fr)]">
          {/*
            左：集群指标 6 项，**两行三列**（用户要求）。
            每项做成一枚小 KPI 卡（label + 大数字 + 图标位），而不是原来三枚裸数字——
            裸数字与右侧图表在同一视觉层级上分不开「指标」和「图表」。
            六项覆盖「CPU / 内存 / 磁盘」× 「均值 / 峰值」：均值看整体，峰值看最差那台，
            两组放在一起才能回答「集群是否健康、有没有个别机器拖后腿」。
          */}
          {/*
            【为什么每张卡带一条水位条】初版只有「标签 + 数字」两行，卡片却被网格拉到与右侧
            图表同高（168px），下半截全是空白——用户反馈「6 张指标卡太空了」。
            加一条按百分比填充的水位条后：① 空白被有意义的内容填满；
            ② 补上「这个数相对 100% 有多满」的直观量感（89% 与 47% 的文字差别远不如条长明显）。
            条的颜色跟数值同源（≥90 危险 / ≥80 注意 / 其余正常），与表格里的水位条一致。
          */}
          <div
            className="grid grid-cols-3 grid-rows-2 gap-1.5"
            aria-label={t('nodes.clusterAvg', '集群平均水位')}
          >
            {clusterMetrics.map((m) => {
              const tone = m.value == null ? 'neutral' : m.value >= 90 ? 'danger' : m.value >= 80 ? 'warning' : 'success'
              const pct = m.value == null ? 0 : Math.min(100, Math.max(0, m.value))
              return (
                <div
                  key={m.key}
                  data-testid={`nodes-avg-${m.key}`}
                  className="flex min-h-0 flex-col justify-between rounded-md border bg-background/60 px-2 py-1.5"
                  title={m.hint}
                >
                  <span className="flex items-center gap-1 text-[10px] leading-tight text-muted-foreground">
                    <m.icon className="size-3 shrink-0" aria-hidden="true" />
                    <span className="truncate">{m.label}</span>
                  </span>
                  <span
                    className={cn(
                      'text-base font-semibold leading-none tabular-nums',
                      tone === 'danger' && 'text-status-danger',
                      tone === 'warning' && 'text-status-warning',
                      tone === 'success' && 'text-status-success',
                      tone === 'neutral' && 'text-muted-foreground',
                    )}
                  >
                    {m.value == null ? '--' : `${Math.round(m.value)}%`}
                  </span>
                  {/* 水位条：填满卡片剩余空间，同时表达「离满还有多远」。 */}
                  <div className="h-1 w-full overflow-hidden rounded-full bg-muted">
                    <div
                      className={cn(
                        'h-full rounded-full transition-[width] duration-300',
                        tone === 'danger' && 'bg-status-danger',
                        tone === 'warning' && 'bg-status-warning',
                        tone === 'success' && 'bg-status-success',
                        tone === 'neutral' && 'bg-muted-foreground/40',
                      )}
                      style={{ width: `${pct}%` }}
                    />
                  </div>
                </div>
              )
            })}
          </div>

          {/* 右：CPU 趋势（均值线 + 当前峰值参考线） */}
          <div className="flex min-w-0 flex-col">
            <div className="mb-1 flex items-baseline justify-between">
              <h3 className="flex items-center gap-1 text-xs font-semibold text-muted-foreground">
                <Activity className="size-3.5" aria-hidden="true" />
                {t('nodes.cpuTrend', '各节点 CPU 趋势')}
              </h3>
              <span className="text-[11px] text-muted-foreground/70">{t('nodes.last24h', '近 24 小时')}</span>
            </div>
            <div className="h-[168px]" data-testid="nodes-cpu-chart">
              <ClusterCpuTrend busiest={busiest} busiestPct={busiestPct} onlineCount={onlineNodes.length} />
            </div>
          </div>
        </div>
      )}
    </section>
  )
}

/**
 * 集群 CPU 趋势：一条均值线 +「当前最忙节点」水平参考线。
 *
 * 数据源：`useMetricOverview` 的 `node_cpu_pct`——集群级聚合，一次请求，与节点数无关。
 * 这正是它能扛住 100 台的原因（上一版按节点取历史是 N 个并发请求）。
 */
function ClusterCpuTrend({
  busiest,
  busiestPct,
  onlineCount,
}: {
  busiest: NodeInfo | null
  busiestPct: number | null
  onlineCount: number
}) {
  const { t } = useTranslation()
  const { data, isLoading } = useMetricOverview('24h')

  const series: ChartSeries[] = useMemo(() => {
    const trend = data?.trends?.find((x) => x.metricKey === 'node_cpu_pct')
    if (!trend || trend.points.length === 0) return []
    return [
      {
        key: 'cluster-avg',
        name: t('nodes.cpuAvgLine', '集群平均'),
        // SeriesPoint 用 avg（降采样档位下为区间均值），null 由图表渲染成断点。
        points: trend.points.map((p) => ({ ts: p.ts, value: p.avg })),
      },
    ]
  }, [data, t])

  if (onlineCount === 0) {
    return <p className="py-10 text-center text-xs text-muted-foreground">{t('nodes.noOnlineNode')}</p>
  }
  if (isLoading) {
    return <p className="py-10 text-center text-xs text-muted-foreground">{t('common.loading', '加载中…')}</p>
  }
  if (series.length === 0) {
    return <p className="py-10 text-center text-xs text-muted-foreground">{t('nodes.noTrendData')}</p>
  }

  return (
    <TimeSeriesChart
      series={series}
      height={160}
      yDomain={[0, 100]}
      valueFormatter={(v) => `${v.toFixed(0)}%`}
      referenceLines={
        busiestPct != null && busiest
          ? [
              {
                value: busiestPct,
                label: t('nodes.busiestLine', { name: busiest.name, pct: busiestPct, defaultValue: '最忙 {{name}} {{pct}}%' }),
                color: 'var(--status-warning)',
              },
            ]
          : []
      }
      ariaLabel={t('nodes.cpuTrendAria', { defaultValue: '集群 CPU 使用率趋势' })}
    />
  )
}
