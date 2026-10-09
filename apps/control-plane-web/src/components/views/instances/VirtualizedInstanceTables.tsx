import { Fragment, useCallback, useEffect, useLayoutEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import {
  Table,
  TableBody,
  TableCell,
  TableRow,
} from '@jianmanager/ui/components/table'
import { useVirtualRows } from '@jianmanager/ui/lib/virtual-list'
import { memberHealth, type MemberHealth } from '@/lib/topology'
import type { GroupDimension, InstanceGroup } from '@jianmanager/ui/lib/instance-grouping'
import type { InstanceInfo } from '@jianmanager/ui/lib/instance-types'
/** 虚拟滚动位置的 sessionStorage 键前缀（与 URL 组合成唯一键）。 */
export const SCROLL_KEY_PREFIX = 'jm.instances.scroll:'
/**
 * 树表统一行模型（FR-452）：分组头行与成员实例行同属一个虚拟化列表，
 * 避免「每组一个虚拟表」的分片表头与滚动错位。
 * - group：分组头（可折叠），带成员计数与聚合健康色带；`depth` 区分 region 两级（0=大区 / 1=小区）。
 * - instance：成员实例行（沿用既有 `renderRow`）。
 */
type InstanceTreeRow =
  | {
      kind: 'group'
      key: string
      /** 折叠态在 URL 中的稳定键（组头行自身用 key 渲染，与 collapseKey 解耦）。 */
      collapseKey: string
      label: string
      /** 层级深度（0 起）；region 两级为 0/1，groupTree 为分组树实际深度。 */
      depth: number
      count: number
      health: MemberHealth
      collapsed: boolean
    }
  | { kind: 'instance'; key: string; instance: InstanceInfo }

/**
 * 由分组结果 + 维度 + 折叠态构建树表行（FR-452）。
 * - region 维度：大区头（depth 0）→ 小区头（depth 1）→ 成员；折叠大区隐藏其全部小区。
 * - groupTree 维度：多级——组头（depth=树深度）→ 本组**直接**成员行 → 递归子组头 → 其成员；
 *   折叠某组隐藏其后代（子组头与全部成员行）。键为组 id，同名不同组各自成组。
 * - 其余维度：单级分组头 → 成员；`none` 维度直接平铺（无分组头）。
 * 未分组（空 key）由聚合函数保证排在末尾，此处不额外排序。
 */
export function buildInstanceTreeRows(
  groups: InstanceGroup[],
  dim: GroupDimension,
  labelOf: (key: string, level: number) => string,
  collapsed: Set<string>,
): InstanceTreeRow[] {
  if (dim === 'none') {
    return groups.flatMap((g) =>
      g.instances.map((instance) => ({ kind: 'instance' as const, key: `instance:${instance.id}`, instance })),
    )
  }
  const rows: InstanceTreeRow[] = []
  if (dim === 'region') {
    for (const region of groups) {
      const regionKey = `region:${region.key}`
      const regionCollapsed = collapsed.has(regionKey)
      rows.push({
        kind: 'group',
        key: `group:${regionKey}`,
        collapseKey: regionKey,
        label: labelOf(region.key, 0),
        depth: 0,
        count: region.instances.length,
        health: memberHealth(region.instances),
        collapsed: regionCollapsed,
      })
      if (regionCollapsed) continue
      const zones = region.children ?? [{ key: '', instances: region.instances }]
      for (const zone of zones) {
        const zoneKey = `region:${region.key}/zone:${zone.key}`
        const zoneCollapsed = collapsed.has(zoneKey)
        rows.push({
          kind: 'group',
          key: `group:${zoneKey}`,
          collapseKey: zoneKey,
          label: labelOf(zone.key, 1),
          depth: 1,
          count: zone.instances.length,
          health: memberHealth(zone.instances),
          collapsed: zoneCollapsed,
        })
        if (zoneCollapsed) continue
        for (const instance of zone.instances) {
          rows.push({ kind: 'instance', key: `instance:${instance.id}`, instance })
        }
      }
    }
    return rows
  }
  if (dim === 'groupTree') {
    // 多级层级展开：组头 → 直接成员 → 子组（递归）；折叠即隐藏整棵子树。
    const walk = (list: InstanceGroup[], depth: number) => {
      for (const group of list) {
        const collapseKey = `${dim}:${group.key}`
        const groupCollapsed = collapsed.has(collapseKey)
        rows.push({
          kind: 'group',
          key: `group:${collapseKey}`,
          collapseKey,
          label: labelOf(group.key, depth),
          depth,
          count: group.instances.length,
          health: memberHealth(group.instances),
          collapsed: groupCollapsed,
        })
        if (groupCollapsed) continue
        for (const instance of group.direct ?? group.instances) {
          rows.push({ kind: 'instance', key: `instance:${instance.id}`, instance })
        }
        if (group.children && group.children.length > 0) walk(group.children, depth + 1)
      }
    }
    walk(groups, 0)
    return rows
  }
  for (const group of groups) {
    const collapseKey = `${dim}:${group.key}`
    const groupCollapsed = collapsed.has(collapseKey)
    rows.push({
      kind: 'group',
      key: `group:${collapseKey}`,
      collapseKey,
      label: labelOf(group.key, 0),
      depth: 0,
      count: group.instances.length,
      health: memberHealth(group.instances),
      collapsed: groupCollapsed,
    })
    if (groupCollapsed) continue
    for (const instance of group.instances) {
      rows.push({ kind: 'instance', key: `instance:${instance.id}`, instance })
    }
  }
  return rows
}

/** 分组头行的聚合健康色带（运行/过渡/崩溃/停止分段；FR-452 验收）。 */
function GroupHealthBand({ health }: { health: MemberHealth }) {
  const { t } = useTranslation()
  const segs: { value: number; className: string; label: string }[] = [
    { value: health.running, className: 'bg-status-success', label: t('networks.healthRunning') },
    { value: health.transitioning, className: 'bg-status-warning', label: t('networks.healthTransitioning') },
    { value: health.crashed, className: 'bg-status-danger', label: t('networks.healthCrashed') },
    { value: health.stopped, className: 'bg-muted-foreground/40', label: t('networks.healthStopped') },
  ]
  const summary = t('grouping.healthBand', {
    running: health.running,
    transitioning: health.transitioning,
    crashed: health.crashed,
    stopped: health.stopped,
  })
  return (
    <span
      className="flex h-1.5 w-16 shrink-0 overflow-hidden rounded-full bg-muted"
      role="img"
      aria-label={summary}
      title={summary}
      data-testid="instances-group-health"
    >
      {segs.map((s, i) =>
        s.value > 0 ? (
          <span
            key={i}
            className={s.className}
            style={{ width: `${(s.value / Math.max(health.total, 1)) * 100}%` }}
          />
        ) : null,
      )}
    </span>
  )
}

/**
 * 实例分组树表（FR-452）：单个虚拟表承载「分组头行（可折叠）+ 成员实例行」。
 * 分组头行显示折叠箭头 + 分组名 + 成员计数 + 聚合健康色带；折叠态由父级写回 URL（`?collapsed=`）。
 */
export function VirtualizedGroupedInstanceTable({
  rows,
  totalCount,
  loadedCount,
  onNeedMore,
  onToggleCollapse,
  scrollStorageKey,
  header,
  renderRow,
  emptyLabel,
}: {
  rows: InstanceTreeRow[]
  totalCount: number
  loadedCount: number
  onNeedMore: () => void
  onToggleCollapse: (key: string) => void
  scrollStorageKey: string
  header: React.ReactNode
  renderRow: (inst: InstanceInfo) => React.ReactNode
  emptyLabel: string
}) {
  const { t } = useTranslation()
  const {
    containerRef,
    onScroll,
    range,
  } = useVirtualRows({
    total: rows.length,
    itemSize: 44,
    overscan: 10,
    fallbackViewportSize: 520,
  })
  const handleScroll = useStoredVirtualScroll(containerRef, onScroll, scrollStorageKey)

  useEffect(() => {
    if (range.end + 20 >= rows.length && loadedCount < totalCount) {
      onNeedMore()
    }
  }, [loadedCount, onNeedMore, range.end, rows.length, totalCount])

  return (
    <div
      ref={containerRef}
      onScroll={handleScroll}
      data-testid="instances-table-virtual"
      data-total-count={totalCount}
      className="max-h-[calc(100vh-20rem)] min-h-72 overflow-auto rounded-lg border bg-card/95 shadow-soft"
    >
      <Table>
        {header}
        <TableBody>
          {range.before > 0 && (
            <TableRow aria-hidden="true">
              <TableCell colSpan={8} className="p-0" style={{ height: range.before }} />
            </TableRow>
          )}
          {rows.slice(range.start, range.end).map((row) => row.kind === 'group' ? (
            <TableRow
              key={row.key}
              data-testid="instances-group-row"
              data-group-depth={row.depth}
              data-collapsed={row.collapsed ? 'true' : 'false'}
              className="sticky top-9 z-10 bg-muted/80 backdrop-blur"
            >
              <TableCell colSpan={8} className="h-11 px-4 py-2">
                <div className="flex items-center gap-2" style={{ paddingLeft: row.depth * 16 }}>
                  <button
                    type="button"
                    onClick={() => onToggleCollapse(row.collapseKey)}
                    aria-expanded={!row.collapsed}
                    aria-label={
                      row.collapsed
                        ? t('grouping.expandGroup', { name: row.label })
                        : t('grouping.collapseGroup', { name: row.label })
                    }
                    data-testid="instances-group-toggle"
                    className="inline-flex size-5 shrink-0 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-accent/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                  >
                    {row.collapsed ? <ChevronRight className="size-3.5" /> : <ChevronDown className="size-3.5" />}
                  </button>
                  <span className="text-sm font-medium">{row.label}</span>
                  <Badge variant="outline" className="font-normal">{row.count}</Badge>
                  <GroupHealthBand health={row.health} />
                </div>
              </TableCell>
            </TableRow>
          ) : (
            <Fragment key={row.key}>{renderRow(row.instance)}</Fragment>
          ))}
          {range.after > 0 && (
            <TableRow aria-hidden="true">
              <TableCell colSpan={8} className="p-0" style={{ height: range.after }} />
            </TableRow>
          )}
          {rows.length === 0 && (
            <TableRow>
              <TableCell colSpan={8} className="text-center text-muted-foreground">
                {emptyLabel}
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
    </div>
  )
}

export function VirtualizedInstanceTable({
  instances,
  totalCount,
  onNeedMore,
  scrollStorageKey,
  header,
  renderRow,
  emptyLabel,
}: {
  instances: InstanceInfo[]
  totalCount: number
  onNeedMore: () => void
  scrollStorageKey: string
  header: React.ReactNode
  renderRow: (inst: InstanceInfo) => React.ReactNode
  emptyLabel: string
}) {
  const {
    containerRef,
    onScroll,
    range,
  } = useVirtualRows({
    total: totalCount,
    itemSize: 44,
    overscan: 10,
    fallbackViewportSize: 520,
  })
  const handleScroll = useStoredVirtualScroll(containerRef, onScroll, scrollStorageKey)

  useEffect(() => {
    if (range.end + 20 >= instances.length && instances.length < totalCount) {
      onNeedMore()
    }
  }, [instances.length, onNeedMore, range.end, totalCount])

  const visible = []
  for (let index = range.start; index < range.end; index++) {
    visible.push({ index, inst: instances[index] })
  }

  return (
    <div
      ref={containerRef}
      onScroll={handleScroll}
      data-testid="instances-table-virtual"
      data-total-count={totalCount}
      className="max-h-[calc(100vh-20rem)] min-h-72 overflow-auto rounded-lg border bg-card/95 shadow-soft"
    >
      <Table>
        {header}
        <TableBody>
          {range.before > 0 && (
            <TableRow aria-hidden="true">
              <TableCell colSpan={8} className="p-0" style={{ height: range.before }} />
            </TableRow>
          )}
          {visible.map(({ index, inst }) => (
            <Fragment key={inst?.id ?? `placeholder-${index}`}>
              {inst ? renderRow(inst) : (
                <TableRow aria-hidden="true">
                  <TableCell colSpan={8} className="p-0" style={{ height: 44 }} />
                </TableRow>
              )}
            </Fragment>
          ))}
          {range.after > 0 && (
            <TableRow aria-hidden="true">
              <TableCell colSpan={8} className="p-0" style={{ height: range.after }} />
            </TableRow>
          )}
          {totalCount === 0 && (
            <TableRow>
              <TableCell colSpan={8} className="text-center text-muted-foreground">
                {emptyLabel}
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
    </div>
  )
}

export function useStoredVirtualScroll(
  containerRef: React.RefObject<HTMLDivElement | null>,
  onScroll: () => void,
  storageKey: string,
) {
  const clearZeroTimerRef = useRef<number | null>(null)

  const cancelClearZeroTimer = useCallback(() => {
    if (clearZeroTimerRef.current == null) return
    window.clearTimeout(clearZeroTimerRef.current)
    clearZeroTimerRef.current = null
  }, [])

  useLayoutEffect(() => {
    const saved = Number(sessionStorage.getItem(storageKey) ?? 0)
    if (!Number.isFinite(saved) || saved <= 0) return

    let cancelled = false
    let attempts = 0
    let timer: number | null = null
    const restore = () => {
      if (cancelled) return
      const el = containerRef.current
      attempts += 1
      if (!el) {
        if (attempts < 20) timer = window.setTimeout(restore, 50)
        return
      }
      el.scrollTop = saved
      onScroll()
      if (el.scrollTop < saved && attempts < 20) timer = window.setTimeout(restore, 50)
    }

    timer = window.setTimeout(restore, 0)
    return () => {
      cancelled = true
      if (timer != null) window.clearTimeout(timer)
    }
  }, [containerRef, onScroll, storageKey])

  useEffect(() => cancelClearZeroTimer, [cancelClearZeroTimer])

  return useCallback(() => {
    onScroll()
    const el = containerRef.current
    if (!el) return

    const scrollTop = Math.round(el.scrollTop)
    if (scrollTop <= 0) {
      cancelClearZeroTimer()
      clearZeroTimerRef.current = window.setTimeout(() => {
        const currentKey = `${SCROLL_KEY_PREFIX}${window.location.pathname}${window.location.search}`
        if (currentKey === storageKey) sessionStorage.removeItem(storageKey)
        clearZeroTimerRef.current = null
      }, 150)
      return
    }

    cancelClearZeroTimer()
    sessionStorage.setItem(storageKey, String(scrollTop))
  }, [cancelClearZeroTimer, containerRef, onScroll, storageKey])
}
