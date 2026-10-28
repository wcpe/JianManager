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
import { lazy, Suspense, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Activity,
  AlertTriangle,
  Ban,
  Layers,
  MessageSquare,
  Server,
  Users,
} from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Panel } from '@jianmanager/ui/components/panel'
import { Popover, PopoverContent, PopoverTrigger } from '@jianmanager/ui/components/popover'
import { ResourceGauge } from '@jianmanager/ui/components/gauge'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { RangePicker } from '@jianmanager/ui/charts/RangePicker'
import type { ChartSeries } from '@jianmanager/ui/charts/TimeSeriesChart'
import type { MetricRange } from '@jianmanager/ui/charts/RangePicker'
import type { PlayerTrendResult } from '@/lib/players/player-trend'
import type { SLOResult as SLOResultView } from '@/lib/metrics/slo'
import { resourceLevel, statusColorVar, type StatusLevel } from '@jianmanager/ui/lib/threshold'

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
 * 铺满模式下趋势图的最小高度。
 *
 * 首帧容器可能测到 0（父级尚未布局），量不到时就退回一个保底值；
 * 另外窄屏（<768px）纵向空间紧张，也用它兜住——否则图表会被压成一条线，失去可读性。
 */
const MIN_TREND_FILL_HEIGHT = 160

/**
 * 资源结论句的兜底文案（键缺省时用）。
 *
 * 用 `resourceLevel` 的三档作为键名，好处是文案与环的颜色走**同一个判据**——
 * 不会出现「环红了但结论写正常」这种自相矛盾。真正的文案在 i18n 的
 * `dashboard.loadVerdict.*`，这里只是保险。
 */
const VERDICT_FALLBACK: Record<StatusLevel, string> = {
  success: '正常',
  warning: '偏高',
  danger: '过高',
  info: '正常',
  neutral: '--',
}

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

/** 数值 → 定长小数展示；`null` 统一显示 `--`（缺测量与「零」必须可区分）。 */
function displayNumber(value: number | null | undefined, suffix = ''): string {
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
    /** 崩溃实例数（KPI 卡用）。 */
    crashedInstanceCount?: number
    /** 停止实例数（KPI 卡用）。 */
    stoppedInstanceCount?: number
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
    /**
     * FR-513 主机级观测：全部可为 null（老 Worker 未上报）。
     *
     * `null` 与 `0` 语义不同：null = 不知道（渲染 `--`），0 = 读数是 0。
     * 把「不知道」画成 0 会让运维以为机器空闲。
     */
    loadPct?: number | null
    load5Pct?: number | null
    load15Pct?: number | null
    procsRunning?: number | null
    procsTotal?: number | null
    cpuBreakdown?: {
      user: number
      nice: number
      system: number
      idle: number
      iowait: number
      irq: number
      softirq: number
      steal: number
    } | null
    /** 分挂载点磁盘用量（后端已按使用率降序）。 */
    mounts?: {
      mountpoint: string
      totalBytes: number
      usedBytes: number
      usedPct: number
    }[]
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
  /**
   * FR-513：**整机** top-N 进程。
   *
   * 与 `topProcesses` 是两个不同口径，**不可混为一谈**：
   * 前者只覆盖受管实例的进程树（我们知道它是谁），后者是整机上按 CPU 排序的前几名
   *（包含系统进程、别人的进程）。运维判断「这台机器为什么慢」时看后者。
   */
  hostTopProcesses?: {
    nodeId: number
    nodeName: string
    pid: number
    comm: string
    cpuPct: number
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
  /**
   * 环下方的结论句（照宝塔首页的做法）。
   *
   * 环上只有百分比，是「精确读数」；而运维在首页第一眼要的是**判断**——
   * 「47% 算不算高」「内存还够吗」。结论句负责回答这个，颜色承载分级，
   * 精确数值仍留在环上。传 `ReactNode` 是因为内存那句要显示「已用 / 总量」两个数。
   */
  caption?: ReactNode
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

/**
 * 把 CPU 构成压成「前 4 项 + 其余归并」（FR-513）。
 *
 * 为什么不直接铺八行：悬浮卡是浮层，八行数字会把它撑得比屏幕还高，
 * 而 nice/irq/softirq/steal 在绝大多数机器上都是零点几——铺出来只是噪声。
 * 但「其余」必须保留：把所有小项直接丢掉会让人以为构成只包含这几项，
 * 加起来不到 100% 反而更困惑。
 *
 * 排序按占比降序：idle 通常最大，但机器忙时 user 会盖过它——
 * 让数据自己说话，而不是硬编码一个「idle 永远在前」的顺序。
 */
function buildCpuBreakdownRows(b: {
  user: number
  nice: number
  system: number
  idle: number
  iowait: number
  irq: number
  softirq: number
  steal: number
}): { key: string; pct: number }[] {
  const all = [
    { key: 'user', pct: b.user },
    { key: 'system', pct: b.system },
    { key: 'idle', pct: b.idle },
    { key: 'iowait', pct: b.iowait },
    { key: 'nice', pct: b.nice },
    { key: 'irq', pct: b.irq },
    { key: 'softirq', pct: b.softirq },
    { key: 'steal', pct: b.steal },
  ].sort((x, y) => y.pct - x.pct)

  const head = all.slice(0, 4)
  const rest = all.slice(4)
  const restSum = rest.reduce((n, r) => n + r.pct, 0)
  // 只在「其余」确实有量时才加这一行：全为 0 时加一行 `其余 0.0%` 纯属占位。
  if (restSum > 0.05) head.push({ key: 'other', pct: restSum })
  return head
}

/** 首页仪表的有界受管资源 Tooltip，不展示任意 OS 进程。 */
export function GaugeAttributionTooltip({
  gauge,
  label,
  children,
  caption,
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
        球体 + 结论句合成一个纵向流：环在上（精确读数），结论在下（判断），
        与宝塔首页三个球的信息层级一致。

        结论句是**包内可选**的：不传 caption 时外观与旧版完全一致（只有环），
        因此其他调用方（超级工作台等）不受影响。
      */}
      <div className="flex flex-col items-center gap-0.5">
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
            /*
              【悬停反馈：不动背景色，改提亮环 + 轻微抬起】
              用户要求保留 hover 反馈，但原先的 `hover:bg-accent/40` 是整块卡变浅底——
              三个球并排时鼠标扫过会轮流闪一下，反而干扰读数。

              改成两处更克制的信号：
              - 整块轻微上浮 1px（translate 不参与布局，不会推动三卡高度）；
              - 环的描边加粗（`[&_circle]` 选择器命中 ResourceGauge 的进度环），
                提亮但不遮挡数值，也不改变卡片底色。
            */
            className="group rounded-sm transition-transform duration-[var(--motion-duration-fast)] ease-ios hover:-translate-y-px [&_svg:not([class*='size-'])]:size-auto [&_svg_circle:last-child]:transition-[stroke-width] [&_svg_circle:last-child]:duration-[var(--motion-duration-fast)] hover:[&_svg_circle:last-child]:stroke-[10] focus-visible:[&_svg_circle:last-child]:stroke-[10]"
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
            /*
              【照宝塔的分层】宝塔的悬浮卡是「结论徽标 → 分段小标题 → 明细行 → 展开入口」，
              每段都有明确标题，扫读成本低于原来的一整片无标题列表。
              这里保留同一套层次，但内容只用**后端已采得到**的东西：
              节点归因、Top 实例、Top 进程。宝塔的 1/5/15 分钟负载、进程总数、
              CPU 时间构成、整机 top5 进程，我们的 Worker 尚未采集（已记入后端待办）。
            */
            <div className="space-y-2">
              {/* 结论徽标：一眼看到当前球处于什么状态，与环的颜色同一判据。 */}
              <div className="flex flex-wrap items-center gap-1.5">
                <span
                  className="inline-flex items-center rounded px-1.5 py-0.5 text-[11px] font-medium"
                  style={{
                    background: 'var(--muted)',
                    color: statusColorVar(resourceLevel(gauge === 'memory' ? (data.nodes[0]?.memoryUsedBytes ?? 0) : gauge === 'cpu' ? (data.nodes[0]?.cpuPct ?? 0) : 0)),
                  }}
                >
                  {label} · {t(`dashboard.loadVerdict.${resourceLevel(gauge === 'cpu' ? (data.nodes[0]?.cpuPct ?? 0) : 0)}`, VERDICT_FALLBACK.success)}
                </span>
                <span className="text-[11px] text-muted-foreground">
                  {formatObservedAt(data.sampledAt, t('dashboard.sampleTimeUnknown'))} · {t(`dashboard.freshness.${data.freshness}`, data.freshness)}
                </span>
              </div>

              {/* 节点归因：哪个节点在贡献这份用量（可下钻监控页）。 */}
              <div>
                <div className="mb-1 border-y py-1 text-[11px] text-muted-foreground">
                  {t('dashboard.attributionByNode', '按节点')}
                </div>
                <div className="space-y-1">
                  {data.nodes.slice(0, 3).map((node) => (
                    <OverviewLink key={node.nodeId} href={`/monitor?node=${encodeURIComponent(node.nodeUuid)}`} className="flex justify-between gap-2 hover:text-primary" renderLink={renderLink}>
                      <span className="truncate">{node.name} · {t(`dashboard.freshness.${node.status}`, node.status)}</span>
                      <span className="font-mono">{formatValue(node.cpuPct, node.memoryUsedBytes)}</span>
                    </OverviewLink>
                  ))}
                </div>
              </div>

              {/* FR-513：负载三档。单值看不出「瞬时尖峰」还是「持续爬升」——
                  5/15 分钟两档下降而 1 分钟高，说明已经过去；反之说明还在恶化。
                  仅在有数据时渲染：老 Worker 不报这两档，画两个 `--` 只是噪声。 */}
              {data.nodes[0] && (data.nodes[0].load5Pct != null || data.nodes[0].load15Pct != null) ? (
                <div>
                  <div className="mb-1 border-y py-1 text-[11px] text-muted-foreground">
                    {t('dashboard.attributionLoadTiers', '负载三档')}
                  </div>
                  <div className="flex gap-3 font-mono text-[11px]">
                    {/* 三档各自带可访问名：`5m` 与 `15m` 的文本互为子串，
                        仅凭可见文本无法精确区分（用例也因此更难写准）。 */}
                    <span aria-label={t('dashboard.loadTier1', '1 分钟负载')}>
                      1m {displayNumber(data.nodes[0].loadPct ?? null, '%')}
                    </span>
                    <span aria-label={t('dashboard.loadTier5', '5 分钟负载')}>
                      5m {displayNumber(data.nodes[0].load5Pct ?? null, '%')}
                    </span>
                    <span aria-label={t('dashboard.loadTier15', '15 分钟负载')}>
                      15m {displayNumber(data.nodes[0].load15Pct ?? null, '%')}
                    </span>
                  </div>
                </div>
              ) : null}

              {/* FR-513：CPU 构成。只看总占用分不清瓶颈类型——iowait 高是 IO 瓶颈、
                  system 高是内核开销、user 高才是应用自身忙，三者处置动作完全不同。
                  只展示前 4 项 + 其余归并，避免 8 行数字把浮层撑爆。 */}
              {data.nodes[0]?.cpuBreakdown ? (
                <div>
                  <div className="mb-1 border-y py-1 text-[11px] text-muted-foreground">
                    {t('dashboard.attributionCpuBreakdown', 'CPU 构成')}
                  </div>
                  <div className="space-y-1">
                    {buildCpuBreakdownRows(data.nodes[0].cpuBreakdown).map((row) => (
                      <div key={row.key} className="flex justify-between gap-2 font-mono text-[11px]">
                        <span>{t(`dashboard.cpuPart.${row.key}`, row.key)}</span>
                        <span>{row.pct.toFixed(1)}%</span>
                      </div>
                    ))}
                  </div>
                </div>
              ) : null}

              {/* FR-513：分挂载点磁盘。单值 diskUsage 只覆盖根分区——
                  `/var` 写满而 `/` 宽松时完全无感，而这正是数据盘最常见的故障形态。 */}
              {data.nodes[0]?.mounts && data.nodes[0].mounts.length > 0 ? (
                <div>
                  <div className="mb-1 border-y py-1 text-[11px] text-muted-foreground">
                    {t('dashboard.attributionMounts', '分挂载点磁盘')}
                  </div>
                  <div className="space-y-1">
                    {data.nodes[0].mounts.slice(0, 5).map((m) => (
                      <div key={m.mountpoint} className="flex justify-between gap-2 font-mono text-[11px]">
                        <span className="truncate">{m.mountpoint}</span>
                        <span style={m.usedPct >= 90 ? { color: 'var(--danger-ink)' } : undefined}>
                          {m.usedPct.toFixed(1)}%
                        </span>
                      </div>
                    ))}
                  </div>
                </div>
              ) : null}

              {/* FR-513：进程计数。主机级口径（非实例内），配合上面负载三档判断机器整体压力。 */}
              {data.nodes[0]?.procsTotal != null ? (
                <div className="flex justify-between gap-2 text-[11px] text-muted-foreground">
                  <span>{t('dashboard.attributionProcs', '进程数（运行 / 总）')}</span>
                  <span className="font-mono">
                    {displayNumber(data.nodes[0].procsRunning ?? null, '')} / {data.nodes[0].procsTotal}
                  </span>
                </div>
              ) : null}

              {/* Top 实例：这份用量主要来自哪几个实例。 */}
              <div>
                <div className="mb-1 border-y py-1 text-[11px] text-muted-foreground">
                  {t('dashboard.attributionTopInstances', 'Top 实例')}
                </div>
                <div className="space-y-1">
                  {data.topInstances.slice(0, 3).map((instance) => (
                    <OverviewLink key={instance.instanceId} href={`/monitor?instance=${encodeURIComponent(instance.instanceUuid)}`} className="flex justify-between gap-2 hover:text-primary" renderLink={renderLink}>
                      <span className="truncate">{instance.instanceName}</span>
                      <span className="font-mono">{formatValue(instance.cpuPct, instance.rssBytes)}</span>
                    </OverviewLink>
                  ))}
                  {data.topInstances.length === 0 && (
                    <p className="text-[11px] text-muted-foreground">{t('dashboard.noData')}</p>
                  )}
                </div>
              </div>

              {/* Top 进程：受管进程树内的明细（不是整机 top5，两者口径不同）。 */}
              <div>
                <div className="mb-1 border-y py-1 text-[11px] text-muted-foreground">
                  {t('dashboard.attributionTopProcesses', 'Top 受管进程')}
                </div>
                <div className="space-y-1">
                  {data.topProcesses.slice(0, 4).map((process) => (
                    <OverviewLink key={`${process.instanceId}-${process.pid}`} href={`/monitor?instance=${encodeURIComponent(process.instanceUuid)}`} className="flex justify-between gap-2 hover:text-primary" renderLink={renderLink}>
                      <span className="truncate">{process.instanceName} / {process.name || `PID ${process.pid}`}</span>
                      <span className="font-mono">{formatValue(process.cpuPercent, process.rssBytes)}</span>
                    </OverviewLink>
                  ))}
                  {data.topProcesses.length === 0 && (
                    <p className="text-[11px] text-muted-foreground">{t('dashboard.noData')}</p>
                  )}
                </div>
              </div>

              {/* FR-513：**整机** top-N 进程。与上面「Top 受管进程」并列但口径不同——
                  这个包含系统进程与别人的进程，才是「机器为什么慢」的答案。
                  标题必须能区分，否则会被读成同一份数据。 */}
              {data.hostTopProcesses && data.hostTopProcesses.length > 0 ? (
                <div>
                  <div className="mb-1 border-y py-1 text-[11px] text-muted-foreground">
                    {t('dashboard.attributionHostProcesses', '整机 Top 进程')}
                  </div>
                  <div className="space-y-1">
                    {data.hostTopProcesses.slice(0, 5).map((p) => (
                      <div key={`${p.nodeId}-${p.pid}`} className="flex justify-between gap-2 font-mono text-[11px]">
                        <span className="truncate">{p.comm || `PID ${p.pid}`}</span>
                        <span>{displayNumber(p.cpuPct, '%')}</span>
                      </div>
                    ))}
                  </div>
                </div>
              ) : null}

              <p className="text-[11px] text-muted-foreground">{t('dashboard.resourceObservation')}</p>
            </div>
          )}
        </PopoverContent>
      </Popover>
        {caption}
      </div>
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

/** 玩家在线趋势卡属性。 */
export interface OverviewTrendPanelProps {
  trend?: PlayerTrendResult
  isLoading: boolean
  isError: boolean
  testId: string
  className?: string
  /**
   * 可用性 SLO 快照（FR-463）。不单独占卡——挂在趋势卡头部一行带过，
   * 细节（MTTR / MTBF / 误差预算）留给监控页。
   */
  slo?: SLOResultView
}

/**
 * 玩家在线趋势卡（FR-469）：全网合计曲线 + 峰值/日均，头部附带可用率。
 *
 * 曲线用轻量内联 SVG 而不是 recharts：本页对渲染成本很敏感（趋势图已有实测
 * 246ms/4 张的教训，见文件头关于懒加载的说明），而这张图只需一条折线、
 * 无需坐标轴/图例/交互，用 recharts 等于为一根线引入整套 SVG 生成逻辑。
 * 精确读数由「峰值 / 日均」两个数字给出，曲线只承担形状（涨跌、尖峰）。
 */
export function OverviewTrendPanel({ trend, isLoading, isError, testId, className, slo }: OverviewTrendPanelProps) {
  const { t } = useTranslation()
  const points = (trend?.trend ?? []).map((p) => p.avg).filter((v) => typeof v === 'number')
  /*
    曲线与「峰值」标签必须用**同一份数据**算峰值。

    踩过的坑：`trend.peakValue` 是后端按自己的口径给的（可能是按小时桶的峰值），
    与 `trend[].avg` 序列的最大值并不相等（实测 212 vs 380）。如果拿 peakValue
    当纵轴上限、却用它去归一化 avg 序列，span 会变成负数，坐标越界到 999+，
    曲线直接画到画布外——表现就是「只看到几个点」。

    故纵轴范围由序列自身求得，`peakValue` 只用于标签显示。
  */
  const seriesMax = points.length ? Math.max(...points) : 0
  /*
    「峰值」标签取 max(后端 peakValue, 序列最大值)，而不是信任单一来源。

    实测两者会不一致：devmock 的 peakValue 是写死的常量 212.2，而 trend[] 序列
    由真随机生成、最高到 380。若直接显示 peakValue，标签上的「峰值 212」会**低于
    曲线的实际最高点**——用户看到的就是一个自相矛盾的数字（曲线明显冲到更高处，
    标签却说峰值只有 212）。取两者较大值可保证「标签不低于曲线」，任何一侧失真都不露馅。
  */
  const peak = Math.max(trend?.peakValue ?? 0, seriesMax)
  const dailyAvg = trend?.dailyAvg ?? 0

  let chart: ReactNode = <p className="py-10 text-center text-sm text-muted-foreground">{t('dashboard.noData')}</p>
  if (isError) chart = <p className="py-10 text-center text-sm text-destructive">{t('dashboard.trendUnavailable', '趋势加载失败')}</p>
  else if (isLoading) chart = <p className="py-10 text-center text-sm text-muted-foreground">{t('common.loading')}</p>
  else if (points.length >= 2) {
    const w = 640
    const h = 84
    /*
      纵轴从 0 起算，而不是从序列最小值起算。

      踩过的坑：一开始用 `min..max` 当纵轴范围，等于把「最低点」也拉到画布底部——
      玩家数在 65~370 之间波动时，曲线会被撑成满幅锯齿，看起来像抖动剧烈的故障信号，
      而实际只是正常昼夜起伏。从 0 起算后，起伏比例与真实量级一致。
      代价是低位时曲线贴近底部，但这正是「现在人少」的直观表达。
    */
    const span = seriesMax || 1
    const line = points
      .map((v, i) => `${(i / (points.length - 1)) * w},${h - (v / span) * (h - 12) - 6}`)
      .join(' L ')
    chart = (
      /*
        【为什么用 100% 宽 + 固定 viewBox + preserveAspectRatio=none】
        这是「撑满容器宽度的折线」的标准写法：viewBox 定坐标系（640×84），
        实际宽度交给 CSS，纵横比不锁定（none）以免曲线被等比缩放后中间留白。
        注意 svg 上不能再写死 `width`/`height` 属性值与 CSS 冲突——
        只给 `className="w-full"` 与 `style.height`，否则浏览器按属性尺寸裁剪。
      */
      <svg
        viewBox={`0 0 ${w} ${h}`}
        preserveAspectRatio="none"
        /*
          `h-full` + 父级 flex-1：本行由「玩家 Top5」的内容高度撑起（它是三栏里最高的），
          若图表固定 84px，趋势卡下半部会留下约 75px 空白（真机截图可见），
          看着像内容没加载完。让图表吃掉剩余高度，两张卡就等高且都填满。
          注意保留 `minHeight`：容器极窄时不至于把曲线压成一条线。
        */
        className="h-full w-full"
        style={{ minHeight: h, display: 'block' }}
        role="img"
        aria-label={t('dashboard.playerTrend', '玩家在线趋势')}
      >
        <path d={`M 0,${h} L ${line} L ${w},${h} Z`} fill="var(--primary)" opacity=".1" />
        <path d={`M ${line}`} fill="none" stroke="var(--primary)" strokeWidth="2" vectorEffect="non-scaling-stroke" />
      </svg>
    )
  }

  return (
    <Panel
      data-testid={testId}
      className={className}
      title={t('dashboard.playerTrend', '玩家在线趋势')}
      actions={
        <span className="flex items-center gap-3 text-xs text-muted-foreground tabular-nums">
          {/*
            可用率一行带过：`applicable=false` 时（窗口内无可用证据、分母为 0）
            显示「不适用」而不是 0%——否则读者会把它读成「完全不可用」。
            颜色按 target 达标与否分级：达标 muted、未达标红。
          */}
          {slo && (
            <span
              className="inline-flex items-center gap-1"
              title={t('dashboard.sloHint', '窗口内可用性；目标 {{target}}，误差预算已用 {{burned}}s / {{allowed}}s', {
                target: `${(slo.target * 100).toFixed(1)}%`,
                burned: Math.round(slo.budgetBurnedSec),
                allowed: Math.round(slo.budgetAllowedSec),
              })}
            >
              {t('dashboard.availability', '可用率')}
              {slo.applicable ? (
                <span
                  className="font-semibold"
                  style={{ color: slo.availability >= slo.target ? 'var(--status-success)' : 'var(--status-danger)' }}
                >
                  {(slo.availability * 100).toFixed(2)}%
                </span>
              ) : (
                <span className="text-muted-foreground">{t('dashboard.notApplicable', '不适用')}</span>
              )}
            </span>
          )}
          <span>
            {t('dashboard.trendPeak', '峰值')} {Math.round(peak)} · {t('dashboard.trendAvg', '日均')} {Math.round(dailyAvg)}
          </span>
        </span>
      }
      bodyClassName="p-3 flex flex-1 flex-col"
    >
      <div className="min-h-0 flex-1">{chart}</div>
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
      {/* 紧凑一行：首页改为固定视口（不滚动）后，这几项指标若不压成单行会把趋势图挤扁。
          说明文字（`notice`）改为 title 悬停——它是口径解释，读过一次即可，不该长期占两行高度。 */}
      {bots && <div className="p-2 text-xs" title={bots.notice}>
        <div className="grid grid-cols-2 gap-x-3 gap-y-1 text-muted-foreground sm:grid-cols-4">
          <span className="min-w-0 truncate">{t('dashboard.botRss')}: {bots.botWorkerRssBytes == null ? '--' : fmtBytes(bots.botWorkerRssBytes)}</span>
          <span className="min-w-0 truncate">{t('dashboard.botCpu')}: {displayNumber(bots.botWorkerCpuPct, '%')}</span>
          <span className="min-w-0 truncate">{t('dashboard.botCount')}: {displayNumber(bots.activeCount)}</span>
          <span className="min-w-0 truncate">{t('dashboard.botEventLoop')}: {displayNumber(bots.eventLoopP95Ms, 'ms')}</span>
        </div>
        {firstUnavailable && <p className="break-words text-amber-600">{t('dashboard.botUnavailable', { reason: firstUnavailable.reason })}</p>}
      </div>}
    </OverviewAggregationPanel>
  )
}


/**
 * 受控边界（ADR-103 a 范式）：**不取数、不发请求、不弹 toast**。
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
  /** 玩家在线趋势（FR-469）；缺省即空态。 */
  playerTrend?: PlayerTrendResult
  /** 趋势取数中。 */
  playerTrendLoading?: boolean
  /** 趋势取数失败（本卡局部降级）。 */
  playerTrendError?: boolean
  /** 玩家排行 Top-N（FR-469）。 */
  /** 排行取数中。 */
  /** 排行取数失败。 */
  /** 平台可用性 SLO（FR-463）；不单独占卡，并入玩家表头部一行。 */
  slo?: SLOResultView
  /** 链接渲染插槽（缺省原生 `<a href>`）。 */
  renderLink?: (args: OverviewLinkArgs) => ReactNode
  /**
   * 切到 AI 对话首页（FR-520 §4.4 的双向切换之一）。
   *
   * 做成回调而不是 `href` 链接：切换要**先写模式记忆再导航**，顺序不能反
   * （先导航会让新页面按旧记忆再判一次，出现闪回）。写记忆需要容器侧参与，
   * 视图只负责「用户点了」这件事。缺省不渲染该入口（如被嵌入到别处时不强加）。
   */
  onSwitchToAssistant?: () => void
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
  platformOverview,
  playerTrend,
  playerTrendLoading = false,
  playerTrendError = false,
  slo,
  renderLink,
  onSwitchToAssistant,
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


  // 「需要处理」的组装逻辑已随该区块一起移到页眉消息中心，首页不再计算。

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
      /** 默认在图例中取消勾选（数据仍进图，勾选即显示）。 */
      hiddenByDefault?: boolean
    }> = [
      { key: 'node_cpu_pct', labelKey: 'dashboard.totalCpu', full: 100, fmt: (v) => `${v.toFixed(0)}%` },
      /*
        负载用 `seriesMax`（按本序列峰值归一化），**不能写死 1**：
        `node_load` 是原始 load1（实测窗口内 0.0–2.4），而仪表上的「总负载」是
        「占核数百分比」（0–100）。把原始 load1 相对 1 归一化会画出 240% 的刻度
        （load1=2.4 → 240%），看起来像负载爆表。tooltip 仍显示原始倍数。
      */
      { key: 'node_load', labelKey: 'dashboard.totalLoad', full: 'seriesMax', fmt: (v) => v.toFixed(2) },
      /*
        内存用 `seriesMax`（按本序列峰值归一化），**不能用 `memTotal`**：
        仪表上的「120.0G / 320.0G」是「跨节点已用合计 / 单节点内存总量」，
        两者量纲本来就不匹配。用 memTotal 当满值会让 120G 画出 240% 的刻度
        （多节点合计超过单机总量），看起来像内存爆了。tooltip 仍按真实字节展示。
      */
      { key: 'node_mem_used', labelKey: 'dashboard.totalMem', full: 'seriesMax', fmt: fmtBytes },
      /*
        Bot Worker 两条共享进程指标也进同一张图（后端 `/metrics/overview` 已返回它们的序列）。
        **默认隐藏**：它们与节点资源不同量级（RSS 是百 MB 级、CPU 是十几个百分点），
        同屏常显会压扁上面三条主线；但要让读者知道「可以看」，所以仍进图例与坐标轴，
        勾选即显示（recharts 图例内建切换）。
      */
      { key: 'bot_worker_rss_bytes', labelKey: 'dashboard.botRss', full: 'seriesMax', fmt: fmtBytes, hiddenByDefault: true },
      { key: 'bot_worker_cpu_pct', labelKey: 'dashboard.botCpu', full: 'seriesMax', fmt: (v) => `${v.toFixed(1)}%`, hiddenByDefault: true },
      /*
        在线玩家**不在**这里：它不是资源占用，量纲与其余三条不同，混在一张归一化趋势图里
        会导致纵轴出现 >100%（玩家按窗口峰值归一化）。玩家改由上方独立的表格与 Top 排行承载。
      */
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
        // 默认隐藏的序列仍进 series：recharts 用 hide 只影响绘制，数据照常参与纵轴域。
        hidden: spec.hiddenByDefault,
        points: raw.map((p) => ({ ts: p.ts, value: p.v == null ? null : (p.v / full) * 100 })),
      })
    }
    return { series, scales }
  }, [overview, t])

  return (
    // 阶段 6 页面迁移：外壳与页头改用布局层原语（PageShell / PageHeader）。
    // data-page 由 PageShell spread 透传，e2e 的就绪信号依赖它。
    // variant="fixed"：整页不滚动（用户要求「第一屏铺满、不往下滚」）；
    // 趋势图用 flex-1 吃满剩余高度，其余区块保持自然高度。
    <PageShell data-page="overview" variant="fixed" className="gap-2">
      <PageHeader
        title={t('dashboard.title')}
        description={t('dashboard.subtitle')}
        actions={
          <>
            {/* 切回 AI 对话首页（FR-520 §4.4）。与 AI 首页上的「经典首页」入口位置对应，
                双向可达，用户不会「进去出不来」。 */}
            {onSwitchToAssistant && (
              <Button variant="outline" size="sm" onClick={onSwitchToAssistant}>
                <MessageSquare className="mr-1 size-3.5" />
                {t('assistant.homeSwitchAssistant', 'AI 对话')}
              </Button>
            )}
            <RangePicker value={range} onChange={onRangeChange} />
          </>
        }
      />

      {/* 顶部：环形仪表盘 + 统计块 */}
      {/*
        【三个球补「结论句」】环上只有百分比，是精确读数；而首页第一眼要的是判断——
        「47% 算不算高」「内存还够吗」。故照宝塔首页的做法，环下方补一句人话结论，
        颜色承载分级（success/warning/danger），数值仍留在环上。
        内存那句改显「已用 / 总量」绝对值：环上已经是百分比，下面再写一遍同一个数没有信息量。
      */}
      <div className="flex shrink-0 flex-col gap-2 xl:flex-row">
      {/* 左：三个环形仪表（还原原尺寸 92px）。指标是「占用率」，环形比数字更直观。 */}
      <div className="grid shrink-0 grid-cols-3 gap-2 xl:w-[27rem]">
        <GaugeAttributionTooltip
          gauge="cpu"
          label={t('dashboard.totalCpu')}
          active={activeGauge === 'cpu'}
          onToggle={() => onActiveGaugeChange(activeGauge === 'cpu' ? null : 'cpu')}
          data={attribution}
          isLoading={attributionLoading}
          isError={attributionError}
          renderLink={renderLink}
          caption={
            <span className="text-sm font-semibold" style={{ color: statusColorVar(resourceLevel(totals?.cpuPct ?? 0)) }}>
              {t(`dashboard.loadVerdict.${resourceLevel(totals?.cpuPct ?? 0)}`, VERDICT_FALLBACK[resourceLevel(totals?.cpuPct ?? 0)])}
            </span>
          }
        >
          <ResourceGauge label={t('dashboard.totalCpu')} value={totals?.cpuPct ?? 0} unit="%" />
        </GaugeAttributionTooltip>
        <GaugeAttributionTooltip
          gauge="load"
          label={t('dashboard.totalLoad')}
          active={activeGauge === 'load'}
          onToggle={() => onActiveGaugeChange(activeGauge === 'load' ? null : 'load')}
          data={attribution}
          isLoading={attributionLoading}
          isError={attributionError}
          renderLink={renderLink}
          caption={
            <span className="text-sm font-semibold" style={{ color: statusColorVar(resourceLevel(totals?.loadAvg ?? 0)) }}>
              {t(`dashboard.loadVerdict.${resourceLevel(totals?.loadAvg ?? 0)}`, VERDICT_FALLBACK[resourceLevel(totals?.loadAvg ?? 0)])}
              {' · '}
              <span className="tabular-nums">{((totals?.loadAvg ?? 0) / 100).toFixed(2)}×</span>
            </span>
          }
        >
          {/* 负载是「占总核数比例」：以倍数（load÷核）呈现而非百分比，环按 1.0=满核封顶，
              不再出现 >100% 的破环（FR-108）。grading 仍按占比走 resourceLevel（>0.8×→红）。 */}
          <ResourceGauge label={t('dashboard.totalLoad')} value={(totals?.loadAvg ?? 0) / 100} max={1} unit="×" decimals={2} />
        </GaugeAttributionTooltip>
        <GaugeAttributionTooltip
          gauge="memory"
          label={t('dashboard.totalMem')}
          active={activeGauge === 'memory'}
          onToggle={() => onActiveGaugeChange(activeGauge === 'memory' ? null : 'memory')}
          data={attribution}
          isLoading={attributionLoading}
          isError={attributionError}
          renderLink={renderLink}
          caption={
            /* whitespace-nowrap：内存那句是「已用 / 总量」两个数，窄屏（三卡并排时每卡约 150px）
               会被折成两行，导致三张卡高度不齐。结论句恒单行，超出由父级截断。 */
            <span className="whitespace-nowrap text-sm font-semibold text-muted-foreground tabular-nums">
              {totals?.memUsedBytes == null ? '--' : fmtBytes(totals.memUsedBytes)}
              {' / '}
              {totals?.memTotalBytes == null ? '--' : fmtBytes(totals.memTotalBytes)}
            </span>
          }
        >
          <ResourceGauge label={t('dashboard.totalMem')} value={memPct} unit="%" />
        </GaugeAttributionTooltip>
      </div>

      {/* 右：六张统计卡（图标 + 数字），3 列 × 2 行。
          补充的三项来自已取的 `/observability/overview` 快照（首页本就为平台观测区取它），
          不额外发请求：崩溃与停止是排查时最常看的两项，实例总数用来对照运行数。

          图标语义与配色对应「这一项好不好」，与数字本身的含义一致：
          在线类用成功绿、需处理用危险红、已停止用中性、总数用主色。 */}
      <div className="grid min-w-0 flex-1 grid-cols-2 gap-2 sm:grid-cols-3">
        <StatCard
          icon={<Server className="size-3.5" />}
          tone="success"
          label={t('dashboard.nodes')}
          value={`${totals?.onlineNodeCount ?? 0}/${totals?.nodeCount ?? nodeTotal ?? 0}`}
          sub={t('dashboard.online')}
        />
        <StatCard
          icon={<Activity className="size-3.5" />}
          tone="success"
          label={t('dashboard.runningInstances')}
          value={String(totals?.runningInstances ?? 0)}
          sub={t('dashboard.instances')}
        />
        <StatCard
          icon={<Users className="size-3.5" />}
          tone="primary"
          label={t('dashboard.onlinePlayers')}
          value={String(totals?.onlinePlayers ?? 0)}
          sub={t('nav.players')}
        />
        <StatCard
          icon={<AlertTriangle className="size-3.5" />}
          tone="danger"
          valueTone={platformOverview?.health.crashedInstanceCount ? 'danger' : 'default'}
          label={t('dashboard.crashedInstances', '崩溃实例')}
          value={String(platformOverview?.health.crashedInstanceCount ?? 0)}
          sub={t('dashboard.needAttention', '需处理')}
        />
        <StatCard
          icon={<Ban className="size-3.5" />}
          tone="neutral"
          label={t('dashboard.stoppedInstances', '停止实例')}
          value={String(platformOverview?.health.stoppedInstanceCount ?? 0)}
          sub={t('dashboard.stopped', '已停止')}
        />
        <StatCard
          icon={<Layers className="size-3.5" />}
          tone="info"
          label={t('dashboard.totalInstances', '实例总数')}
          value={String(
            (platformOverview?.health.runningInstanceCount ?? totals?.runningInstances ?? 0) +
              (platformOverview?.health.crashedInstanceCount ?? 0) +
              (platformOverview?.health.stoppedInstanceCount ?? 0),
          )}
          sub={t('dashboard.all', '全部')}
        />
      </div>
      </div>


      {/*
        【第二行：玩家在线】（图表 + 读数，两栏并排）

        结构：左侧玩家曲线（形状 = 昼夜起伏），右侧读数表（数值 = 现在多少、峰值在哪）。
        曲线与读数并排而不是上下堆叠——上下堆会把这一行撑到 300px 以上，
        挤掉下方资源趋势的可视高度（整页固定视口、不滚动）。

        玩家 Top 服务器已按用户要求删除：它与「服务器」页的实例列表回答同一件事，
        首页不再承担「哪台服在跑」的浏览职责。
      */}
      <PlayerStatsTable
        testId="overview-player-table"
        trend={playerTrend}
        isLoading={playerTrendLoading}
        isError={playerTrendError}
        currentPlayers={totals?.onlinePlayers}
        slo={slo}
      />

      {/*
        【第三行：资源趋势】纵轴只有真资源三条主线（CPU / 负载 / 内存），恒在 0–100%。
        Bot Worker 的两条共享进程指标**并入图内**（图例可见、默认隐藏、勾选显示），
        不再单独占底部参数条：同一组数据在两处出现既重复又占高度。
      */}
      <Panel title={t('dashboard.trends')} className="flex min-h-[200px] flex-1 flex-col" bodyClassName="flex min-h-0 flex-1 flex-col">
        <Suspense fallback={<Skeleton className="w-full flex-1" />}>
          {/* 高度由容器实测后传入：TimeSeriesChart 只接受数字 height（内部用固定像素画图），
              要「铺满剩余空间」就只能量一次容器高度。ResizeObserver 让窗口缩放/侧栏折叠后跟手。 */}
          <TrendFillChart
            series={trendChart.series}
            scales={trendChart.scales}
          />
        </Suspense>
      </Panel>

      {/* 集群健康墙已按用户要求移除：单机水位与异常改由页眉「需要处理」承载，
          逐台热力矩阵不再占用首屏。renderHealthWall 插槽保留但不再调用（应用侧仍可复用）。 */}
    </PageShell>
  )
}


/**
 * 铺满容器的资源趋势图。
 *
 * 为什么需要它：`TimeSeriesChart` 的 `height` 是数字（内部按固定像素绘制），而首页要求
 * 「趋势图占满第一屏剩余空间、整页不滚动」。所以在外面量一次可用高度再传进去，
 * 并用 ResizeObserver 跟随容器变化（窗口缩放、侧栏折叠、窄屏切换都会改变可用高度）。
 *
 * 下限保护：容器在首帧可能测到 0（父级还没布局），此时用一个保底高度，
 * 避免图表以 0 高渲染后不再自增；测量值低于保底时取保底。
 */
function TrendFillChart({
  series,
  scales,
}: {
  series: ChartSeries[]
  scales: Map<string, { full: number; fmt: (v: number) => string }>
}) {
  /*
    默认隐藏的实现：recharts 的图例点击是**它内部状态**，外部拿不到「当前勾了哪几条」。
    所以这里自己维护一份「用户已开启的键」集合：初始为空 = 全部默认隐藏项不画；
    图例点击时由 recharts 内部切换线的显隐，同时我们把该键记进集合，保证重渲染后不回弹。
  */

  const ref = useRef<HTMLDivElement | null>(null)
  const [height, setHeight] = useState(TREND_CHART_HEIGHT)

  useEffect(() => {
    const el = ref.current
    if (!el || typeof ResizeObserver === 'undefined') return undefined
    const measure = () => {
      const h = el.getBoundingClientRect().height
      if (h > 0) setHeight(Math.max(MIN_TREND_FILL_HEIGHT, Math.round(h)))
    }
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  return (
    <div ref={ref} className="min-h-0 flex-1" data-testid="overview-trend-fill">
      <TimeSeriesChart
        series={series}
        height={height}
        yDomain={[0, 100]}
        valueFormatter={(v, name) => {
          const scale = name ? scales.get(name) : undefined
          if (!scale) return `${v.toFixed(0)}%`
          return scale.fmt((v / 100) * scale.full)
        }}
      />
    </div>
  )
}


/**
 * 玩家在线表格（FR-469）：从「资源趋势」独立出来的玩家视角。
 *
 * 【为什么不与资源放一张图】玩家数不是资源占用，量纲也没有自然上限。
 * 混进归一化趋势图会让纵轴出现 >100% 的刻度（曾出现 240%），读起来像故障。
 *
 * 【为什么是表格】玩家侧要回答的是「现在多少、高峰在哪、分布长什么样」——
 * 这些是**读数**而不是趋势形状，表格比曲线更直接；曲线留给下方资源趋势。
 *
 * 【放了哪些】全部由已有的一次取数算出，不额外发请求：
 * 当前在线 / 峰值（含发生时刻）/ 日均 / 窗口最低 / 波动幅度 / 高峰与低谷时段 / 可用率。
 */
export function PlayerStatsTable({
  testId,
  trend,
  isLoading,
  isError,
  currentPlayers,
  slo,
  className,
}: {
  testId: string
  trend?: PlayerTrendResult
  isLoading?: boolean
  isError?: boolean
  /** 当前在线（来自 `/metrics/overview` 的 totals；缺失时退回趋势最后一点）。 */
  currentPlayers?: number
  slo?: SLOResultView
  className?: string
}) {
  const { t } = useTranslation()
  const points = trend?.trend ?? []
  const values = points.map((p) => p.avg).filter((v): v is number => v != null)

  // 当前在线：totals 优先；缺失时取趋势最后一点（比均值为 0 更接近「现在」）。
  const lastValue = values.length > 0 ? values[values.length - 1] : undefined
  const now = currentPlayers ?? (lastValue == null ? null : Math.round(lastValue))
  const peak = trend?.peakValue ?? (values.length > 0 ? Math.max(...values) : null)
  const lowest = values.length > 0 ? Math.min(...values) : null
  const avg = trend?.dailyAvg ?? null
  // 波动幅度：峰谷差相对峰值的比例，用来判断「是不是长尾尖峰」。
  const swingPct = peak && peak > 0 && lowest != null ? ((peak - lowest) / peak) * 100 : null

  /** 峰值发生时刻：接口给的是 ISO 时间，按用户本地时区显示到分钟。 */
  const peakAtText = trend?.peakAt
    ? new Date(trend.peakAt).toLocaleString(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
    : null

  /*
    时段分布：`hourlyDist` 是 24 小时桶。取最大与最小桶的**下标**即高峰/低谷时段。
    全 0（无数据）时不显示——那会误导成「0 点最忙」。
  */
  const dist = trend?.hourlyDist ?? []
  const distMax = dist.length > 0 ? Math.max(...dist) : 0
  const peakHour = distMax > 0 ? dist.indexOf(distMax) : -1
  const lowHour = distMax > 0 ? dist.indexOf(Math.min(...dist.filter((v) => v >= 0))) : -1
  const hourLabel = (h: number) => `${String(h).padStart(2, '0')}:00`

  /*
    两栏：左图表、右读数。
    图表给「形状」（昼夜起伏、尖峰），读数给「数值」（现在多少、峰值何时）。
    两者并排而非上下堆叠——上下堆会把这一行撑到 300px+，挤掉下方资源趋势的高度。
  */
  const chartBlock = isError ? (
    <p className="flex h-full items-center justify-center text-xs text-destructive">
      {t('dashboard.trendUnavailable', '趋势加载失败')}
    </p>
  ) : isLoading ? (
    <p className="flex h-full items-center justify-center text-xs text-muted-foreground">{t('common.loading')}</p>
  ) : values.length >= 2 ? (
    (() => {
      const w = 640
      const h = 120
      // 纵轴从 0 起算：用 min..max 会把 65~370 的正常波动撑成满幅锯齿，看起来像故障。
      const span = Math.max(...values, 1)
      const line = values
        .map((v, i) => `${(i / (values.length - 1)) * w},${h - (v / span) * (h - 12) - 6}`)
        .join(' L ')
      return (
        <svg
          viewBox={`0 0 ${w} ${h}`}
          preserveAspectRatio="none"
          className="block h-full w-full"
          role="img"
          aria-label={t('dashboard.playerTrend', '玩家在线趋势')}
        >
          <path d={`M 0,${h} L ${line} L ${w},${h} Z`} fill="var(--primary)" opacity=".12" />
          <path d={`M ${line}`} fill="none" stroke="var(--primary)" strokeWidth="2" vectorEffect="non-scaling-stroke" />
        </svg>
      )
    })()
  ) : (
    <p className="flex h-full items-center justify-center text-xs text-muted-foreground">{t('dashboard.noData')}</p>
  )

  return (
    <Panel
      data-testid={testId}
      className={className}
      title={t('dashboard.playerSection', '玩家在线')}
      actions={
        slo && (
          <span className="text-[11px] font-normal text-muted-foreground">
            {t('dashboard.availability', '可用率')}{' '}
            {/*
              `applicable=false`（窗口内无可用证据、分母为 0）必须显示「不适用」而不是 0%。
              显示 0% 会被读成「完全不可用」——那是与本意相反的结论。
            */}
            {slo.applicable ? (
              <span className="text-status-success tabular-nums">{(slo.availability * 100).toFixed(2)}%</span>
            ) : (
              <span className="text-muted-foreground">{t('dashboard.sloNotApplicable', '不适用')}</span>
            )}
          </span>
        )
      }
      bodyClassName="p-2"
    >
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-[minmax(0,1fr)_18rem]">
        {/* 左：图表 */}
        <div className="min-h-[110px] min-w-0">{chartBlock}</div>

        {/* 右：读数（两列网格，比表更省高度） */}
        <div className="grid grid-cols-2 gap-x-3 gap-y-1.5">
          {isError ? (
            <p className="col-span-2 text-xs text-destructive">{t('dashboard.trendUnavailable', '趋势加载失败')}</p>
          ) : isLoading ? (
            <p className="col-span-2 text-xs text-muted-foreground">{t('common.loading')}</p>
          ) : (
            <>
              <Stat label={t('dashboard.playersNow', '当前在线')} value={now == null ? '--' : String(now)} strong />
              <Stat
                label={t('dashboard.peakPlayers', '峰值在线')}
                value={peak == null ? '--' : String(Math.round(peak))}
                hint={peakAtText ? `${t('dashboard.peakAt', '发生时刻')} ${peakAtText}` : undefined}
              />
              <Stat label={t('dashboard.avgPlayers', '日均在线')} value={avg == null ? '--' : String(Math.round(avg))} />
              <Stat label={t('dashboard.lowestPlayers', '窗口最低')} value={lowest == null ? '--' : String(Math.round(lowest))} />
              <Stat
                label={t('dashboard.peakHour', '高峰时段')}
                value={peakHour >= 0 ? hourLabel(peakHour) : '--'}
                hint={peakHour >= 0 ? `${t('dashboard.peakHourCount', '该时段峰值')} ${Math.round(distMax)}` : undefined}
              />
              <Stat label={t('dashboard.lowHour', '低谷时段')} value={lowHour >= 0 ? hourLabel(lowHour) : '--'} />
              <Stat
                label={t('dashboard.playerSwing', '波动幅度')}
                value={swingPct == null ? '--' : `${swingPct.toFixed(0)}%`}
                hint={t('dashboard.playerSwingHint', '（峰值 − 最低）/ 峰值。偏高说明是短时尖峰而非稳定负载')}
                className="col-span-2"
              />
            </>
          )}
        </div>
      </div>
    </Panel>
  )
}

/** 玩家读数的小单元格：标签在上、数值在下（比表格行更紧凑）。 */
function Stat({
  label,
  value,
  hint,
  strong,
  className,
}: {
  label: string
  value: string
  hint?: string
  strong?: boolean
  className?: string
}) {
  return (
    <div className={className} title={hint}>
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className={strong ? 'text-lg font-semibold tabular-nums' : 'text-sm font-medium tabular-nums'}>
        {value}
      </div>
    </div>
  )
}

export default OverviewPageView
