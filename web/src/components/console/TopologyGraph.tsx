import { useMemo } from 'react'
import { useQueries } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import api from '@/api/client'
import type { InstanceInfo } from '@/api/instances'
import type { Registration } from '@/api/registrations'
import {
  buildTopology,
  layoutTopology,
  type LaidNode,
  type ProxyRegistrations,
} from '@/lib/topology'
import { instanceStatusLevel, statusColorVar } from '@/lib/threshold'
import { cn } from '@/lib/utils'

/** 拓扑节点盒尺寸（像素）。 */
const NODE_W = 168
const NODE_H = 44
const ROW_H = NODE_H
const PADDING_Y = 18

interface TopologyGraphProps {
  /** role=proxy 的实例集合；组件按需并行拉取各自的已注册后端。 */
  proxies: InstanceInfo[]
  className?: string
}

/**
 * 群组服 proxy↔backend 拓扑图（FR-145）：二部 SVG 自绘——左列代理 / 右列后端，
 * 连线 = 注册关系（M:N），节点与连线按运行状态着色（token 驱动，禁硬编码品牌色）。
 * 不引图库；布局走 lib/topology 纯函数。
 */
export default function TopologyGraph({ proxies, className }: TopologyGraphProps) {
  const { t } = useTranslation()

  // 每个 proxy 一条查询并行拉取其注册（proxy 数动态，用 useQueries）。
  const results = useQueries({
    queries: proxies.map((p) => ({
      queryKey: ['registrations', p.id],
      queryFn: async () => {
        const { data } = await api.get<Registration[]>(`/proxies/${p.id}/registrations`)
        return data
      },
      enabled: !!p.id,
    })),
  })

  const loading = results.some((r) => r.isLoading)
  // 各 proxy 注册数据的稳定快照标记（更新时间戳拼接），作为重算依赖键。
  const dataKey = results.map((r) => r.dataUpdatedAt).join(',')

  const laid = useMemo(() => {
    const input: ProxyRegistrations[] = proxies.map((p, i) => ({
      proxy: p,
      registrations: results[i]?.data ?? [],
    }))
    return layoutTopology(buildTopology(input), {
      width: 720,
      rowHeight: ROW_H,
      nodeWidth: NODE_W,
      paddingY: PADDING_Y,
    })
    // results 引用每渲染变，用 dataKey 表征其内容变化
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [proxies, dataKey])

  if (proxies.length === 0) {
    return (
      <div className={cn('flex h-40 items-center justify-center text-sm text-muted-foreground', className)}>
        {t('networks.topoNoProxy')}
      </div>
    )
  }

  if (loading) {
    return (
      <div className={cn('flex h-40 items-center justify-center text-sm text-muted-foreground', className)}>
        {t('common.loading')}
      </div>
    )
  }

  const hasBackend = laid.nodes.some((n) => n.kind === 'backend')

  return (
    <div className={cn('w-full overflow-x-auto', className)}>
      <svg
        viewBox={`0 0 ${laid.width} ${laid.height}`}
        width="100%"
        height={laid.height}
        role="img"
        aria-label={t('networks.topoTitle')}
        className="min-w-[560px]"
      >
        {/* 连线层（先画，置于节点下） */}
        <g>
          {laid.edges.map((e, i) => {
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
                strokeOpacity={e.enabled ? 0.8 : 0.4}
                strokeDasharray={e.enabled ? undefined : '4 4'}
              />
            )
          })}
        </g>
        {/* 节点层 */}
        <g>
          {laid.nodes.map((n) => (
            <TopoNodeBox key={`${n.kind}-${n.id}`} node={n} t={t} />
          ))}
        </g>
      </svg>

      {!hasBackend && (
        <p className="mt-2 text-center text-xs text-muted-foreground">{t('networks.topoNoBackend')}</p>
      )}
    </div>
  )
}

/** 单个拓扑节点盒（代理用主色描边、后端按状态着色）。 */
function TopoNodeBox({ node, t }: { node: LaidNode; t: (k: string, o?: Record<string, unknown>) => string }) {
  const x = node.x - NODE_W / 2
  const y = node.y - NODE_H / 2
  const isProxy = node.kind === 'proxy'
  const statusColor = isProxy ? 'var(--primary)' : statusColorVar(instanceStatusLevel(node.status))
  const roleLabel = t(`networks.role_${node.kind === 'proxy' ? 'proxy' : 'backend'}`, { defaultValue: node.kind })

  return (
    <g>
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
      <text
        x={x + 26}
        y={node.y - 3}
        fill="var(--card-foreground)"
        fontSize={12}
        fontWeight={600}
      >
        {truncate(node.name, 16)}
      </text>
      {/* 副信息：角色 · 端口 */}
      <text x={x + 26} y={node.y + 12} fill="var(--muted-foreground)" fontSize={10}>
        {roleLabel}
        {node.port ? ` · :${node.port}` : ''}
      </text>
    </g>
  )
}

/** SVG <text> 无自动省略，超长名称手动截断加省略号。 */
function truncate(s: string, max: number): string {
  return s.length > max ? `${s.slice(0, max - 1)}…` : s
}
