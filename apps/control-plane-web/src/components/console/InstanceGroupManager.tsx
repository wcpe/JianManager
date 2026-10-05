import { Fragment, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { ChevronRight, FolderTree, Tag } from 'lucide-react'
import { toast } from 'sonner'
import { useInstances, type InstanceInfo } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import {
  useInstanceGroups,
  useInstanceGroupSubtree,
  useAddInstanceGroupMembers,
  useRemoveInstanceGroupMembers,
  type InstanceGroupNode,
} from '@/api/instanceGroups'
import { InstanceWorktableCard } from './InstanceWorktableCard'
import { InstanceGroupTree, INSTANCE_DND_MIME } from './InstanceGroupTree'
import { groupPathOf } from './instance-group-path'
import { Panel } from '@jianmanager/ui/components/panel'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { cn } from '@jianmanager/ui'
import { useVirtualRows } from '@jianmanager/ui/lib/virtual-list'
import { useCardColumns } from '@/lib/use-card-columns'

/** 卡片行高（px）：与 InstancesPage 的卡片网格同源。虚拟化按「行」定位，须与真实渲染高度一致。 */
const CARD_ROW_HEIGHT = 244

/**
 * 本次要渲染哪些行：窗口内的行，外加「拖拽中」那一行（若它已在窗口外）。
 *
 * 被拖行必须始终留在 DOM 里：原生 HTML5 拖拽期间若把源元素卸载，浏览器会直接取消拖拽
 * 且不触发 `dragend`。而拖拽时用户常会滚动去找目标组，源行滑出窗口被回收是常态。
 */
function renderRowIndexes(range: { start: number; end: number }, draggingRowIndex: number): number[] {
  const indexes: number[] = []
  for (let r = range.start; r < range.end; r++) indexes.push(r)
  if (draggingRowIndex >= 0 && !indexes.includes(draggingRowIndex)) indexes.push(draggingRowIndex)
  return indexes
}

/**
 * 实例多级分组完整视图（FR-165，design §4.4）：左 = 分组树，右 = 选中组（含子树）的实例列表。
 * 右列表复用工作台卡骨架（§4.5），头部为组路径面包屑 + 批量「标记入组」；实例卡可拖入左树某组。
 * 与 InstancesPage 既有筛选/分组视图正交并列——本视图是「按组织归类浏览」的专用形态。
 */
export function InstanceGroupManager() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [selectedGroupId, setSelectedGroupId] = useState<number | null>(null)
  const [selectedIds, setSelectedIds] = useState<number[]>([])

  const { data: allInstances } = useInstances()
  const { data: nodes } = useNodes()
  const { data: groups } = useInstanceGroups()
  // 选中组时取其子树（含后代、去重）的实例 ID 集合；未选时不查（显示全部）。
  const { data: subtreeIds } = useInstanceGroupSubtree(selectedGroupId)

  const nodeName = (id: number) => nodes?.find((n) => n.id === id)?.name ?? t('console.unknownNode', { id })

  // 右列表数据：未选组=全部实例；选中组=子树实例集合过滤。
  const visible = useMemo<InstanceInfo[]>(() => {
    const list = allInstances ?? []
    if (selectedGroupId === null) return list
    const idSet = new Set(subtreeIds ?? [])
    return list.filter((i) => idSet.has(i.id))
  }, [allInstances, selectedGroupId, subtreeIds])

  /**
   * 右列表虚拟化。实例数是千级（大档 1200），此前整表铺进 DOM——而每张卡还各自挂了
   * 3 个 mutation hook 与 `useInstanceMetrics`（运行态轮询），单卡成本远高于普通表格行。
   * 虚拟化把入 DOM 的卡片数从 1200 降到「视口行数 × 列数」，并顺带消掉这批轮询。
   */
  const columns = useCardColumns()
  const rowCount = Math.ceil(visible.length / columns)
  const {
    containerRef: gridScrollRef,
    onScroll: onGridScroll,
    range: gridRange,
    totalSize: gridTotalSize,
  } = useVirtualRows({ total: rowCount, itemSize: CARD_ROW_HEIGHT, overscan: 4, fallbackViewportSize: 720 })

  // 拖拽钉行：正在拖的那一行必须留在 DOM 里，理由见 renderRowIndexes 的说明。
  const [draggingId, setDraggingId] = useState<number | null>(null)
  const draggingRowIndex = useMemo(() => {
    if (draggingId === null) return -1
    const idx = visible.findIndex((i) => i.id === draggingId)
    return idx < 0 ? -1 : Math.floor(idx / columns)
  }, [draggingId, visible, columns])

  const breadcrumb = useMemo(
    () => (selectedGroupId === null ? [] : groupPathOf(groups ?? [], selectedGroupId)),
    [groups, selectedGroupId],
  )

  const toggleOne = (id: number) =>
    setSelectedIds((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]))
  const clearSelection = () => setSelectedIds([])

  return (
    <div className="grid grid-cols-1 gap-3 lg:grid-cols-[18rem_1fr]">
      <Panel className="lg:h-[calc(100vh-16rem)]" bodyClassName="min-h-0 p-2">
        <InstanceGroupTree selectedGroupId={selectedGroupId} onSelect={setSelectedGroupId} />
      </Panel>

      <Panel className="lg:h-[calc(100vh-16rem)]" bodyClassName="flex min-h-0 flex-col p-3">
        {/* 组路径面包屑 */}
        <div className="mb-3 flex shrink-0 flex-wrap items-center gap-1 text-sm">
          <FolderTree className="size-4 text-primary" />
          {selectedGroupId === null ? (
            <span className="font-medium">{t('instanceGroups.allInstances')}</span>
          ) : (
            breadcrumb.map((seg, i) => (
              <Fragment key={seg.id}>
                {i > 0 && <ChevronRight className="size-3.5 text-muted-foreground" />}
                <span className={cn(i === breadcrumb.length - 1 && 'font-medium')}>{seg.name}</span>
              </Fragment>
            ))
          )}
          <Badge variant="outline" className="ml-1 font-normal">
            {visible.length}
          </Badge>
        </div>

        {/* 批量「标记入组」栏：选中实例后出现 */}
        {selectedIds.length > 0 && (
          <div className="shrink-0">
            <MarkIntoGroupBar
              selectedIds={selectedIds}
              selectedGroupId={selectedGroupId}
              groups={groups ?? []}
              onDone={clearSelection}
            />
          </div>
        )}

        {visible.length === 0 ? (
          <p className="py-10 text-center text-sm text-muted-foreground">
            {selectedGroupId === null ? t('instances.empty') : t('instanceGroups.groupEmpty')}
          </p>
        ) : (
          <div
            ref={gridScrollRef}
            onScroll={onGridScroll}
            className="min-h-0 flex-1 overflow-y-auto"
            data-testid="instance-group-grid"
            data-total-count={visible.length}
          >
            {/* 外层撑起总高，每行绝对定位——虚拟化把入 DOM 的卡片数压到视口量级 */}
            <div className="relative" style={{ height: gridTotalSize }}>
              {renderRowIndexes(gridRange, draggingRowIndex).map((rowIndex) => (
                <div
                  key={rowIndex}
                  className="absolute inset-x-0 grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3"
                  style={{ top: rowIndex * CARD_ROW_HEIGHT, minHeight: CARD_ROW_HEIGHT - 12 }}
                >
                  {visible.slice(rowIndex * columns, rowIndex * columns + columns).map((inst) => (
                    <DraggableInstance
                      key={inst.id}
                      inst={inst}
                      selected={selectedIds.includes(inst.id)}
                      onToggle={() => toggleOne(inst.id)}
                      selectedIds={selectedIds}
                      onDragStateChange={setDraggingId}
                    >
                      <InstanceWorktableCard
                        inst={inst}
                        nodeName={nodeName(inst.nodeId)}
                        roleBadge={null}
                        menu={null}
                        onOpen={(id) => navigate(`/instances/${id}`)}
                      />
                    </DraggableInstance>
                  ))}
                </div>
              ))}
            </div>
          </div>
        )}
      </Panel>
    </div>
  )
}

/**
 * 可拖拽 + 可多选的实例卡包装。
 * 左上角复选框做批量选中；整块可拖入左树某组——拖拽载荷为「当前选中集合（含本卡）」的实例 ID 数组。
 */
function DraggableInstance({
  inst,
  selected,
  onToggle,
  selectedIds,
  onDragStateChange,
  children,
}: {
  inst: InstanceInfo
  selected: boolean
  onToggle: () => void
  selectedIds: number[]
  /** 拖拽起止回调：父级据此把被拖行钉在渲染集合里，避免滚动时源元素被回收而中断拖拽。 */
  onDragStateChange?: (id: number | null) => void
  children: React.ReactNode
}) {
  const { t } = useTranslation()
  return (
    <div
      className={cn('relative rounded-xl', selected && 'ring-2 ring-primary ring-offset-1 ring-offset-background')}
      draggable
      onDragStart={(e) => {
        // 拖选中集合；若本卡未在选中集合内，则只拖本卡。
        const ids = selected && selectedIds.length > 0 ? selectedIds : [inst.id]
        e.dataTransfer.setData(INSTANCE_DND_MIME, JSON.stringify(ids))
        e.dataTransfer.effectAllowed = 'copy'
        onDragStateChange?.(inst.id)
      }}
      onDragEnd={() => onDragStateChange?.(null)}
    >
      <div className="absolute left-2 top-2 z-10">
        <Checkbox checked={selected} onCheckedChange={onToggle} aria-label={t('instanceGroups.selectInstance', { name: inst.name })} />
      </div>
      {children}
    </div>
  )
}

/** 批量「标记入组」栏：选一个目标组，把已选实例批量加入（幂等）。 */
function MarkIntoGroupBar({
  selectedIds,
  selectedGroupId,
  groups,
  onDone,
}: {
  selectedIds: number[]
  selectedGroupId: number | null
  groups: InstanceGroupNode[]
  onDone: () => void
}) {
  const { t } = useTranslation()
  const [target, setTarget] = useState<string>('')
  const addMembers = useAddInstanceGroupMembers()
  const removeMembers = useRemoveInstanceGroupMembers()

  const submit = () => {
    if (!target) return
    addMembers.mutate(
      { id: Number(target), instanceIds: selectedIds },
      {
        onSuccess: (res) => {
          toast.success(t('instanceGroups.markedCount', { count: res.added }))
          onDone()
        },
        onError: () => toast.error(t('instanceGroups.markFailed')),
      },
    )
  }

  const removeFromCurrent = () => {
    if (selectedGroupId === null) return
    removeMembers.mutate(
      { id: selectedGroupId, instanceIds: selectedIds },
      {
        onSuccess: () => {
          toast.success(t('instanceGroups.removedCount', { count: selectedIds.length }))
          onDone()
        },
        onError: () => toast.error(t('instanceGroups.removeFailed')),
      },
    )
  }

  return (
    <div className="mb-3 flex flex-wrap items-center gap-2 rounded-lg border bg-accent/40 px-3 py-2">
      <Tag className="size-4 text-primary" />
      <span className="text-sm font-medium">{t('instanceGroups.selectedCount', { count: selectedIds.length })}</span>
      <div className="ml-auto flex items-center gap-2">
        <Select value={target} onValueChange={setTarget}>
          <SelectTrigger size="sm" className="w-44">
            <SelectValue placeholder={t('instanceGroups.pickTargetGroup')} />
          </SelectTrigger>
          <SelectContent>
            {groups.map((g) => (
              <SelectItem key={g.id} value={String(g.id)}>
                {g.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button size="sm" onClick={submit} disabled={!target || addMembers.isPending}>
          {t('instanceGroups.markInto')}
        </Button>
        {selectedGroupId !== null && (
          <Button
            size="sm"
            variant="outline"
            className="text-destructive hover:text-destructive"
            title={t('instanceGroups.removeFromCurrentHint')}
            onClick={removeFromCurrent}
            disabled={removeMembers.isPending}
          >
            {t('instanceGroups.removeFromCurrent')}
          </Button>
        )}
        <Button size="sm" variant="ghost" onClick={onDone}>
          {t('common.cancel')}
        </Button>
      </div>
    </div>
  )
}
