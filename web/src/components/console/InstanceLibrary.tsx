import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ChevronRight, GripVertical, PanelLeftClose, PanelLeftOpen, Search } from 'lucide-react'
import { useInstances, type InstanceInfo } from '@/api/instances'
import { cn } from '@/lib/utils'
import { CARD_TYPES, cardTypeDef, type CardType } from '@/lib/workspace-card'
import { WORKSPACE_DND_MIME, encodeDragPayload, type DragPayload } from '@/lib/instance-library'
import InstanceStatusDot from './InstanceStatusDot'

/**
 * 超级工作台「实例库」面板（FR-167 / design §9）：左侧可收起，HTML5 原生 DnD 拖拽源。
 *
 * - 搜索过滤实例（按名/状态/角色）。
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

export default function InstanceLibrary({ collapsed, onToggleCollapsed }: InstanceLibraryProps) {
  const { t } = useTranslation()
  const { data: instances, isLoading } = useInstances()
  const [query, setQuery] = useState('')
  const [expanded, setExpanded] = useState<Set<number>>(new Set())
  const [selected, setSelected] = useState<Set<number>>(new Set())

  const filtered = useMemo<InstanceInfo[]>(() => {
    const list = instances ?? []
    const q = query.trim().toLowerCase()
    if (!q) return list
    return list.filter(
      (i) => i.name.toLowerCase().includes(q) || i.status.toLowerCase().includes(q) || i.role.toLowerCase().includes(q),
    )
  }, [instances, query])

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
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t('superWorkbench.searchInstance')}
            className="h-8 w-full rounded-md border bg-background pl-7 pr-2 text-sm outline-none ring-primary/30 focus:ring-2"
          />
        </div>
        {selected.size > 0 && (
          <p className="mt-1.5 text-[11px] text-muted-foreground">
            {t('superWorkbench.selectedHint', { count: selected.size })}
          </p>
        )}
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto scrollbar-none px-2 pb-2">
        {isLoading ? (
          <p className="px-2 py-2 text-xs text-muted-foreground">{t('common.loading')}</p>
        ) : filtered.length === 0 ? (
          <p className="px-2 py-2 text-xs text-muted-foreground">{t('console.noInstances')}</p>
        ) : (
          <ul className="space-y-0.5">
            {filtered.map((inst) => (
              <InstanceLibraryRow
                key={inst.id}
                instance={inst}
                expanded={expanded.has(inst.id)}
                selected={selected.has(inst.id)}
                selectedIds={selected}
                onToggleExpand={() => toggleExpand(inst.id)}
                onToggleSelect={() => toggleSelect(inst.id)}
              />
            ))}
          </ul>
        )}
      </div>
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
 * 展开后每个功能各为一个拖拽源（拖功能=单卡）。
 */
function InstanceLibraryRow({
  instance,
  expanded,
  selected,
  selectedIds,
  onToggleExpand,
  onToggleSelect,
}: {
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
    <li>
      <div
        draggable
        onDragStart={handleInstanceDragStart}
        className={cn(
          'group flex cursor-grab items-center gap-1.5 rounded-md px-1.5 py-1.5 text-sm active:cursor-grabbing',
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
          className="grid size-5 shrink-0 place-items-center rounded text-muted-foreground hover:text-foreground"
        >
          {expanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
        </button>
        <InstanceStatusDot status={instance.status} />
        <span className="min-w-0 flex-1 truncate">{instance.name}</span>
        <GripVertical className="size-3.5 shrink-0 text-muted-foreground/40 group-hover:text-muted-foreground" />
      </div>

      {expanded && (
        <ul className="mb-1 ml-7 space-y-0.5 border-l pl-2">
          {CARD_TYPES.map((def) => (
            <FunctionDragItem key={def.type} instanceId={instance.id} type={def.type} />
          ))}
        </ul>
      )}
    </li>
  )
}

/** 单功能拖拽源：拖到画布 = 加该实例该类型单卡。 */
function FunctionDragItem({ instanceId, type }: { instanceId: number; type: CardType }) {
  const { t } = useTranslation()
  const def = cardTypeDef(type)!
  return (
    <li
      draggable
      onDragStart={(e) => setDragData(e, { kind: 'card', instanceId, cardType: type })}
      className="flex cursor-grab items-center gap-1.5 rounded px-1.5 py-1 text-xs text-muted-foreground hover:bg-accent/50 hover:text-foreground active:cursor-grabbing"
    >
      <GripVertical className="size-3 shrink-0 opacity-40" />
      <span className="truncate">{t(def.titleKey)}</span>
    </li>
  )
}
