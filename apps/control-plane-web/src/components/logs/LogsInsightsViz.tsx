import { useTranslation } from 'react-i18next'
import { cn } from '@jianmanager/ui'
import { Tooltip, TooltipContent, TooltipTrigger } from '@jianmanager/ui/components/tooltip'

/**
 * 日志可视化条（洞察栏）：级别分布、Top 来源、时间分布。
 *
 * # 为何把这些单独成文件
 *
 * LogsPage 已经 1600+ 行。可视化是一块**纯展示**逻辑（只吃数据、无交互状态、
 * 无取数），跟查询编排、游标推进这些生命周期完全无关；混在一个文件里只会让
 * 两者都难读。
 *
 * # 为何不用 recharts 画这几块
 *
 * recharts 是重库（已为其他页引入）。这里要的是：堆叠条、横向条形、迷你柱阵。
 * 三者都是「高度/宽度 = 比例」的简单矩形，手写不到 100 行，而且能精准控制
 * 颜色吃 `status-*` token 与无障碍语义。为它们引一张完整图表引擎，代价不成比例。
 * 真正需要坐标轴、缩放、图例的时候（将来做「点击柱状图缩放到时段」）才上 recharts。
 */

/** 一个维度值及其计数。 */
export interface FacetValue {
  value: string
  count: number
}

/**
 * 级别分布堆叠条。
 *
 * # 为何用堆叠条而不是饼图
 *
 * 饼图在「两个值占 2/3 与 1/3」这种比例上很难目测比大小，而日志的级别分布
 * 恰好常年是「info 绝大多数 + 少量 warn/error」的偏态数据。堆叠条的长度可直接比较，
 * 而且能塞进一行高度里（洞察栏本就这么高）。Kibana/Splunk 也都是横向堆叠。
 */
export function LevelDistributionBar({
  facets,
  total,
  uncertain,
}: {
  facets: FacetValue[]
  /** 分母；不传时用 facets 自加（注意：facets 可能被截断，自加会偏小）。 */
  total?: number
  /** 计数可能不精确（覆盖不完整 / 维度被截断）：显示时加 ≈ 前缀。 */
  uncertain?: boolean
}) {
  const { t } = useTranslation()
  const sum = total ?? facets.reduce((n, f) => n + f.count, 0)
  if (!facets.length || sum <= 0) return null

  // 颜色与日志行左侧色条同源（status token），不另造一套。
  const colorOf = (level: string): string => {
    switch (level.toLowerCase()) {
      case 'error':
      case 'fatal':
        return 'bg-status-danger'
      case 'warn':
      case 'warning':
        return 'bg-status-warning'
      case 'info':
        return 'bg-status-info'
      default:
        return 'bg-muted-foreground/50'
    }
  }

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <div
          data-testid="logs-level-distribution"
          className="flex h-4 w-40 shrink-0 overflow-hidden rounded-sm bg-muted"
          role="img"
          aria-label={t('logs.vizLevelAria', {
            detail: facets.map((f) => `${f.value} ${f.count}`).join(', '),
          })}
          // 具体数值也落到 DOM 上：图形本身对读屏不可读，而 aria-label 在
          // 未加载 i18n 资源的测试环境里会退化成 key。这里给的是不经翻译的事实数据。
          data-level-detail={facets.map((f) => `${f.value}:${f.count}`).join(',')}
        >
          {facets.map((f) => (
            <div
              key={f.value}
              className={cn('h-full transition-[width]', colorOf(f.value))}
              style={{ width: `${(f.count / sum) * 100}%` }}
            />
          ))}
        </div>
      </TooltipTrigger>
      <TooltipContent>
        <div className="flex flex-col gap-0.5">
          {facets.map((f) => (
            <span key={f.value}>
              {f.value}: {uncertain ? '≈' : ''}
              {f.count} ({((f.count / sum) * 100).toFixed(1)}%)
            </span>
          ))}
        </div>
      </TooltipContent>
    </Tooltip>
  )
}

/**
 * Top 来源横向条形（「哪个源在刷屏」的直接答案）。
 *
 * # 为何不用饼图
 *
 * 来源经常是长尾（一两个源占 90%，其余几十个各占一点）。条形能同时表达
 * 「值」（长度）与「名」（左侧标签），而饼图切多了就看不清谁是谁。
 */
export function TopSourcesBars({
  items,
  limit = 5,
}: {
  items: { id: string; count: number }[]
  limit?: number
}) {
  const top = [...items].sort((a, b) => b.count - a.count).slice(0, limit)
  if (!top.length) return null
  const max = Math.max(...top.map((i) => i.count), 1)

  return (
    <div data-testid="logs-top-sources" className="flex flex-col gap-1">
      {top.map((item) => (
        <div key={item.id} className="flex items-center gap-2">
          <span
            className="w-28 shrink-0 truncate font-mono text-[10px] text-muted-foreground"
            title={item.id}
          >
            {item.id}
          </span>
          <div className="h-1.5 w-24 overflow-hidden rounded-full bg-muted">
            <div
              className="h-full rounded-full bg-status-info"
              style={{ width: `${(item.count / max) * 100}%` }}
            />
          </div>
          <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground">
            {item.count}
          </span>
        </div>
      ))}
    </div>
  )
}

/**
 * 时间分布迷你柱阵（按分钟/小时分桶）。
 *
 * # 为何从当前页算而不问后端
 *
 * 后端的 `/stats` 目前只返回**单个聚合值**（`agg.count`），没有时间序列；
 * 要真做时间直方图得加一个后端接口。而当前页 100 行已足够回答用户在这个
 * 尺度上的问题：「我筛出来的这批日志是集中在某个时刻，还是平均散开的」。
 *
 * # 代价必须说清
 *
 * 它只统计**当前页**，不是全量。所以标题写「本页」，不冒充全量趋势。
 * 若将来后端提供时序，这里只需换数据源，图形不用动。
 */
export function TimeDistributionBars({
  /* 已排序的时间戳（毫秒）。 */
  timestamps,
}: {
  timestamps: number[]
}) {
  const { t } = useTranslation()
  if (timestamps.length < 2) return null

  const min = Math.min(...timestamps)
  const max = Math.max(...timestamps)
  // 全部落在同一毫秒：无分布可言，不画。
  if (max === min) return null

  const BUCKETS = 24
  const span = max - min
  const width = span / BUCKETS
  const buckets = new Array<number>(BUCKETS).fill(0)
  for (const ts of timestamps) {
    const i = Math.min(BUCKETS - 1, Math.floor((ts - min) / width))
    // 下标非负：ts>=min 保证 floor 不出现 -0 以下的边界情况，但显式取 max 更稳。
    const idx = Math.max(0, i)
    buckets[idx] = (buckets[idx] ?? 0) + 1
  }
  const peak = Math.max(...buckets, 1)

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <div
          data-testid="logs-time-distribution"
          className="flex h-4 items-end gap-px"
          role="img"
          aria-label={t('logs.vizTimeAria')}
        >
          {buckets.map((n, i) => (
            <div
              key={i}
              className={cn('w-1 rounded-t-[1px] bg-status-info/70', n === 0 && 'bg-muted')}
              // 最小 1px：否则「有数据但极少」的桶完全不可见，看上去像没数据。
              style={{ height: `${Math.max(n === 0 ? 1 : 2, (n / peak) * 100)}%` }}
            />
          ))}
        </div>
      </TooltipTrigger>
      <TooltipContent>
        {t('logs.vizTimeHelp', {
          from: new Date(min).toLocaleString(),
          to: new Date(max).toLocaleString(),
        })}
      </TooltipContent>
    </Tooltip>
  )
}
