import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ChevronRight, GripVertical, PanelLeftClose, PanelLeftOpen, Search } from 'lucide-react'
import { useInfiniteInstanceSearch, type InstanceInfo } from '@/api/instances'
import { cn } from '@jianmanager/ui'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { useVirtualRows } from '@/lib/virtual-list'
import { useDebounced } from '@/lib/use-debounced'
import { CARD_TYPES, cardTypeDef, type CardType } from '@/lib/workspace-card'
import { WORKSPACE_DND_MIME, encodeDragPayload, type DragPayload } from '@/lib/instance-library'
import InstanceStatusDot from './InstanceStatusDot'

/**
 * 超级工作台「实例库」面板（FR-167 / design §9）：左侧可收起，HTML5 原生 DnD 拖拽源。
 *
 * - 搜索走服务端（`/instances/search` 的 `q`，输入防抖），滚动到底按页补齐（FR-235 范式），
 *   避免 1000+ 实例时全量拉取 + 全量挂 DOM。
 * - 列表用 `lib/virtual-list` 虚拟化：把「实例行」与其展开的「功能子项」拍平成定高行流，
 *   只渲染可视窗口内的行（DnD 逐行独立、多选凭已选 id 集合，不依赖全量 DOM 挂载）。
 * - 实例可展开看 6+ 功能（复用 `workspace-card` 目录）。
 * - **拖实例** = 加该实例默认卡组；**拖功能** = 加单卡；**多选批量拖** = 一次拼监看墙。
 * - 多选用复选框（点行切换选中），选中后拖任一选中实例即批量拖。
 *
 * 仅产出拖拽载荷（`lib/instance-library` 纯逻辑序列化）；落位/去重在 store `dropToSuper`。
 */
interface InstanceLibraryProps {
  /** 折叠态（仅图标轨）。 */
  collapsed: boolean
  /** 切换折叠。 */
  onToggleCollapsed: () => void
}

/** 拍平后的库行：实例行本身，或某展开实例下的单功能子项。定高虚拟化用。 */
type LibraryRow =
  | { kind: 'instance'; key: string; instance: InstanceInfo }
  | { kind: 'function'; key: string; instanceId: number; type: CardType }

/** 虚拟化行高（含实例行与功能子项行统一定高，px）。 */
const LIBRARY_ROW_HEIGHT = 34

/** 搜索输入防抖（毫秒）：停止输入后才下发服务端 q。 */
const SEARCH_DEBOUNCE_MS = 250

export default function InstanceLibrary({ collapsed, onToggleCollapsed }: InstanceLibraryProps) {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const debouncedQuery = useDebounced(query.trim(), SEARCH_DEBOUNCE_MS)
  const searchParams = useMemo(
    () => ({ ...(debouncedQuery ? { q: debouncedQuery } : {}), pageSize: 50, sort: 'name' as const, order: 'asc' as const }),
    [debouncedQuery],
  )
  const {
    data: searchData,
    isLoading,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
  } = useInfiniteInstanceSearch(searchParams)
  const instances = useMemo<InstanceInfo[]>(
    () => searchData?.pages.flatMap((p) => p.items) ?? [],
    [searchData],
  )
  const totalCount = searchData?.pages[0]?.total ?? instances.length

  const [expanded, setExpanded] = useState<Set<number>>(new Set())
  const [selected, setSelected] = useState<Set<number>>(new Set())

  const toggleExpand = (id: number) =>
    setExpanded((s) => {
      const next = new Set(s)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  const toggleSelect = (id: number) =>
    setSelected((s) => {
      const next = new Set(s)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  // 实例行 + 展开的功能子项拍平成定高行流（虚拟化前提）。
  const rows = useMemo<LibraryRow[]>(
    () =>
      instances.flatMap((inst) => {
        const base: LibraryRow = { kind: 'instance', key: `i:${inst.id}`, instance: inst }
        if (!expanded.has(inst.id)) return [base]
        return [
          base,
          ...CARD_TYPES.map<LibraryRow>((def) => ({
            kind: 'function',
            key: `f:${inst.id}:${def.type}`,
            instanceId: inst.id,
            type: def.type,
          })),
        ]
      }),
    [instances, expanded],
  )

  const { containerRef, onScroll, range, totalSize } = useVirtualRows({
    total: rows.length,
    itemSize: LIBRARY_ROW_HEIGHT,
    overscan: 8,
    fallbackViewportSize: 480,
  })

  // 滚动接近底部时按页补齐（FR-235 范式）。
  useEffect(() => {
    if (range.end + 12 >= rows.length && hasNextPage && !isFetchingNextPage) {
      void fetchNextPage()
    }
  }, [fetchNextPage, hasNextPage, isFetchingNextPage, range.end, rows.length])

  if (collapsed) {
    return (
      <div className="flex w-12 shrink-0 flex-col items-center border-r bg-card/40 py-2">
        <button
          type="button"
          onClick={onToggleCollapsed}
          aria-label={t('superWorkbench.expandLibrary')}
          title={t('superWorkbench.expandLibrary')}
          className="grid size-8 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
        >
          <PanelLeftOpen className="size-4" />
        </button>
      </div>
    )
  }

  const visible = rows.slice(range.start, range.end)

  return (
    <div className="flex w-64 shrink-0 flex-col border-r bg-card/40">
      <div className="flex shrink-0 items-center gap-2 border-b px-3 py-2">
        <span className="flex-1 truncate text-sm font-semibold">{t('superWorkbench.libraryTitle')}</span>
        <button
          type="button"
          onClick={onToggleCollapsed}
          aria-label={t('superWorkbench.collapseLibrary')}
          title={t('superWorkbench.collapseLibrary')}
          className="grid size-6 place-items-center rounded text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
        >
          <PanelLeftClose className="size-4" />
        </button>
      </div>

      <div className="shrink-0 px-3 py-2">
        <div className="relative">
          <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t('superWorkbench.searchInstance')}
            aria-label={t('superWorkbench.searchInstance')}
            className="h-8 w-full rounded-md border bg-background pl-7 pr-2 text-sm outline-none ring-primary/30 focus:ring-2"
          />
        </div>
        {selected.size > 0 && (
          <p className="mt-1.5 text-[11px] text-muted-foreground">
            {t('superWorkbench.selectedHint', { count: selected.size })}
          </p>
        )}
      </div>

      {isLoading ? (
        // 骨架行占位（替代裸文字），避免加载态布局跳动。
        <div className="min-h-0 flex-1 space-y-1 px-2 pb-2" data-testid="instance-library-skeleton">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-7 w-full" />
          ))}
        </div>
      ) : rows.length === 0 ? (
        <p className="min-h-0 flex-1 px-2 py-2 text-xs text-muted-foreground">{t('console.noInstances')}</p>
      ) : (
        <div
          ref={containerRef}
          onScroll={onScroll}
          data-testid="instance-library-virtual"
          data-total-count={totalCount}
          className="min-h-0 flex-1 overflow-y-auto scrollbar-none px-2 pb-2"
        >
          <ul className="relative" style={{ height: totalSize }}>
            {visible.map((row, i) => {
              const top = (range.start + i) * LIBRARY_ROW_HEIGHT
              if (row.kind === 'instance') {
                return (
                  <InstanceLibraryRow
                    key={row.key}
                    top={top}
                    instance={row.instance}
                    expanded={expanded.has(row.instance.id)}
                    selected={selected.has(row.instance.id)}
                    selectedIds={selected}
                    onToggleExpand={() => toggleExpand(row.instance.id)}
                    onToggleSelect={() => toggleSelect(row.instance.id)}
                  />
                )
              }
              return <FunctionDragItem key={row.key} top={top} instanceId={row.instanceId} type={row.type} />
            })}
          </ul>
        </div>
      )}
    </div>
  )
}

/** 给一个 DOM dragstart 写入工作区载荷（统一 MIME + 文本兜底）。 */
function setDragData(e: React.DragEvent, payload: DragPayload): void {
  const data = encodeDragPayload(payload)
  e.dataTransfer.setData(WORKSPACE_DND_MIME, data)
  e.dataTransfer.setData('text/plain', data)
  e.dataTransfer.effectAllowed = 'copy'
}

/**
 * 实例行：可展开看功能；行本身是拖拽源（拖实例=默认卡组，若已多选则批量拖）。
 * 虚拟化定位：绝对定位到 `top`，定高 {@link LIBRARY_ROW_HEIGHT}。
 */
function InstanceLibraryRow({
  top,
  instance,
  expanded,
  selected,
  selectedIds,
  onToggleExpand,
  onToggleSelect,
}: {
  top: number
  instance: InstanceInfo
  expanded: boolean
  selected: boolean
  selectedIds: Set<number>
  onToggleExpand: () => void
  onToggleSelect: () => void
}) {
  const { t } = useTranslation()

  const handleInstanceDragStart = (e: React.DragEvent) => {
    // 已多选且本行在选中集内 → 批量拖（监看墙）；否则拖单实例默认卡组。
    if (selectedIds.size > 1 && selectedIds.has(instance.id)) {
      setDragData(e, { kind: 'instances', instanceIds: [...selectedIds] })
    } else {
      setDragData(e, { kind: 'instance', instanceId: instance.id })
    }
  }

  return (
    <li className="absolute inset-x-0" style={{ top, height: LIBRARY_ROW_HEIGHT }}>
      <div
        draggable
        onDragStart={handleInstanceDragStart}
        className={cn(
          'group flex h-full cursor-grab items-center gap-1.5 rounded-md px-1.5 text-sm active:cursor-grabbing',
          selected ? 'bg-primary/10' : 'hover:bg-accent/50',
        )}
      >
        <input
          type="checkbox"
          checked={selected}
          onChange={onToggleSelect}
          onClick={(e) => e.stopPropagation()}
          aria-label={t('superWorkbench.selectInstance', { name: instance.name })}
          className="size-3.5 shrink-0 accent-primary"
        />
        <button
          type="button"
          onClick={onToggleExpand}
          aria-expanded={expanded}
          aria-label={t('superWorkbench.toggleInstanceFunctions', {
            defaultValue: 'Toggle functions for {{name}}',
            name: instance.name,
          })}
          className="grid size-5 shrink-0 place-items-center rounded text-muted-foreground hover:text-foreground"
        >
          {expanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
        </button>
        <InstanceStatusDot status={instance.status} />
        <span className="min-w-0 flex-1 truncate">{instance.name}</span>
        <GripVertical className="size-3.5 shrink-0 text-muted-foreground/40 group-hover:text-muted-foreground" />
      </div>
    </li>
  )
}

/**
 * 单功能拖拽源：拖到画布 = 加该实例该类型单卡。
 * 虚拟化定位：绝对定位到 `top`，定高 {@link LIBRARY_ROW_HEIGHT}。
 */
function FunctionDragItem({ top, instanceId, type }: { top: number; instanceId: number; type: CardType }) {
  const { t } = useTranslation()
  const def = cardTypeDef(type)!
  return (
    <li className="absolute inset-x-0 pl-7 pr-0" style={{ top, height: LIBRARY_ROW_HEIGHT }}>
      <div
        draggable
        onDragStart={(e) => setDragData(e, { kind: 'card', instanceId, cardType: type })}
        className="ml-2 flex h-full cursor-grab items-center gap-1.5 rounded border-l px-1.5 text-xs text-muted-foreground hover:bg-accent/50 hover:text-foreground active:cursor-grabbing"
      >
        <GripVertical className="size-3 shrink-0 opacity-40" />
        <span className="truncate">{t(def.titleKey)}</span>
      </div>
    </li>
  )
}
