import { useTranslation } from 'react-i18next'
import { Server, Cpu, MemoryStick, HardDrive, Box, ChevronDown, ChevronRight } from 'lucide-react'
import type { NodeInfo } from '@/api/nodes'
import { MiniBar } from '@/components/ui/mini-bar'
import { Badge } from '@/components/ui/badge'
import { StatusBadge } from '@/components/ui/status-badge'
import { toneChipClass } from '@/lib/tone'
import type { StatusLevel } from '@/lib/threshold'
import { cn } from '@/lib/utils'

/** 节点状态码 → 状态等级（1 在线=正常 / 2 启动中=警告 / 0 离线=危险）。 */
function nodeStatusLevel(status: number): StatusLevel {
  if (status === 1) return 'success'
  if (status === 2) return 'warning'
  return 'danger'
}

/**
 * 节点工作台卡（FR-144，§4.5 运行实体范式）。
 * 内嵌 CPU/内存/磁盘水位条 + 在线呼吸灯 + 实例计数；点卡展开详情（分段趋势/对比）。
 * 离线节点资源条空轨（陈旧值不渲染，BUG-019 同理），操作收 kebab 由页面通过 menu 传入。
 */
export function NodeWorktableCard({
  node,
  instanceCount,
  expanded,
  onToggle,
  menu,
}: {
  node: NodeInfo
  /** 该节点上的实例数（由页面统一统计后传入）。 */
  instanceCount: number
  expanded: boolean
  onToggle: () => void
  /** 「⋯」操作菜单元素（JDK/端口/维护/排空/下线，由页面渲染）。 */
  menu: React.ReactNode
}) {
  const { t } = useTranslation()
  const online = node.status === 1
  const level = nodeStatusLevel(node.status)
  const statusLabel = online
    ? t('nodes.online')
    : node.status === 2
      ? t('nodes.starting')
      : t('nodes.offline')

  // 负载利用率（load1/核数*100），与详情仪表盘口径一致。
  const loadPct = node.cpuCores > 0 ? ((node.loadAvg1 ?? 0) / node.cpuCores) * 100 : 0

  return (
    <div className="flex flex-col rounded-xl border bg-card p-4 text-card-foreground shadow-soft transition-[transform,box-shadow] duration-300 ease-ios hover:-translate-y-0.5 hover:shadow-lift">
      <div className="flex items-center gap-3">
        <span className={cn('flex size-10 shrink-0 items-center justify-center rounded-xl', toneChipClass(online ? 'primary' : 'neutral'))}>
          <Server className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <button
            type="button"
            onClick={onToggle}
            className="flex max-w-full items-center gap-1 text-left text-sm font-semibold hover:text-primary"
            title={node.name}
          >
            {expanded ? <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" /> : <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" />}
            <span className="truncate">{node.name}</span>
          </button>
          <div className="mt-0.5 truncate text-xs text-muted-foreground" title={`${node.host} · ${node.os} ${node.arch}`}>
            {node.host}
          </div>
        </div>
        <span
          className={cn('size-2 shrink-0 rounded-full', online && 'animate-breathing')}
          style={{ backgroundColor: `var(--status-${level === 'neutral' ? 'info' : level})`, color: `var(--status-${level === 'neutral' ? 'info' : level})` }}
          aria-hidden
        />
        {menu}
      </div>

      <div className="mt-2 flex items-center gap-1.5">
        <StatusBadge level={level} label={statusLabel} />
        {node.maintenance && (
          <Badge variant="outline" className="text-status-warning border-status-warning/50">
            {t('nodes.maintenance')}
          </Badge>
        )}
      </div>

      {/* 内嵌资源水位：CPU / 内存 / 磁盘（离线空轨 + 「--」） */}
      <div className="mt-3 space-y-1.5">
        <ResourceLine icon={<Cpu className="size-3" />} label={t('nodes.cpu')} pct={(node.cpuUsage ?? 0) * 100} active={online} />
        <ResourceLine icon={<MemoryStick className="size-3" />} label={t('nodes.memory')} pct={(node.memoryUsage ?? 0) * 100} active={online} />
        <ResourceLine icon={<HardDrive className="size-3" />} label={t('nodes.disk')} pct={(node.diskUsage ?? 0) * 100} active={online} />
      </div>

      <div className="mt-3 flex items-center gap-4 border-t pt-3 text-sm text-muted-foreground">
        <span className="inline-flex items-center gap-1">
          <Box className="size-3.5" />
          <span className="tabular-nums">{instanceCount}</span> {t('nodes.instancesUnit')}
        </span>
        <span className="ml-auto inline-flex items-center gap-1 text-xs">
          {t('nodes.load')} <span className="tabular-nums">{online ? `${loadPct.toFixed(0)}%` : '--'}</span>
        </span>
      </div>
    </div>
  )
}

/** 卡内单条资源行：图标 + 标签 + MiniBar（离线时空轨 + 「--」）。 */
function ResourceLine({
  icon,
  label,
  pct,
  active,
}: {
  icon: React.ReactNode
  label: string
  pct: number
  active: boolean
}) {
  return (
    <div className="flex items-center gap-2">
      <span className="flex w-12 shrink-0 items-center gap-1 text-[10px] text-muted-foreground">
        {icon}
        {label}
      </span>
      <MiniBar value={active ? pct : 0} className="flex-1" />
      <span className="w-9 shrink-0 text-right text-[10px] tabular-nums text-muted-foreground">
        {active ? `${pct.toFixed(0)}%` : '--'}
      </span>
    </div>
  )
}
