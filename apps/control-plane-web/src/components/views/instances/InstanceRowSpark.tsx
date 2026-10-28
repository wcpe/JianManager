import { useCallback, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import { cn } from '@jianmanager/ui'
import { fmtBytes } from '@/lib/metrics/metrics-format'
import { TimeSeriesChart } from '@jianmanager/ui/charts/TimeSeriesChart'
import { healthHoverBridgeRef } from './health-hover-bridge'

/**
 * 行内缩略 sparkline + 悬浮大图（实例列表行）。
 *
 * 【为什么是缩略图而不是纯数字】这一列要回答的是「刚才稳不稳」，不是「此刻多少」。
 * 纯数字看不出 30 秒前刚从 20 TPS 掉到 3，而一条 60px 宽的折线能。
 *
 * 【为什么悬浮才给完整图表】行内一屏 50 行，四张完整曲线会把这个页面变成图表墙；
 * 而「要看细节」是低频动作，悬浮展开即可。数据**本来就在快照里**（同一批 batch 请求），
 * 悬浮不产生任何额外请求。
 *
 * 【为什么自带 SVG 而不复用 TimeSeriesChart】行内缩略图只有 60×24px，recharts 的最小
 * 渲染成本（坐标轴/网格/图例）远大于内容本身，50 行就是 50 套；而悬浮图只要一条线 + 端点值，
 * 手绘 SVG 更小更快，也不引入 lazy chunk。
 */

/** 折线用的数值序列（缩略图内部用）。 */
type Spark = number[]

/** 带时间戳的序列（正式图表用）。 */
export type TimedSeries = { ts: string; value: number }[]

/** 缩略图配色（按指标区分，与悬浮图同色系）。 */
const SPARK_COLOR: Record<string, string> = {
  tps: 'text-chart-1',
  players: 'text-chart-2',
  cpu: 'text-chart-3',
  mem: 'text-chart-4',
}

const SPARK_W = 60
const SPARK_H = 16

/** 把序列映射成 SVG 折线路径（横向铺满、纵向按 min..max 拉伸）。 */
function sparkPath(values: Spark, w = SPARK_W, h = SPARK_H): string {
  if (values.length === 0) return ''
  if (values.length === 1) {
    const y = h / 2
    return `M 0,${y} L ${w},${y}`
  }
  const min = Math.min(...values)
  const max = Math.max(...values)
  // 全平序列（如一直 20 TPS）：画在中线，避免除零与「贴底看起来像 0」的误读。
  const span = max - min || 1
  const pad = 2
  const usable = h - pad * 2
  return values
    .map((v, i) => {
      const x = (i / (values.length - 1)) * w
      const y = max === min ? h / 2 : pad + usable - ((v - min) / span) * usable
      return `${i === 0 ? 'M' : 'L'} ${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
}

/** 行内缩略图：一条折线，无坐标轴。 */
function Sparkline({ values, className }: { values: Spark; className?: string }) {
  if (values.length < 2) {
    return <span className="text-[11px] text-muted-foreground/60">--</span>
  }
  const d = sparkPath(values)
  return (
    <svg
      viewBox={`0 0 ${SPARK_W} ${SPARK_H}`}
      preserveAspectRatio="none"
      className={cn('ml-auto block h-4 w-full max-w-[52px]', className)}
      aria-hidden="true"
    >
      <path d={d} fill="none" stroke="currentColor" strokeWidth="1.75" vectorEffect="non-scaling-stroke" />
    </svg>
  )
}




/**
 * 服务器健康度：单格呈现「一句话结论 + 四指标迷你趋势」，悬浮展开完整四图。
 *
 * 【为什么合并成一项】原先行内占三列（实时指标 / 活跃度 / 心跳），每列又是一到两行，
 * 合计吃掉约 300px 宽、64px 高——一屏能看的实例数被压缩到十几个。
 * 而运维扫表时真正要回答的是「这台健不健康」，不是「CPU 是 55% 还是 56%」：
 * 后者是**悬浮才需要**的细节。故收敛为一个健康度格子，其余信息放进悬浮面板。
 *
 * 【健康度怎么算】不是新造指标，而是把已有读数按最差项归一：
 * TPS 停摆 / 玩家掉零 / CPU 打满 / 内存吃紧，任一触发即降级。
 * 判据集中在本文件，避免与 StatusBadge 的状态色各说一套。
 */
export type HealthLevel = 'success' | 'warning' | 'danger' | 'neutral'

/**
 * 健康度格子（纯展示件）。
 *
 * 【为什么不含 Popover】此前每个格子各自包一个 Radix Popover：50 行 = 50 个实例，
 * 每个都带触发器上下文、portal 容器与一组事件监听。实测点击排序时产生 7 个长任务、
 * 主线程累计阻塞约 2.4s——卡顿的主因就在这里。
 * 现在格子只渲染「圆点 + 结论 + 迷你曲线」，悬浮检测与面板渲染交给**表格级单例**
 * （见 `InstanceHealthHoverLayer`）：全表只有一个 Popover 实例，与行数无关。
 */
export function InstanceHealthCell({
  metrics,
  running,
  onHover,
}: {
  metrics?: HealthMetrics
  /** 是否运行中：停机不做健康判断（显示「未运行」而不是伪造 0 分）。 */
  running: boolean
  /** 鼠标进入/离开时上报（由表格层决定是否弹面板）。 */
  onHover?: (el: HTMLElement | null, metrics: HealthMetrics | undefined, running: boolean) => void
}) {
  const { t } = useTranslation()

  const level = healthLevelOf(metrics, running)
  const LABEL: Record<HealthLevel, string> = {
    success: t('instances.health.good', '良好'),
    warning: t('instances.health.warn', '注意'),
    danger: t('instances.health.bad', '异常'),
    neutral: running ? t('instances.health.unknown', '无数据') : t('instances.health.notRunning', '未运行'),
  }
  const DOT: Record<HealthLevel, string> = {
    success: 'bg-status-success',
    warning: 'bg-status-warning',
    danger: 'bg-status-danger',
    neutral: 'bg-muted-foreground/40',
  }

  const s = metrics?.series
  return (
    <div
      className="flex w-full items-center gap-1.5 rounded px-1 py-1"
      onMouseEnter={(e) => onHover?.(e.currentTarget, metrics, running)}
      onMouseLeave={() => onHover?.(null, undefined, false)}
    >
      <span className={cn('size-2 shrink-0 rounded-full', DOT[level])} />
      <span className="shrink-0 text-xs">{LABEL[level]}</span>
      {s && s.tps.length >= 2 && (
        <span className={cn('ml-auto min-w-0 flex-1 overflow-hidden text-right', SPARK_COLOR.tps)}>
          <Sparkline values={s.tps.map((p) => p.value)} />
        </span>
      )}
    </div>
  )
}

/** 健康度判定（抽出来供格子与面板共用，避免两处判据漂移）。 */
function healthLevelOf(metrics: HealthMetrics | undefined, running: boolean): HealthLevel {
  if (!running) return 'neutral'
  const hasData = metrics != null && (metrics.tps != null || metrics.cpuPct != null)
  if (!hasData) return 'neutral'
  const cpu = metrics.cpuPct ?? null
  const tps = metrics.tps ?? null
  const players = metrics.players ?? null
  if (cpu != null && cpu >= 90) return 'danger'
  if (tps != null && tps <= 0) return 'danger'
  if (cpu != null && cpu >= 80) return 'warning'
  if (players != null && players === 0) return 'warning'
  return 'success'
}

/** 健康度所需的指标子集（行内快照的超集，便于测试注入）。 */
export interface HealthMetrics {
  tps: number | null
  cpuPct: number | null
  memUsedBytes: number | null
  players: number | null
  playersMax: number | null
  series?: { tps: TimedSeries; players: TimedSeries; cpuPct: TimedSeries; memBytes: TimedSeries }
}

/**
 * 表格级健康度悬浮层：**全表只渲染一个面板**。
 *
 * 【为什么这样改】原来每行一个 Radix Popover，50 行就是 50 个实例；实测点击排序时
 * 产生 7 个长任务、主线程累计阻塞约 2.4s，页面明显卡顿。Radix 的 Popover 即使不展开，
 * 每个实例仍带 Root/Trigger 上下文、事件监听与 portal 占位，成本与行数线性相关。
 *
 * 现在：格子只上报「鼠标进了哪一格、这格的指标是什么」，面板由本组件按需渲染一个。
 * 与行数无关，50 行与 500 行的挂载成本相同。
 *
 * 【为什么用 fixed 定位而不是跟随元素】跟随元素要读 getBoundingClientRect 并在滚动时重算，
 * 每帧都触发布局读取；表格本身在滚动容器里，跟随会明显掉帧。固定在指针右下方偏移即可——
 * 用户视线本来就在鼠标附近，位置稳定性比「精确贴住单元格」更重要。
 */
/** 面板宽度（px）：内联到 style，图表列宽据此推导，两处不会漂移。 */
const PANEL_WIDTH = 520
const PANEL_CHART_WIDTH = Math.floor((PANEL_WIDTH - 24 - 12) / 2)

export function InstanceHealthHoverLayer() {
  const { t } = useTranslation()
  const [hover, setHover] = useState<{ x: number; y: number; metrics?: HealthMetrics; running: boolean } | null>(null)
  const timerRef = useRef<number | null>(null)

  /*
    悬停意图延迟：鼠标扫过表格时会经过大量格子，若立刻弹面板会疯狂重渲染。
    120ms 是「稍作停留」的经验阈值，短于它视为路过。
  */
  const handleHover = useCallback((el: HTMLElement | null, metrics?: HealthMetrics, running = false) => {
    if (timerRef.current) window.clearTimeout(timerRef.current)
    if (!el) {
      timerRef.current = window.setTimeout(() => setHover(null), 80)
      return
    }
    timerRef.current = window.setTimeout(() => {
      const r = el.getBoundingClientRect()
      setHover({ x: r.left, y: r.bottom + 6, metrics, running })
    }, 120)
  }, [])

  useEffect(() => () => { if (timerRef.current) window.clearTimeout(timerRef.current) }, [])

  // 把 handler 暴露给格子：经 context 传递会多一层 re-render，这里用模块级单例回调。
  //
  // 【为什么写在 effect 里而不是渲染期】渲染期写模块级可变对象会被
  // `react-hooks/immutability` 判为「渲染期修改组件外部变量」（副作用应在 effect 中）。
  // 改成 effect 后语义也更对：handler 指向**已提交**的最近一次渲染的闭包，
  // 而渲染期赋值会在 React 丢弃该次渲染（并发模式中断）时留下一个指向不存在渲染的 handler。
  useEffect(() => {
    healthHoverBridgeRef.current = handleHover
  }, [handleHover])

  if (!hover) return null

  const s = hover.metrics?.series
  const blocks = s
    ? [
        { key: 'tps', label: t('instances.metrics.tps', 'TPS'), vals: s.tps, fmt: (v: number) => v.toFixed(1) },
        { key: 'players', label: t('instances.metrics.players', '在线'), vals: s.players, fmt: (v: number) => String(Math.round(v)) },
        { key: 'cpu', label: t('instances.metrics.cpu', 'CPU'), vals: s.cpuPct, fmt: (v: number) => `${v.toFixed(0)}%` },
        { key: 'mem', label: t('instances.metrics.mem', '内存'), vals: s.memBytes, fmt: (v: number) => fmtBytes(v) },
      ]
    : []

  return createPortal(
    <div
      className="pointer-events-none fixed z-50 rounded-lg border bg-popover p-3 text-popover-foreground shadow-lg"
      style={{ width: PANEL_WIDTH, left: Math.min(hover.x, window.innerWidth - (PANEL_WIDTH + 20)), top: Math.min(hover.y, window.innerHeight - 320) }}
      role="tooltip"
      data-testid="instance-health-hover"
    >
      {blocks.length === 0 ? (
        <p className="text-[11px] text-muted-foreground">{t('instances.health.noSeries', '暂无指标序列')}</p>
      ) : (
        <div className="grid grid-cols-2 gap-x-3 gap-y-3">
          {blocks.map((b) => {
            const cur = b.vals.length > 0 ? b.vals[b.vals.length - 1] : null
            return (
              <div key={b.key} className="min-w-0">
                <div className="mb-1 flex items-baseline justify-between gap-2">
                  <span className="text-[11px] text-muted-foreground">{b.label}</span>
                  <span className="text-xs font-medium tabular-nums">{cur == null ? '--' : b.fmt(cur.value)}</span>
                </div>
                {/*
                  显式给宽度：面板宽 520 - 左右 padding 12×2 - 两列间距 12 = 484，单列 242。
                  `TimeSeriesChart` 自测宽要经 2000ms 防抖才落进 state，浮层场景会先空白两秒。
                */}
                <TimeSeriesChart
                  series={[{ key: b.key, name: b.label, points: b.vals }]}
                  height={96}
                  width={PANEL_CHART_WIDTH}
                  yDomain={['auto', 'auto']}
                  valueFormatter={(v) => b.fmt(v)}
                  emptyHint={t('instances.health.noSeries', '暂无指标序列')}
                />
              </div>
            )
          })}
        </div>
      )}
    </div>,
    document.body,
  )
}


