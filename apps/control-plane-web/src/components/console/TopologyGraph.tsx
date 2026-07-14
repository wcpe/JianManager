import { useMemo, useRef, useState, type PointerEvent as ReactPointerEvent, type WheelEvent as ReactWheelEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Maximize2, Search } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { useTopology } from '@/api/topology'
import {
  buildTopology,
  groupTopology,
  layoutTopologyGrouped,
  type LaidNode,
  type ProxyRegistrations,
} from '@/lib/topology'
import { instanceStatusLevel, statusColorVar, type StatusLevel } from '@jianmanager/ui'
import { cn } from '@jianmanager/ui'

/** 拓扑节点盒尺寸（像素）。 */
const NODE_W = 168
const NODE_H = 44
const ROW_H = NODE_H
const PADDING_Y = 18
const BAND_HEADER_H = 26
const BAND_GAP = 20
/** SVG 内容布局的逻辑宽度（viewBox 单位；容器按比例缩放）。 */
const CANVAS_W = 720
/** 视口容器定高（像素）——不随节点数膨胀。 */
const VIEWPORT_H = 560
const ZOOM_MIN = 0.5
const ZOOM_MAX = 4

/** 状态筛选可选项（复用状态等级；禁用连线单独一档）。 */
type StatusFilterKey = 'success' | 'warning' | 'danger' | 'neutral'

interface ViewBox {
  x: number
  y: number
  w: number
  h: number
}

interface TopologyGraphProps {
  className?: string
}

/**
 * 群组服 proxy↔backend 拓扑图（FR-145 / FR-335）：单条聚合查询消 per-proxy N+1；
 * SVG 视口壳（pan/zoom + 适应视图 + 名称搜索 + 状态筛选 + 按 network 分组分层布局），
 * 百级节点可用。不引图库；布局走 lib/topology 纯函数。
 */
export default function TopologyGraph({ className }: TopologyGraphProps) {
  const { t } = useTranslation()
  const { data, isLoading } = useTopology()

  // 状态：搜索词、仅显示匹配、状态筛选集合、禁用连线显隐。
  const [search, setSearch] = useState('')
  const [onlyMatch, setOnlyMatch] = useState(false)
  const [activeStatus, setActiveStatus] = useState<Set<StatusFilterKey>>(new Set())
  const [showDisabled, setShowDisabled] = useState(true)

  // 布局：由聚合结果构图 → 分组 → 分层布局（纯函数，随数据变重算）。
  const laid = useMemo(() => {
    const input: ProxyRegistrations[] = (data?.proxies ?? []).map((p) => ({
      proxy: {
        id: p.id,
        name: p.name,
        status: p.status,
        serverPort: p.serverPort,
        nodeId: p.nodeId,
        // buildTopology 只消费上述五字段；补齐 InstanceInfo 其余必填占位。
        uuid: '',
        type: '',
        role: 'proxy',
        processType: '',
        startCommand: '',
        workDir: '',
        autoStart: false,
        autoRestart: false,
        tags: null,
        createdAt: '',
      },
      registrations: p.registrations,
    }))
    const grouped = groupTopology(buildTopology(input), data?.networks ?? [])
    return layoutTopologyGrouped(grouped, {
      width: CANVAS_W,
      rowHeight: ROW_H,
      nodeWidth: NODE_W,
      paddingY: PADDING_Y,
      bandHeaderHeight: BAND_HEADER_H,
      bandGap: BAND_GAP,
    })
  }, [data])

  const contentBox = useMemo<ViewBox>(
    () => ({ x: 0, y: 0, w: CANVAS_W, h: Math.max(laid.height, VIEWPORT_H) }),
    [laid.height],
  )

  // viewBox 状态：默认贴合内容包围盒；用户交互后偏移/缩放（pan/zoom）。
  // 内容尺寸（布局）变化时在渲染期复位视口——用「上一次 key 存入 state」的官方模式，
  // 避免残留旧偏移把内容移出视野（React 支持的渲染期 setState 收敛）。
  const contentKey = `${contentBox.w}x${contentBox.h}`
  const [viewBox, setViewBox] = useState<ViewBox>(contentBox)
  const [seenKey, setSeenKey] = useState(contentKey)
  if (seenKey !== contentKey) {
    setSeenKey(contentKey)
    setViewBox(contentBox)
  }

  const svgRef = useRef<SVGSVGElement | null>(null)
  const dragState = useRef<{ startX: number; startY: number; origin: ViewBox } | null>(null)

  const fitView = () => setViewBox(contentBox)

  // 滚轮以光标为锚缩放（范围 ZOOM_MIN~ZOOM_MAX，相对内容宽度）。
  const onWheel = (e: ReactWheelEvent<SVGSVGElement>) => {
    e.preventDefault()
    const svg = svgRef.current
    if (!svg) return
    const rect = svg.getBoundingClientRect()
    const px = (e.clientX - rect.left) / rect.width // 0..1 光标横向占比
    const py = (e.clientY - rect.top) / rect.height
    const factor = e.deltaY < 0 ? 0.9 : 1 / 0.9
    const minW = contentBox.w / ZOOM_MAX
    const maxW = contentBox.w / ZOOM_MIN
    const nextW = clamp(viewBox.w * factor, minW, maxW)
    const nextH = nextW * (viewBox.h / viewBox.w)
    // 保持光标下的世界坐标不动。
    const anchorX = viewBox.x + px * viewBox.w
    const anchorY = viewBox.y + py * viewBox.h
    setViewBox({ x: anchorX - px * nextW, y: anchorY - py * nextH, w: nextW, h: nextH })
  }

  const onPointerDown = (e: ReactPointerEvent<SVGSVGElement>) => {
    ;(e.target as Element).setPointerCapture?.(e.pointerId)
    dragState.current = { startX: e.clientX, startY: e.clientY, origin: viewBox }
  }
  const onPointerMove = (e: ReactPointerEvent<SVGSVGElement>) => {
    const st = dragState.current
    const svg = svgRef.current
    if (!st || !svg) return
    const rect = svg.getBoundingClientRect()
    const dx = ((e.clientX - st.startX) / rect.width) * st.origin.w
    const dy = ((e.clientY - st.startY) / rect.height) * st.origin.h
    setViewBox({ ...st.origin, x: st.origin.x - dx, y: st.origin.y - dy })
  }
  const onPointerUp = () => {
    dragState.current = null
  }

  // 匹配集合：搜索命中的节点 id（名称/节点/状态子串）。空搜索=全命中。
  const matchIds = useMemo(() => {
    const q = search.trim().toLowerCase()
    const ids = new Set<number>()
    if (!q) return null // null 表示不做搜索高亮
    for (const n of laid.nodes) {
      const hay = `${n.name} ${n.status} ${n.nodeId ?? ''}`.toLowerCase()
      if (hay.includes(q)) ids.add(n.id)
    }
    return ids
  }, [search, laid.nodes])

  // 状态筛选后应隐藏的节点：activeStatus 非空时，仅显示等级∈activeStatus 的节点。
  const statusHidden = (level: StatusLevel): boolean =>
    activeStatus.size > 0 && !activeStatus.has(level as StatusFilterKey)

  const toggleStatus = (key: StatusFilterKey) =>
    setActiveStatus((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })

  if (isLoading) {
    return (
      <div className={cn('flex h-40 items-center justify-center text-sm text-muted-foreground', className)}>
        {t('common.loading')}
      </div>
    )
  }

  const proxyCount = data?.proxies.length ?? 0
  if (proxyCount === 0) {
    return (
      <div className={cn('flex h-40 items-center justify-center text-sm text-muted-foreground', className)}>
        {t('networks.topoNoProxy')}
      </div>
    )
  }

  const hasBackend = laid.nodes.some((n) => n.kind === 'backend')

  return (
    <div className={cn('w-full', className)}>
      {/* 工具条：适应视图 + 搜索 + 状态筛选 + 禁用线开关 */}
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Button type="button" size="xs" variant="outline" onClick={fitView} className="gap-1">
          <Maximize2 className="size-3.5" />
          {t('networks.topoFitView', { defaultValue: 'Fit view' })}
        </Button>
        <div className="relative">
          <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t('networks.topoSearch', { defaultValue: 'Search node…' })}
            className="h-7 w-44 pl-7 text-xs"
            aria-label={t('networks.topoSearch', { defaultValue: 'Search node' })}
          />
        </div>
        <label className="inline-flex cursor-pointer items-center gap-1.5 text-xs text-muted-foreground">
          <input type="checkbox" checked={onlyMatch} onChange={(e) => setOnlyMatch(e.target.checked)} className="size-3.5" />
          {t('networks.topoOnlyMatch', { defaultValue: 'Only matches' })}
        </label>
        <span className="mx-1 h-4 w-px bg-border" aria-hidden />
        <StatusFilterPills active={activeStatus} onToggle={toggleStatus} t={t} />
        <label className="inline-flex cursor-pointer items-center gap-1.5 text-xs text-muted-foreground">
          <input type="checkbox" checked={showDisabled} onChange={(e) => setShowDisabled(e.target.checked)} className="size-3.5" />
          {t('networks.topoShowDisabled', { defaultValue: 'Disabled links' })}
        </label>
      </div>

      {/* 视口：定高容器 + viewBox 驱动的 pan/zoom */}
      <div
        className="relative overflow-hidden rounded-lg border bg-muted/20"
        style={{ height: VIEWPORT_H }}
      >
        <svg
          ref={svgRef}
          viewBox={`${viewBox.x} ${viewBox.y} ${viewBox.w} ${viewBox.h}`}
          width="100%"
          height={VIEWPORT_H}
          role="img"
          aria-label={t('networks.topoTitle')}
          className="block h-full w-full cursor-grab touch-none active:cursor-grabbing"
          onWheel={onWheel}
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
          onPointerLeave={onPointerUp}
        >
          {/* 分组带背景 + 标题 */}
          <g>
            {laid.bands.map((band, i) => (
              <g key={`${band.id ?? 'ungrouped'}-${i}`}>
                <rect
                  x={4}
                  y={band.y}
                  width={CANVAS_W - 8}
                  height={band.height}
                  rx={10}
                  fill="var(--card)"
                  fillOpacity={0.4}
                  stroke="var(--border)"
                  strokeWidth={1}
                  strokeDasharray="3 4"
                />
                <text x={16} y={band.y + 16} fill="var(--muted-foreground)" fontSize={11} fontWeight={600}>
                  {band.id === null
                    ? t('networks.topoUngrouped', { defaultValue: 'Ungrouped' })
                    : band.name}
                </text>
              </g>
            ))}
          </g>
          {/* 连线层 */}
          <g>
            {laid.edges.map((e, i) => {
              if (!e.enabled && !showDisabled) return null
              const from = laid.nodes.find((n) => n.kind === 'proxy' && n.id === e.proxyId)
              const to = laid.nodes.find((n) => n.kind === 'backend' && n.id === e.backendId)
              const endpointsHidden =
                (from && statusHidden(instanceStatusLevel(from.status))) ||
                (to && statusHidden(instanceStatusLevel(to.status)))
              if (endpointsHidden) return null
              const dim =
                matchIds !== null &&
                !(matchIds.has(e.proxyId) || matchIds.has(e.backendId))
              if (dim && onlyMatch) return null
              const color = statusColorVar(e.level)
              const midX = (e.x1 + e.x2) / 2
              const d = `M ${e.x1} ${e.y1} C ${midX} ${e.y1}, ${midX} ${e.y2}, ${e.x2} ${e.y2}`
              return (
                <path
                  key={`${e.proxyId}-${e.backendId}-${i}`}
                  d={d}
                  fill="none"
                  stroke={color}
                  strokeWidth={2}
                  strokeOpacity={dim ? 0.12 : e.enabled ? 0.8 : 0.4}
                  strokeDasharray={e.enabled ? undefined : '4 4'}
                />
              )
            })}
          </g>
          {/* 节点层 */}
          <g>
            {laid.nodes.map((n) => {
              const level = instanceStatusLevel(n.status)
              if (statusHidden(level)) return null
              const matched = matchIds === null || matchIds.has(n.id)
              if (!matched && onlyMatch) return null
              return (
                <TopoNodeBox
                  key={`${n.kind}-${n.id}`}
                  node={n}
                  t={t}
                  dim={!matched}
                  multiHomed={laid.multiHomed.has(n.id)}
                />
              )
            })}
          </g>
        </svg>
      </div>

      {!hasBackend && (
        <p className="mt-2 text-center text-xs text-muted-foreground">{t('networks.topoNoBackend')}</p>
      )}
    </div>
  )
}

/** 状态筛选 pills（运行/过渡/崩溃/停止；复用健康分布色）。 */
function StatusFilterPills({
  active,
  onToggle,
  t,
}: {
  active: Set<StatusFilterKey>
  onToggle: (k: StatusFilterKey) => void
  t: (k: string, o?: Record<string, unknown>) => string
}) {
  const items: { key: StatusFilterKey; className: string; label: string }[] = [
    { key: 'success', className: 'bg-status-success', label: t('networks.healthRunning') },
    { key: 'warning', className: 'bg-status-warning', label: t('networks.healthTransitioning') },
    { key: 'danger', className: 'bg-status-danger', label: t('networks.healthCrashed') },
    { key: 'neutral', className: 'bg-muted-foreground/40', label: t('networks.healthStopped') },
  ]
  return (
    <div className="inline-flex items-center gap-1">
      {items.map((it) => {
        const on = active.has(it.key)
        return (
          <button
            key={it.key}
            type="button"
            onClick={() => onToggle(it.key)}
            aria-pressed={on}
            className={cn(
              'inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[11px] transition-colors',
              on ? 'border-primary bg-primary/10 text-foreground' : 'border-transparent text-muted-foreground hover:bg-accent/60',
            )}
          >
            <span className={cn('size-1.5 rounded-full', it.className)} />
            {it.label}
          </button>
        )
      })}
    </div>
  )
}

/** 单个拓扑节点盒（代理用主色描边、后端按状态着色；非匹配降透明、多归属加角标）。 */
function TopoNodeBox({
  node,
  t,
  dim,
  multiHomed,
}: {
  node: LaidNode
  t: (k: string, o?: Record<string, unknown>) => string
  dim: boolean
  multiHomed: boolean
}) {
  const x = node.x - NODE_W / 2
  const y = node.y - NODE_H / 2
  const isProxy = node.kind === 'proxy'
  const statusColor = isProxy ? 'var(--primary)' : statusColorVar(instanceStatusLevel(node.status))
  const roleLabel = t(`networks.role_${node.kind === 'proxy' ? 'proxy' : 'backend'}`, { defaultValue: node.kind })

  return (
    <g opacity={dim ? 0.28 : 1}>
      <rect
        x={x}
        y={y}
        width={NODE_W}
        height={NODE_H}
        rx={12}
        fill="var(--card)"
        stroke={isProxy ? 'var(--primary)' : 'var(--border)'}
        strokeWidth={isProxy ? 1.5 : 1}
      />
      {/* 左侧状态色点 */}
      <circle cx={x + 14} cy={node.y} r={4} fill={statusColor} />
      {/* 名称 */}
      <text x={x + 26} y={node.y - 3} fill="var(--card-foreground)" fontSize={12} fontWeight={600}>
        {truncate(node.name, 16)}
      </text>
      {/* 副信息：角色 · 端口 */}
      <text x={x + 26} y={node.y + 12} fill="var(--muted-foreground)" fontSize={10}>
        {roleLabel}
        {node.port ? ` · :${node.port}` : ''}
      </text>
      {/* 多归属角标（软标签可属多 network，落首带 + 提示） */}
      {multiHomed && (
        <g>
          <circle cx={x + NODE_W - 10} cy={y + 10} r={7} fill="var(--primary)" />
          <text
            x={x + NODE_W - 10}
            y={y + 13}
            fill="var(--primary-foreground)"
            fontSize={9}
            fontWeight={700}
            textAnchor="middle"
          >
            +
          </text>
          <title>{t('networks.topoMultiHomed', { defaultValue: 'Belongs to multiple networks' })}</title>
        </g>
      )}
    </g>
  )
}

/** SVG <text> 无自动省略，超长名称手动截断加省略号。 */
function truncate(s: string, max: number): string {
  return s.length > max ? `${s.slice(0, max - 1)}…` : s
}

/** 数值夹逼到 [min, max]。 */
function clamp(v: number, min: number, max: number): number {
  return Math.min(Math.max(v, min), max)
}
