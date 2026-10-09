import { useEffect, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@jianmanager/ui/components/badge'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { instanceStatusLevel } from '@jianmanager/ui/lib/threshold'
import { useCardColumns } from '@/lib/use-card-columns'
import { useVirtualRows } from '@/lib/virtual-list'
import { useStoredVirtualScroll } from '@/components/views/instances/VirtualizedInstanceTables'
import { RoleBadge } from '@/components/views/instances/InstanceTableParts'
import type { GroupDimension, InstanceGroup } from '@/lib/instance-grouping'
import type { InstanceInfo } from '@/lib/instance-types'
import type { ProxyRegistration } from '@/lib/proxy-registration'

/** 工作台卡渲染参数：由应用侧注入的卡片组件消费（该组件含启停等 mutation）。 */
export interface InstanceCardRenderArgs {
  inst: InstanceInfo
  nodeName: string
  roleBadge: ReactNode
  menu: ReactNode
  onOpen: () => void
}

/** proxy 行内联后端摘要的容器注入点。 */
export interface BackendsInlineProps {
  /** 已注册后端（容器经 useRegistrations 取数）。 */
  registrations?: ProxyRegistration[]
  /** 取数中。 */
  isLoading?: boolean
  /** 打开实例详情（容器注入路由跳转）。 */
  onOpenInstance: (id: number) => void
}
/**
 * 卡片视图（FR-136 工作台卡）：平铺或按分组维度分段渲染工作台卡网格。
 * 分组维度非 none 时每组一段（组头 + 该组卡片网格）。
 */
export function CardView({
  groupBy,
  groups,
  totalCount,
  onNeedMore,
  scrollStorageKey,
  groupLabel,
  nodeName,
  buildMenu,
  hasActiveFilter,
  onOpenInstance,
  renderCard,
}: {
  groupBy: GroupDimension
  groups: { key: string; instances: InstanceInfo[] }[]
  totalCount: number
  onNeedMore: () => void
  scrollStorageKey: string
  groupLabel: (key: string) => string
  nodeName: (id: number) => string
  buildMenu: (inst: InstanceInfo) => ReactNode
  hasActiveFilter: boolean
  onOpenInstance: (id: number) => void
  /** 工作台卡渲染插槽：应用侧注入含 mutation 的卡片组件。 */
  renderCard: (args: InstanceCardRenderArgs) => ReactNode
}) {
  const { t } = useTranslation()
  const grid = (list: InstanceInfo[], count = list.length, onMore: () => void = () => {}, key = scrollStorageKey) => (
    <VirtualizedCardGrid
      instances={list}
      totalCount={count}
      onNeedMore={onMore}
      scrollStorageKey={key}
      nodeName={nodeName}
      buildMenu={buildMenu}
      onOpenInstance={onOpenInstance}
      renderCard={renderCard}
    />
  )

  const loadedTotal = groups.reduce((sum, g) => sum + g.instances.length, 0)
  if (totalCount === 0 && loadedTotal === 0) {
    return (
      <p className="text-center text-muted-foreground py-8">
        {hasActiveFilter ? t('grouping.noMatch') : t('instances.empty')}
      </p>
    )
  }

  if (groupBy === 'none') {
    return grid(groups[0]?.instances ?? [], totalCount, onNeedMore)
  }
  // groupTree 为多级树：卡片视图按树前序展平为「每组一段」（每段只画本组**直接**成员，
  // 子组各自成段），保留层级结构；其余维度沿用顶层单段（region 的顶层 instances 即全量成员）。
  // `count` 为组头徽标计数：groupTree 用子树并集（含后代去重，与列表组头一致），
  // `instances` 仅为该段实际渲染的直接成员卡片。
  const segments: { key: string; label: string; instances: InstanceInfo[]; count: number; depth: number }[] = []
  if (groupBy === 'groupTree') {
    const walk = (list: InstanceGroup[], depth: number) => {
      for (const g of list) {
        segments.push({
          key: `${g.key}`,
          label: groupLabel(g.key),
          instances: g.direct ?? g.instances,
          count: g.instances.length,
          depth,
        })
        if (g.children && g.children.length > 0) walk(g.children, depth + 1)
      }
    }
    walk(groups, 0)
  } else {
    for (const g of groups) {
      segments.push({ key: g.key, label: groupLabel(g.key), instances: g.instances, count: g.instances.length, depth: 0 })
    }
  }
  return (
    <div className="space-y-4">
      {segments.map((seg) => (
        <div key={seg.key || '__none__'} className="space-y-2">
          <div className="flex items-center gap-2 px-1" style={{ paddingLeft: 4 + seg.depth * 16 }}>
            <span className="text-sm font-medium">{seg.label}</span>
            <Badge variant="outline" className="font-normal">{seg.count}</Badge>
          </div>
          {grid(seg.instances, seg.instances.length, () => undefined, `${scrollStorageKey}:group:${seg.key || '__none__'}`)}
        </div>
      ))}
    </div>
  )
}

export const CARD_ROW_HEIGHT = 244

export function VirtualizedCardGrid({
  instances,
  totalCount,
  onNeedMore,
  scrollStorageKey,
  nodeName,
  buildMenu,
  onOpenInstance,
  renderCard,
}: {
  instances: InstanceInfo[]
  totalCount: number
  onNeedMore: () => void
  scrollStorageKey: string
  nodeName: (id: number) => string
  buildMenu: (inst: InstanceInfo) => ReactNode
  onOpenInstance: (id: number) => void
  /** 工作台卡渲染插槽（应用侧注入含 mutation 的卡片组件）。 */
  renderCard: (args: InstanceCardRenderArgs) => ReactNode
}) {
  const columns = useCardColumns()
  const rowCount = Math.ceil(totalCount / columns)
  const {
    containerRef,
    onScroll,
    range,
    totalSize,
  } = useVirtualRows({
    total: rowCount,
    itemSize: CARD_ROW_HEIGHT,
    overscan: 4,
    fallbackViewportSize: 720,
  })
  const handleScroll = useStoredVirtualScroll(containerRef, onScroll, scrollStorageKey)

  useEffect(() => {
    if (range.end * columns + 20 >= instances.length && instances.length < totalCount) {
      onNeedMore()
    }
  }, [columns, instances.length, onNeedMore, range.end, totalCount])

  const rows = []
  for (let rowIndex = range.start; rowIndex < range.end; rowIndex++) {
    const startIndex = rowIndex * columns
    const rowItems: Array<{ index: number; inst?: InstanceInfo }> = []
    for (let offset = 0; offset < columns; offset++) {
      const index = startIndex + offset
      if (index < totalCount) rowItems.push({ index, inst: instances[index] })
    }
    rows.push({ rowIndex, rowItems })
  }

  return (
    <div
      ref={containerRef}
      onScroll={handleScroll}
      data-testid="instances-card-virtual"
      data-total-count={totalCount}
      className="max-h-[calc(100vh-18rem)] min-h-96 overflow-auto pr-1"
    >
      <div className="relative" style={{ height: totalSize }}>
        {rows.map(({ rowIndex, rowItems }) => (
          <div
            key={rowIndex}
            className="absolute inset-x-0 grid gap-4"
            style={{
              top: rowIndex * CARD_ROW_HEIGHT,
              minHeight: CARD_ROW_HEIGHT - 16,
              gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))`,
            }}
          >
            {rowItems.map(({ index, inst }) => (
              <div key={inst?.id ?? `placeholder-${index}`} data-testid="instances-card-virtual-item">
                {inst ? (
                  renderCard({
                    inst,
                    nodeName: nodeName(inst.nodeId),
                    roleBadge: <RoleBadge role={inst.role} compact />,
                    menu: buildMenu(inst),
                    onOpen: () => onOpenInstance(inst.id),
                  })
                ) : (
                  <div className="h-[228px] rounded-lg border border-dashed bg-card/70" aria-hidden="true" />
                )}
              </div>
            ))}
          </div>
        ))}
      </div>
    </div>
  )
}

/** proxy 行 inline 展开的已注册 backend 摘要（FR-136）。 */
export function BackendsInline({ registrations, isLoading = false, onOpenInstance }: BackendsInlineProps) {
  const { t } = useTranslation()

  if (isLoading) {
    return <p className="px-6 py-3 text-sm text-muted-foreground">{t('common.loading')}</p>
  }
  const regs = registrations ?? []
  if (regs.length === 0) {
    return <p className="px-6 py-3 text-sm text-muted-foreground">{t('proxy.noBackends')}</p>
  }
  return (
    <div className="px-6 py-3">
      <div className="mb-2 text-xs font-medium text-muted-foreground">
        {t('proxy.registeredBackends', { count: regs.length })}
      </div>
      <ul className="space-y-1">
        {regs.map((r) => {
          const b = r.backend
          return (
            <li key={r.id} className="flex items-center gap-3 text-sm">
              <span className="text-muted-foreground">{r.priority}</span>
              {b ? (
                <button
                  type="button"
                  className="font-medium text-primary hover:underline"
                  onClick={() => onOpenInstance(b.id)}
                >
                  {b.name}
                </button>
              ) : (
                <span className="font-medium">{r.alias || `#${r.backendId}`}</span>
              )}
              {r.alias && <span className="text-xs text-muted-foreground">({r.alias})</span>}
              {b && (
                <StatusBadge
                  level={instanceStatusLevel(b.status)}
                  label={t(`instances.${b.status.toLowerCase()}`, b.status)}
                  className="ml-auto"
                />
              )}
              {b && b.serverPort > 0 && (
                <span className="text-xs tabular-nums text-muted-foreground">:{b.serverPort}</span>
              )}
              {!r.enabled && (
                <Badge variant="outline" className="text-muted-foreground">
                  {t('common.disabled')}
                </Badge>
              )}
            </li>
          )
        })}
      </ul>
    </div>
  )
}
