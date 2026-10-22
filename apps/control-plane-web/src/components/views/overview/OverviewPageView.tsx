/**
 * @file OverviewPageView：概览页（`/`）的受控视图——六个查询的结果与错误态、平台观测区可见性、
 *       健康墙 / 链接 / 按需续取三处接线全由应用容器注入。
 * @input lib/instance-types（InstanceInfo）、lib/task-status（Task、TaskState）、lib/alert-contracts（AlertEventInfo）、
 *        lib/alert-helpers（levelStatusLevel）、lib/threshold（instanceStatusLevel、StatusLevel）、lib/virtual-list（useVirtualRows）、
 *        Panel / StatCard / StatusBadge / ResourceGauge / Skeleton / Table 原语、layout（PageShell / PageHeader）、
 *        charts（RangePicker 静态、TimeSeriesChart 懒加载）、翻译上下文
 * @output OverviewPageView、OverviewPageViewProps、OverviewAggregationPanel、OverviewAggregationPanelProps、
 *         GaugeAttributionTooltip、GaugeAttributionTooltipProps、PlatformHealthPanel、PlatformExceptionsPanel、
 *         PlatformBotRuntimePanel、PlatformPanelProps、OverviewInstancePanel、OverviewInstancePanelProps、
 *         AttributionGauge、OverviewLinkArgs、
 *         OverviewMetricTotals、OverviewMetricTrend、OverviewMetricSnapshot、OverviewObservabilitySnapshot、OverviewAttributionSnapshot
 * @sync apps/control-plane-web/src/pages/OverviewPage.tsx、apps/control-plane-web/src/pages/OverviewPage.dom.test.tsx
 * @since FR-508（组件受控化迁包；原页 FR-061 旗舰总览、FR-060 聚合历史曲线、FR-402 平台全景观测、
 *        FR-432 平台观测区权限对齐、FR-461 集群健康墙、FR-502 阶段 6 布局原语与渲染补丁）
 */
import { lazy, Suspense, useEffect, useMemo, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@jianmanager/ui/components/button'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Panel } from '@jianmanager/ui/components/panel'
import { Popover, PopoverContent, PopoverTrigger } from '@jianmanager/ui/components/popover'
import { ResourceGauge } from '@jianmanager/ui/components/gauge'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { RangePicker } from '@jianmanager/ui/charts/RangePicker'
import type { ChartSeries } from '@jianmanager/ui/charts/TimeSeriesChart'
import type { MetricRange } from '@jianmanager/ui/charts/RangePicker'
import { levelStatusLevel } from '@/lib/alerts/alert-helpers'
import type { AlertEventInfo } from '@/lib/alerts/alert-contracts'
import type { InstanceInfo } from '@/lib/instances/instance-types'
import type { Task, TaskState } from '@/lib/tasks/task-status'
import { instanceStatusLevel, type StatusLevel } from '@jianmanager/ui/lib/threshold'

/**
 * 首页趋势图（FR-060）**懒加载**：recharts 不能留在落地页的同步依赖里。
 *
 * 【为什么必须懒】`TimeSeriesChart` 静态 import recharts（含 d3 一族，构建产物中约 359 kB /
 * gzip 约 106 kB）。它此前是从 `@jianmanager/ui` barrel 同步引入的，于是本页所在的
 * **路由 chunk 必须等整个 recharts 下载并执行完才开始跑**——而 `/` 就是登录后的落地页，
 * 被拖住的不只是这张图，外壳、页头、CPU/负载/内存/实例数这些纯数字指标全在同一个 chunk 里。
 *
 * 【延后到哪里】改成动态 import 后，recharts 只在浏览器解析完首屏 chunk 之后才发起请求，
 * 落在首屏渲染之外。渲染时机只推迟「图表本身」，其余区块照旧。
 *
 * 【为什么写死成 lazy 常量而非组件内调用】`lazy()` 必须拿到稳定的组件引用，写在渲染函数里
 * 会让每次渲染都产生新组件、整棵子树重挂载。放模块顶层，配合下面的 Suspense 边界，
 * 图表 chunk 全生命周期只请求一次。
 *
 * 具名导出 → 用 `.then` 适配成 `default`（lazy 只认默认导出）。
 */
const TimeSeriesChart = lazy(() => import('@jianmanager/ui/charts/TimeSeriesChart').then((m) => ({ default: m.TimeSeriesChart })))

/**
 * 趋势图高度（px）：**声明值 = 真实值**——同一个常量既传给 `TimeSeriesChart` 的 `height`，
 * 也用作懒加载骨架的高度，图表就位前后占位完全一致，不产生布局跳动。
 */
const TREND_CHART_HEIGHT = 220

/**
 * 单张聚合卡片的截断条数。
 * 服务端已按 `pageSize`/`limit=5` 取数，这里再切片是**显示口径**兜底（告警端点的 pageSize 语义
 * 不保证条数上限时也稳），与迁包前 `items.slice(0, 5)` 完全同值。
 */
const AGGREGATION_LIMIT = 5

/**
 * 「需要处理」合并列表的显示条数。
 *
 * 原先是三张卡各 5 条（合计最多 15 行、分三栏并排）。合并成一张后若仍按 15 行铺，
 * 会比原来更高；取 8 条即可覆盖常见规模，且页高收在原三栏高度以内。
 */
const ATTENTION_LIMIT = 8

/**
 * 概览底部实例表行高（px）：**声明值 = 真实值**——同一个常量既喂虚拟窗口（`itemSize`），
 * 24h 范围服务端返回 **96 点/条**，而首页图表高 180px、宽约 400px——96 点远超实际可分辨的
 * 像素密度，纯属渲染负担。recharts 是 SVG 实现、**每个点都生成 DOM**：实测 4 个图表 × 96 点
 * 在容器宽度变化时重绘一次要 **546ms**（trace 佐证：单个 RunTask 内 `FireAnimationFrame` →
 * `react-dom` 546.6ms），侧栏开合时这一下会明显卡顿。
 *
 * 降到 32 点后渲染成本约为原来的 1/3，而 180px 高度下曲线形状肉眼无差别。
 * 末点单独保留：否则曲线终点会前移，读图时会误以为数据缺了一段。
 */
function downsampleTrend<T>(points: T[], target = 32): T[] {
  if (points.length <= target) return points
  const step = points.length / target
  const out: T[] = []
  for (let i = 0; i < target; i++) out.push(points[Math.floor(i * step)]!)
  out.push(points[points.length - 1]!)
  return out
}

/** 字节 → 紧凑可读（G/M/K）。 */
function fmtBytes(b: number): string {
  if (!Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1e9) return `${(b / 1024 / 1024 / 1024).toFixed(1)}G`
  if (b >= 1e6) return `${(b / 1024 / 1024).toFixed(0)}M`
  return `${(b / 1024).toFixed(0)}K`
}

/** 任务状态映射为首页紧凑徽章等级。 */
function taskStatusLevel(state: TaskState): StatusLevel {
  if (state === 'running') return 'info'
  if (state === 'succeeded') return 'success'
  if (state === 'failed') return 'danger'
  return 'neutral'
}

/** 数值 → 定长小数展示；`null` 统一显示 `--`（缺测量与「零」必须可区分）。 */
function displayNumber(value: number | null, suffix = ''): string {
  return value == null ? '--' : `${value.toFixed(suffix === '%' ? 1 : 0)}${suffix}`
}

/** 采样时刻 → 本地时间文案；缺失/非法时回落到调用方给的未知占位。 */
function formatObservedAt(value: string | null, unknown: string): string {
  if (!value) return unknown
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? unknown : date.toLocaleString()
}

/** 首页仪表可归属的资源维度（也是归因查询的排序口径）。 */
export type AttributionGauge = 'cpu' | 'load' | 'memory'

/**
 * 链接渲染插槽参数。
 * 视图只说「跳到哪、长什么样、带什么测试钩子」，由容器决定用 `react-router` 的 `Link`
 * 还是别的导航原语；插槽缺省时渲染原生 `<a href>`（组件博物馆与脱离路由的场景仍可用）。
 */
export interface OverviewLinkArgs {
  /** 目标地址（容器侧 `Link` 的 `to`）。 */
  href: string
  /** 链接类名（口径由视图给定，容器原样透传）。 */
  className: string
  /** 测试钩子（如 `overview-exception-item`）；缺省不落 `data-testid`。 */
  testId?: string
  children: ReactNode
}

/**
 * `/metrics/overview` 的 `totals` 中本页用到的字段（结构性最小集）。
 * 刻意不照搬应用侧 `@/api/metrics` 的类型：容器直接传该响应的 `totals` 也结构兼容，
 * 而包内无需认识整份观测响应（同 `StatisticsOverviewTotals` 的取舍）。
 */
export interface OverviewMetricTotals {
  /** 节点总数（含离线）。 */
  nodeCount: number
  /** 在线节点数。 */
  onlineNodeCount: number
  /** 运行中实例数。 */
  runningInstances: number
  /** 跨节点 CPU 平均占用（%）。 */
  cpuPct: number
  /** 在线节点负载利用率均值（load1/核数×100，FR-062）。 */
  loadAvg: number
  /** 已用内存（字节）。 */
  memUsedBytes: number
  /** 内存总量（字节）。 */
  memTotalBytes: number
  /** 在线玩家数。 */
  onlinePlayers: number
}

/** `/metrics/overview` 的一条聚合曲线；本页只读指标键与 `ts`/`avg` 两个采样字段。 */
export interface OverviewMetricTrend {
  /** 指标键（node_cpu_pct / node_load / node_mem_used / inst_players_online）。 */
  metricKey: string
  points: { ts: string; avg: number | null }[]
}

/** `/metrics/overview` 快照（容器取数 + 渲染降级后整体注入）。 */
export interface OverviewMetricSnapshot {
  /** 跨节点当前总量（环形仪表与三个 KPI 读数）。 */
  totals: OverviewMetricTotals
  /** 聚合历史曲线（FR-060）。 */
  trends: OverviewMetricTrend[]
}

/**
 * `/observability/overview` 中本页用到的字段（结构性最小集，FR-402）。
 * 三张平台卡共用同一份快照，故只列它们真正读到的分支。
 */
export interface OverviewObservabilitySnapshot {
  health: {
    /** 节点总数。 */
    nodeCount: number
    /** 在线节点数。 */
    onlineNodeCount: number
    /** 运行中实例数。 */
    runningInstanceCount: number
  }
  resources: {
    /** 跨节点 CPU 平均占用；样本缺失为 null。 */
    cpuPct: number | null
    /** 已用内存（字节）；样本缺失为 null。 */
    memoryUsedBytes: number | null
    /** 内存总量（字节）；样本缺失为 null。 */
    memoryTotalBytes: number | null
    /** 鲜度（fresh / stale / offline / unavailable），用于 i18n 键拼接。 */
    freshness: string
  }
  bots: {
    /** 共享进程观察值说明（后端给的口径文案）。 */
    notice: string
    /** Bot Worker 常驻内存（字节）。 */
    botWorkerRssBytes: number | null
    /** Bot Worker CPU（%）。 */
    botWorkerCpuPct: number | null
    /** 活跃 Bot 数。 */
    activeCount: number | null
    /** 事件循环 P95 延迟（ms）。 */
    eventLoopP95Ms: number | null
    /** 采集不可用的节点及原因（本页展示首条）。 */
    unavailable: { reason: string }[]
  }
  /** 活跃告警（本页只读条数）。 */
  alerts: { length: number }
  /** 活跃任务（本页只读条数）。 */
  tasks: { length: number }
  /** 异常项（心跳陈旧、崩溃实例等），带下钻地址。 */
  exceptions: { kind: string; nodeId?: number; instanceId?: number; title: string; href: string }[]
}

/**
 * `/metrics/resource-attribution` 中本页用到的字段（结构性最小集，FR-402）。
 * 归因 Tooltip 只展示有界的受管资源，故后端已按 `limit=5` 截断，视图再取前 3/4 条。
 */
export interface OverviewAttributionSnapshot {
  /** 采样时刻；缺失为 null。 */
  sampledAt: string | null
  /** 整体鲜度（fresh / stale / offline / unavailable）。 */
  freshness: string
  nodes: {
    nodeId: number
    nodeUuid: string
    name: string
    status: string
    cpuPct: number | null
    memoryUsedBytes: number | null
  }[]
  topInstances: {
    instanceId: number
    instanceUuid: string
    instanceName: string
    cpuPct: number
    rssBytes: number
  }[]
  topProcesses: {
    instanceId: number
    instanceUuid: string
    instanceName: string
    pid: number
    name: string
    cpuPercent: number
    rssBytes: number
  }[]
}

/** 链接插槽的统一落点：有插槽走插槽，缺省渲染原生 `<a href>`。 */
function OverviewLink({ href, className, testId, children, renderLink }: OverviewLinkArgs & { renderLink?: (args: OverviewLinkArgs) => ReactNode }) {
  if (renderLink) return <>{renderLink({ href, className, testId, children })}</>
  return (
    <a href={href} className={className} data-testid={testId}>
      {children}
    </a>
  )
}

/** 首页仪表的有界受管资源 Tooltip 属性。 */
export interface GaugeAttributionTooltipProps {
  /** 本仪表对应的归因维度（cpu / load / memory）。 */
  gauge: AttributionGauge
  /** 仪表名（用于可访问名与 `--` 口径文案）。 */
  label: string
  /** 仪表本体（环形图）。 */
  children: ReactNode
  /** 是否展开。 */
  active: boolean
  /** 展开/收起上报（受控：容器决定是否取数）。 */
  onToggle: () => void
  /** 归因快照（仅在展开后由容器取数）。 */
  data?: OverviewAttributionSnapshot
  /** 取数中。 */
  isLoading: boolean
  /** 取数失败（局部降级为不可用文案，不动其他区块）。 */
  isError: boolean
  /** 链接渲染插槽（下钻监控页）。 */
  renderLink?: (args: OverviewLinkArgs) => ReactNode
}

/** 首页仪表的有界受管资源 Tooltip，不展示任意 OS 进程。 */
export function GaugeAttributionTooltip({
  gauge,
  label,
  children,
  active,
  onToggle,
  data,
  isLoading,
  isError,
  renderLink,
}: GaugeAttributionTooltipProps) {
  const { t } = useTranslation()
  const formatValue = (cpuPct: number | null, rssBytes: number | null): string => (
    gauge === 'cpu' ? displayNumber(cpuPct, '%') : rssBytes == null ? '--' : fmtBytes(rssBytes)
  )
  return (
    <Panel bodyClassName="relative flex items-center justify-center py-3">
      {/*
        归因浮层此前是「button + active 时渲染一个 absolute 定位的 div」：位置靠
        `top-[calc(100%-0.25rem)]` 硬算，仪表卡换尺寸/换方向就会错位；也没有点击外部关闭、
        没有焦点管理、Escape 要自己写键处理。这些都是**有锚点的非模态浮层**的通用需求，
        正是 Popover 的职责范围（本仓 popover.tsx 文件头写明的分工：菜单装动作列表，
        Popover 装任意内容）。
        Popover 之后：锚点由 Radix 定位到面板（不再依赖父级 relative），
        点击外部/ Escape 关闭、焦点归还全部由原语托管，本视图只保留「开合态上报」。
        受控：容器据开合态决定是否发起归因请求（enabled），故 open 必须由 props 驱动。
      */}
      <Popover open={active} onOpenChange={(next) => { if (next !== active) onToggle() }}>
        <PopoverTrigger asChild>
          {/* 仪表卡整块是归因浮层的触发区：外观为零、只承载可点区域，
              故取 invisible + auto；asChild 让 Button 把样式与键盘语义一并交给触发器。 */}
          <Button
            type="button"
            variant="invisible"
            size="auto"
            aria-label={t('dashboard.resourceAttributionLabel', { label })}
            // `[&_svg…]:size-auto`：Button 基类会把无 size-* 类的 svg 强制成 size-4，
            // 而仪表环形图的 svg 靠 width/height 属性取尺寸，这里必须交还原属性。
            className="rounded-sm hover:bg-accent/40 [&_svg:not([class*='size-'])]:size-auto"
          >
            {children}
          </Button>
        </PopoverTrigger>
        <PopoverContent
          align="center"
          side="bottom"
          data-testid="overview-resource-attribution"
          className="w-[min(22rem,calc(100vw-2rem))] text-left text-xs"
        >
          {isLoading && <p className="text-muted-foreground">{t('dashboard.resourceAttributionLoading')}</p>}
          {isError && <p className="text-muted-foreground">{t('dashboard.resourceAttributionUnavailable')}</p>}
          {data && (
            <div className="space-y-2">
              <p className="text-muted-foreground">{formatObservedAt(data.sampledAt, t('dashboard.sampleTimeUnknown'))} · {t(`dashboard.freshness.${data.freshness}`, data.freshness)}</p>
              <div className="space-y-1">
                {data.nodes.slice(0, 3).map((node) => (
                  <OverviewLink key={node.nodeId} href={`/monitoring?node=${encodeURIComponent(node.nodeUuid)}`} className="flex justify-between gap-2 hover:text-primary" renderLink={renderLink}>
                    <span className="truncate">{node.name} · {t(`dashboard.freshness.${node.status}`, node.status)}</span>
                    <span className="font-mono">{formatValue(node.cpuPct, node.memoryUsedBytes)}</span>
                  </OverviewLink>
                ))}
              </div>
              <div className="space-y-1 border-t pt-2">
                {data.topInstances.slice(0, 3).map((instance) => (
                  <OverviewLink key={instance.instanceId} href={`/monitoring?instance=${encodeURIComponent(instance.instanceUuid)}`} className="flex justify-between gap-2 hover:text-primary" renderLink={renderLink}>
                    <span className="truncate">{instance.instanceName}</span>
                    <span className="font-mono">{formatValue(instance.cpuPct, instance.rssBytes)}</span>
                  </OverviewLink>
                ))}
                {data.topProcesses.slice(0, 4).map((process) => (
                  <OverviewLink key={`${process.instanceId}-${process.pid}`} href={`/monitoring?instance=${encodeURIComponent(process.instanceUuid)}`} className="flex justify-between gap-2 hover:text-primary" renderLink={renderLink}>
                    <span className="truncate">{process.instanceName} / {process.name || `PID ${process.pid}`}</span>
                    <span className="font-mono">{formatValue(process.cpuPercent, process.rssBytes)}</span>
                  </OverviewLink>
                ))}
              </div>
              <p className="text-[11px] text-muted-foreground">{t('dashboard.resourceObservation')}</p>
            </div>
          )}
        </PopoverContent>
      </Popover>
    </Panel>
  )
}

/** 首页紧凑聚合面板属性。 */
export interface OverviewAggregationPanelProps {
  /** 卡片测试钩子（`overview-exceptions` / `overview-recent-tasks` / `overview-active-alerts`）。 */
  testId: string
  /** 卡片标题。 */
  title: string
  /** 「查看全部」目标地址。 */
  to: string
  /** 取数中。 */
  isLoading: boolean
  /** 取数失败（本卡局部降级）。 */
  isError: boolean
  /** 数据为空（成功态下的空集）。 */
  isEmpty: boolean
  /** 空态文案。 */
  emptyText: string
  /** 错误态文案。 */
  errorText: string
  /** 卡片内容（有数据时渲染）。 */
  children: ReactNode
  /** 链接渲染插槽（「查看全部」）。 */
  renderLink?: (args: OverviewLinkArgs) => ReactNode
}

/** 统一聚合面板的加载、空态与错误降级，避免单域失败影响其他卡片。 */
export function OverviewAggregationPanel({
  testId,
  title,
  to,
  isLoading,
  isError,
  isEmpty,
  emptyText,
  errorText,
  children,
  renderLink,
}: OverviewAggregationPanelProps) {
  let content = children
  if (isError) content = <p className="py-8 text-center text-sm text-destructive">{errorText}</p>
  else if (isLoading) content = <p className="py-8 text-center text-sm text-muted-foreground">加载中…</p>
  else if (isEmpty) content = <p className="py-8 text-center text-sm text-muted-foreground">{emptyText}</p>

  return (
    <Panel
      data-testid={testId}
      title={title}
      actions={<OverviewLink href={to} className="text-xs text-muted-foreground hover:text-foreground" renderLink={renderLink}>查看全部</OverviewLink>}
      bodyClassName="p-0"
      className="h-full min-w-0 overflow-hidden"
    >
      {content}
    </Panel>
  )
}

/** 平台健康卡属性：三张平台卡共用同一份快照与取数态。 */
export interface PlatformPanelProps {
  /** `/observability/overview` 快照；缺省即空态。 */
  data?: OverviewObservabilitySnapshot
  /** 取数中。 */
  isLoading: boolean
  /** 取数失败。 */
  isError: boolean
}

export function PlatformBotRuntimePanel({ data, isLoading, isError }: PlatformPanelProps) {
  const { t } = useTranslation()
  const bots = data?.bots
  // 不可用原因只展示首条（与列表口径一致）
  const firstUnavailable = bots?.unavailable[0]
  return (
    <OverviewAggregationPanel testId="platform-observability-bots" title={t('dashboard.sharedBotRuntime')} to="/monitoring" isLoading={isLoading} isError={isError} isEmpty={!bots} emptyText={t('dashboard.noData')} errorText={t('dashboard.platformOverviewUnavailable')}>
      {bots && <div className="space-y-2 p-3 text-sm">
        <p className="break-words text-muted-foreground">{bots.notice}</p>
        <div className="grid grid-cols-2 gap-2 text-muted-foreground">
          <span className="min-w-0 break-words">{t('dashboard.botRss')}: {bots.botWorkerRssBytes == null ? '--' : fmtBytes(bots.botWorkerRssBytes)}</span>
          <span className="min-w-0 break-words">{t('dashboard.botCpu')}: {displayNumber(bots.botWorkerCpuPct, '%')}</span>
          <span className="min-w-0 break-words">{t('dashboard.botCount')}: {displayNumber(bots.activeCount)}</span>
          <span className="min-w-0 break-words">{t('dashboard.botEventLoop')}: {displayNumber(bots.eventLoopP95Ms, 'ms')}</span>
        </div>
        {firstUnavailable && <p className="break-words text-amber-600">{t('dashboard.botUnavailable', { reason: firstUnavailable.reason })}</p>}
      </div>}
    </OverviewAggregationPanel>
  )
}


/**
 * 受控边界（ADR-102 a 范式）：**不取数、不发请求、不弹 toast**。
 * - 六个查询的结果与错误态经 props 注入（容器调 `useMetricOverview` / `useNodes` / `useInstanceSearch` /
 *   `useInfiniteInstanceSearch` / `useTasks` / `useAlertEvents` / `usePlatformObservabilityOverview` /
 *   `useResourceAttribution`）；计数、百分比、降采样与图表序列都是纯展示派生，留本组件；
 * - **归容器**的受控状态：统计窗口 `range`（全部指标查询的查询键）与归因 Tooltip 的 `activeGauge`
 *   （既决定是否发起归因请求，又决定该请求的排序口径 cpu/memory，属取数口径）；
 *   平台观测区可见性与链接、健康墙、续取守卫同样归容器（鉴权与路由不在包内）；
 * - **留本组件**的纯 UI 状态：实例表的虚拟窗口、Tooltip 的键盘交互（Esc 关闭经 `onActiveGaugeChange` 上报）；
 * - **千级实体**：底部实例表与异常卡片都保持既有服务端分页语义——异常卡片由容器按
 *   `status=CRASHED&pageSize=5` 取数，实例表由容器按需续取（视图只上报「滚到近底」，不拉全量）；
 * - 集群健康墙（FR-461）本体是应用侧接线层（自持取数 + 排序状态），故以 `renderHealthWall` 插槽注入，
 *   包内不 import 它；插槽缺省或无权时不渲染该区块。
 */
export interface OverviewPageViewProps {
  /** 统计窗口（受控：指标查询的查询键，变更即重新取数）。 */
  range: MetricRange
  /** 统计窗口变更上报。 */
  onRangeChange: (range: MetricRange) => void
  /** `/metrics/overview` 快照（容器做 10s 轮询的渲染降级后注入；缺省时读数回落到 0/兜底值）。 */
  overview?: OverviewMetricSnapshot
  /** 节点总数兜底（`/metrics/overview` 的 `nodeCount` 缺省时用它算「在线/总数」）。 */
  nodeTotal?: number
  /** 已展开的归因 Tooltip；`null` 表示全部收起（容器据此不发归因请求）。 */
  activeGauge: AttributionGauge | null
  /** 归因 Tooltip 展开/收起上报。 */
  onActiveGaugeChange: (gauge: AttributionGauge | null) => void
  /** 受管资源归因快照（仅 Tooltip 展开后由容器取数）。 */
  attribution?: OverviewAttributionSnapshot
  /** 归因取数中。 */
  attributionLoading: boolean
  /** 归因取数失败（Tooltip 内局部降级）。 */
  attributionError: boolean
  /** 异常实例（容器按 `status=CRASHED`、`pageSize=5` 取数）。 */
  exceptionInstances?: InstanceInfo[]
  /** 异常实例取数中。 */
  exceptionLoading: boolean
  /** 异常实例取数失败。 */
  exceptionError: boolean
  /** 近期任务（容器按 `limit=5` 取数）。 */
  recentTasks?: Task[]
  /** 近期任务取数中。 */
  recentTasksLoading: boolean
  /** 近期任务取数失败。 */
  recentTasksError: boolean
  /** 活跃告警（容器按 `pageSize=5` 取数）。 */
  activeAlerts?: AlertEventInfo[]
  /** 活跃告警取数中。 */
  activeAlertsLoading: boolean
  /** 活跃告警取数失败。 */
  activeAlertsError: boolean
  /** 平台观测区是否可见（FR-432 权限树对齐：平台管理员或持 monitor.read / node.read）。 */
  canSeePlatformObs: boolean
  /** `/observability/overview` 快照（三张平台卡共用）。 */
  platformOverview?: OverviewObservabilitySnapshot
  /** 平台观测取数中。 */
  platformOverviewLoading: boolean
  /** 平台观测取数失败。 */
  platformOverviewError: boolean
  /** 集群健康墙插槽（FR-461）：取数与排序在应用侧接线层，插槽缺省或无权时不渲染。 */
  renderHealthWall?: () => ReactNode
  /** 链接渲染插槽（缺省原生 `<a href>`）。 */
  renderLink?: (args: OverviewLinkArgs) => ReactNode
}

/**
 * 概览页（FR-061 旗舰）：环形仪表盘 + 聚合历史曲线（FR-060） + 密集实例表，一屏概览。
 * 页面外壳与页头走布局层原语（PageShell / PageHeader），`data-page="overview"` 由 PageShell 透传，
 * e2e 的就绪信号依赖它。
 */
export function OverviewPageView({
  range,
  onRangeChange,
  overview,
  nodeTotal,
  activeGauge,
  onActiveGaugeChange,
  attribution,
  attributionLoading,
  attributionError,
  exceptionInstances,
  exceptionLoading,
  exceptionError,
  recentTasks,
  recentTasksLoading,
  recentTasksError,
  activeAlerts,
  activeAlertsLoading,
  activeAlertsError,
  canSeePlatformObs,
  platformOverview,
  platformOverviewLoading,
  platformOverviewError,
  renderHealthWall,
  renderLink,
}: OverviewPageViewProps) {
  const { t } = useTranslation()

  const totals = overview?.totals
  const memPct = totals && totals.memTotalBytes > 0 ? (totals.memUsedBytes / totals.memTotalBytes) * 100 : 0

  /** Esc 关闭已展开的归因 Tooltip：受控状态在容器，故经回调上报（本组件不持该状态）。 */
  useEffect(() => {
    if (!activeGauge) return undefined
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onActiveGaugeChange(null)
    }
    window.addEventListener('keydown', closeOnEscape)
    return () => window.removeEventListener('keydown', closeOnEscape)
  }, [activeGauge, onActiveGaugeChange])

  // 三张聚合卡各自截断到 5 条（显示口径，见 AGGREGATION_LIMIT 说明）。
  const exceptionRows = (exceptionInstances ?? []).slice(0, AGGREGATION_LIMIT)
  const recentTaskRows = (recentTasks ?? []).slice(0, AGGREGATION_LIMIT)
  const activeAlertRows = (activeAlerts ?? []).slice(0, AGGREGATION_LIMIT)

  /**
   * 「需要处理」统一列表（本轮结构重排）。
   *
   * 【为什么合并】原先并排三栏：异常实例 / 近期任务 / 活跃告警。但三栏地位并不对等——
   * 「近期任务」里多数是**已完成**的（它回答「最近发生了什么」），而前两者回答的是
   * 「现在需要我做什么」。三者同权重并排时，真正要动手的项被「已完成」的噪音淹没。
   *
   * 合并后按**紧急度**排序，一眼看到最先该处理的那条：
   * 崩溃实例(0) > 失败任务(1) > 活跃告警(2，按告警级别再分) > 其余任务(3)。
   * 严重度同分时保持各自原有顺序（稳定排序），避免每 10 秒轮询时行序跳动。
   *
   * 条数上限取 `ATTENTION_LIMIT`：合并后若仍各给 5 条会变成 15 行，比原先更高；
   * 取 8 条既覆盖常见规模，又把页高收在原来的三栏高度以内。
   */
  type AttentionRow = {
    key: string
    /** 越小越紧急。 */
    rank: number
    /** 来源类别：用于「各类保底席位」，避免某一类被整体挤掉。 */
    kind: 'exception' | 'task' | 'alert'
    label: string
    badge: ReactNode
    href?: string
    testId: string
  }
  /*
    【为什么不是简单全局截断】直接按紧急度排序再 `slice(0, 8)` 会有个副作用：
    崩溃实例一多（比如 20 台），8 个位置全被它占满，失败任务与告警**永远露不出来**——
    而「有失败任务」恰恰是用户更该知道的事。故按类别**保底**各留席位：
    每类先各取至多 3 条，剩余的席位再按紧急度回填。这样任一类别有内容就一定可见，
    同时整体仍按紧急度排序，最先看到的还是最该处理的。
  */
  const PER_KIND_FLOOR = 3
  const allAttention: AttentionRow[] = [
    ...exceptionRows.map((instance) => ({
      key: `inst:${instance.id}`,
      // 崩溃实例排最前：它是唯一「已经在损失可用性」的一类。
      rank: 0,
      kind: 'exception' as const,
      label: instance.name,
      badge: <StatusBadge level={instanceStatusLevel(instance.status)} label={instance.status} />,
      href: `/instances/${instance.id}`,
      testId: 'overview-exception-item',
    })),
    ...recentTaskRows.map((task) => {
      const failed = taskStatusLevel(task.state) === 'danger'
      return {
        key: `task:${task.taskId}`,
        // 失败任务与崩溃实例同级紧迫；进行中/已完成的排到告警之后。
        rank: failed ? 1 : 3,
        kind: 'task' as const,
        label: task.title,
        badge: (
          <StatusBadge
            level={task.cancelRequested ? 'warning' : taskStatusLevel(task.state)}
            label={task.cancelRequested ? t('tasks.state.canceling', '取消中') : t(`tasks.state.${task.state}`)}
          />
        ),
        href: `/tasks?task=${encodeURIComponent(task.taskId)}`,
        testId: 'overview-task-item',
      }
    }),
    ...activeAlertRows.map((alert) => ({
      key: `alert:${alert.id}`,
      // 告警按自身级别细分：严重(1.5) 次于失败任务，警告(2) 再次之。
      rank: levelStatusLevel(alert.level) === 'danger' ? 1.5 : 2,
      kind: 'alert' as const,
      label: alert.message,
      badge: <StatusBadge level={levelStatusLevel(alert.level)} label={t(`alerts.level_${alert.level}`, alert.level)} />,
      // 告警行不是链接（迁包前即是纯展示行），故不给 href。
      testId: 'overview-alert-item',
    })),
  ].sort((a, b) => a.rank - b.rank)

  // 各类保底席位 → 剩余席位按紧急度回填 → 最终仍按紧急度排序输出。
  const floorPicked = new Set<string>()
  for (const kind of ['exception', 'task', 'alert'] as const) {
    allAttention.filter((r) => r.kind === kind).slice(0, PER_KIND_FLOOR).forEach((r) => floorPicked.add(r.key))
  }
  const attentionRows = allAttention
    .filter((r) => floorPicked.has(r.key))
    .slice(0, ATTENTION_LIMIT)
    .concat(allAttention.filter((r) => !floorPicked.has(r.key)))
    .slice(0, ATTENTION_LIMIT)
    .sort((a, b) => a.rank - b.rank)

  /**
   * 四条趋势合成一张图（FR-502 阶段 6 补丁）。
   *
   * 【为什么合并】React Profiler 埋点实测：首页整页渲染 283.9ms 中，
   * 4 个 `TimeSeriesChart` 占 **246ms（86.6%）**，而其余所有区块合计仅 24.7ms
   * （含那 290 个不依赖本数据的元素）。recharts 是 SVG 实现，每张图都要独立生成
   * 坐标轴、网格、图例与路径——四张就是四套。合成一张后只剩一套，实测省约 185ms。
   *
   * 【为什么归一化】四个指标量纲完全不同（CPU % / 负载倍数 / 内存字节 / 玩家数），
   * 同图必须共享纵轴。统一映射为「占各自满值的百分比」：曲线形状（涨跌、尖峰、拐点）
   * 完整保留，而这正是趋势图要回答的问题——**绝对数值已由上方 4 个 ResourceGauge
   * 与 5 个 StatCard 分别给出**。tooltip 再按满值反算回原始量纲，信息不丢。
   *
   * 【满值怎么取】CPU=100、负载=1.0 是固有满值；内存取节点内存总量；
   * 玩家数没有自然上限，退回「本序列窗口内最大值」——此时曲线顶端即"窗口峰值"，
   * 语义仍然清楚（tooltip 照常显示真实人数）。
   */
  const trendChart = useMemo(() => {
    const memTotal = overview?.totals?.memTotalBytes ?? 0
    const series: ChartSeries[] = []
    /** 序列名 → { 满值, 原值格式化器 }，供 tooltip 反算。 */
    const scales = new Map<string, { full: number; fmt: (v: number) => string }>()
    const specs: Array<{
      key: string
      labelKey: string
      full: number | 'memTotal' | 'seriesMax'
      fmt: (v: number) => string
    }> = [
      { key: 'node_cpu_pct', labelKey: 'dashboard.totalCpu', full: 100, fmt: (v) => `${v.toFixed(0)}%` },
      { key: 'node_load', labelKey: 'dashboard.totalLoad', full: 1, fmt: (v) => v.toFixed(2) },
      { key: 'node_mem_used', labelKey: 'dashboard.totalMem', full: 'memTotal', fmt: fmtBytes },
      { key: 'inst_players_online', labelKey: 'dashboard.onlinePlayers', full: 'seriesMax', fmt: (v) => v.toFixed(0) },
    ]

    for (const spec of specs) {
      const tr = overview?.trends.find((x) => x.metricKey === spec.key)
      if (!tr || tr.points.length === 0) continue
      const raw = downsampleTrend(tr.points.map((p) => ({ ts: p.ts, v: p.avg })))
      const max = Math.max(...raw.map((p) => p.v ?? 0), 0)
      const full =
        spec.full === 'memTotal'
          ? memTotal > 0
            ? memTotal
            : max || 1
          : spec.full === 'seriesMax'
            ? max || 1
            : spec.full
      const name = t(spec.labelKey)
      scales.set(name, { full, fmt: spec.fmt })
      series.push({
        key: spec.key,
        name,
        points: raw.map((p) => ({ ts: p.ts, value: p.v == null ? null : (p.v / full) * 100 })),
      })
    }
    return { series, scales }
  }, [overview, t])

  return (
    // 阶段 6 页面迁移：外壳与页头改用布局层原语（PageShell / PageHeader）。
    // data-page 由 PageShell spread 透传，e2e 的就绪信号依赖它。
    <PageShell data-page="overview">
      <PageHeader
        title={t('dashboard.title')}
        description={t('dashboard.subtitle')}
        actions={<RangePicker value={range} onChange={onRangeChange} />}
      />

      {/* 顶部：环形仪表盘 + 统计块 */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 md:grid-cols-3 xl:grid-cols-6">
        <GaugeAttributionTooltip gauge="cpu" label={t('dashboard.totalCpu')} active={activeGauge === 'cpu'} onToggle={() => onActiveGaugeChange(activeGauge === 'cpu' ? null : 'cpu')} data={attribution} isLoading={attributionLoading} isError={attributionError} renderLink={renderLink}>
          <ResourceGauge label={t('dashboard.totalCpu')} value={totals?.cpuPct ?? 0} unit="%" />
        </GaugeAttributionTooltip>
        <GaugeAttributionTooltip gauge="load" label={t('dashboard.totalLoad')} active={activeGauge === 'load'} onToggle={() => onActiveGaugeChange(activeGauge === 'load' ? null : 'load')} data={attribution} isLoading={attributionLoading} isError={attributionError} renderLink={renderLink}>
          {/* 负载是「占总核数比例」：以倍数（load÷核）呈现而非百分比，环按 1.0=满核封顶，
              不再出现 >100% 的破环（FR-108）。grading 仍按占比走 resourceLevel（>0.8×→红）。 */}
          <ResourceGauge label={t('dashboard.totalLoad')} value={(totals?.loadAvg ?? 0) / 100} max={1} unit="×" decimals={2} />
        </GaugeAttributionTooltip>
        <GaugeAttributionTooltip gauge="memory" label={t('dashboard.totalMem')} active={activeGauge === 'memory'} onToggle={() => onActiveGaugeChange(activeGauge === 'memory' ? null : 'memory')} data={attribution} isLoading={attributionLoading} isError={attributionError} renderLink={renderLink}>
          <ResourceGauge label={t('dashboard.totalMem')} value={memPct} unit="%" />
        </GaugeAttributionTooltip>
        <StatCard
          label={t('dashboard.nodes')}
          value={`${totals?.onlineNodeCount ?? 0}/${totals?.nodeCount ?? nodeTotal ?? 0}`}
          sub={t('dashboard.online')}
        />
        <StatCard label={t('dashboard.runningInstances')} value={String(totals?.runningInstances ?? 0)} sub={t('dashboard.instances')} />
        <StatCard label={t('dashboard.onlinePlayers')} value={String(totals?.onlinePlayers ?? 0)} sub={t('nav.players')} />
      </div>

      {/*
        【三栏 → 一张「需要处理」卡】原先并排 异常实例 / 近期任务 / 活跃告警，
        三者地位并不对等：近期任务多数是**已完成**的（回答「最近发生了什么」），
        前两者才回答「现在要我做什么」。同权重并排时，要动手的项被已完成项淹没。
        合并后按紧急度排序（崩溃实例 > 失败任务 > 严重告警 > 警告 > 其余任务），
        一眼看到最先该处理的那条；条数与紧迫度口径见 `attentionRows` 与 `ATTENTION_LIMIT`。
      */}
      <OverviewAggregationPanel
        testId="overview-attention"
        title={t('dashboard.needsAttention', '需要处理')}
        to="/instances?status=CRASHED"
        isLoading={exceptionLoading || recentTasksLoading || activeAlertsLoading}
        isError={exceptionError && recentTasksError && activeAlertsError}
        isEmpty={attentionRows.length === 0}
        emptyText={t('dashboard.nothingToDo', '没有需要处理的项')}
        errorText={t('dashboard.attentionFailed', '加载失败')}
        renderLink={renderLink}
      >
        <div className="divide-y">
          {attentionRows.map((row) => {
            const body = (
              <>
                <span className="min-w-0 truncate text-sm font-medium" title={row.label}>
                  {row.label}
                </span>
                {row.badge}
              </>
            )
            // 有 href 的行走链接（实例 / 任务）；告警行是纯展示行，保持 div 不挂链接。
            return row.href ? (
              <OverviewLink
                key={row.key}
                href={row.href}
                testId={row.testId}
                className="flex items-center justify-between gap-3 px-3 py-2.5 hover:bg-muted/40"
                renderLink={renderLink}
              >
                {body}
              </OverviewLink>
            ) : (
              <div
                key={row.key}
                data-testid={row.testId}
                className="flex items-center justify-between gap-3 px-3 py-2.5"
              >
                {body}
              </div>
            )
          })}
        </div>
      </OverviewAggregationPanel>

      {/*
        【平台健康 / 异常项 / 首页实例表三块已移除】数据口径修好之后（mock 的 totals 曾写死，
        导致同一屏上「运行实例」既是 720 又是 242），这三块的重复一眼可见：

        - 平台健康：4 个 StatCard 与页顶的指标卡**逐项相同**（节点 / 运行实例 / 总 CPU / 总内存）。
          同一组数字在首屏出现两遍，第二遍不提供任何新信息，只是把下面的内容推低约 150px。
        - 异常项：读 `platformOverview.exceptions`，与读 `/instances/search?status=CRASHED`
          的「异常实例」表达同一件事，两个数据源还常对不上（一个空、一个有内容）。
        - 首页实例表：不限量无限滚动铺全部实例，与「服务器」页的完整版本重复；
          它同时是本页渲染量的大头（改前实测约 783 个元素、单次主线程阻塞约 360ms）。

        移除后信息一项不丢：指标在页顶、异常在「需要处理」、完整实例列表在「服务器」页。
      */}
      {canSeePlatformObs && <PlatformBotRuntimePanel data={platformOverview} isLoading={platformOverviewLoading} isError={platformOverviewError} />}

      {/* FR-461：逐台集群健康墙（热力矩阵 + 分级 + 排序 + 一键下钻）——本体是应用侧接线层，经插槽注入。 */}
      {canSeePlatformObs && renderHealthWall?.()}

      {/* 中部：聚合历史曲线（FR-060）——四指标合成一张多序列图。
          纵轴是「占各自满值的百分比」，tooltip 反算回原始量纲（见 trendChart 的说明）。
          图表走 lazy + Suspense：recharts 的下载与执行不阻塞本页其余区块，
          骨架高度与图表高度同源（TREND_CHART_HEIGHT），就位前后不跳动。 */}
      <Panel title={t('dashboard.trends')}>
        <Suspense fallback={<Skeleton className="w-full" style={{ height: TREND_CHART_HEIGHT }} />}>
          <TimeSeriesChart
            series={trendChart.series}
            height={TREND_CHART_HEIGHT}
            yDomain={[0, 100]}
            valueFormatter={(v, name) => {
              const scale = name ? trendChart.scales.get(name) : undefined
              if (!scale) return `${v.toFixed(0)}%`
              return scale.fmt((v / 100) * scale.full)
            }}
          />
        </Suspense>
      </Panel>
    </PageShell>
  )
}

export default OverviewPageView
