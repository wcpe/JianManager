import { Fragment, useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronRight, FolderTree, Tag } from 'lucide-react'
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
import { useVirtualRows } from '@/lib/shared/virtual-list'
import { useCardColumns } from '@/lib/hooks/use-card-columns'
import type { InstanceGroupNode } from '@/lib/instances/instance-group'
import { groupPathOf } from '@/lib/instances/instance-group-path'
import { InstanceWorktableCard } from '@/components/views/instances/InstanceWorktableCard'
import type { InstanceWorktableCardInstanceView } from '@/components/views/instances/InstanceWorktableCard'
import { INSTANCE_DND_MIME } from '@/components/views/instances/InstanceGroupTree'

/** 分组管理页所需的实例视图：卡面字段 + 所属节点 id（用于解析节点名）。 */
export interface GroupManagerInstance extends InstanceWorktableCardInstanceView {
  nodeId: number
}

/** 提示通道：视图算好文案交外壳展示（本包不弹 toast）。 */
export type GroupManagerNotice = (kind: 'success' | 'error', message: string) => void

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
 *
 * 受控视图（ADR-097 b 范式）：不取数、不发请求、不弹 toast —— 实例与分组由外壳注入，
 * 左树以函数插槽交外壳渲染（它自带接线层），打开实例、批量标记/移出经回调上报。
 * 面包屑、右列表虚拟化、拖拽钉行留在视图内。
 */
export interface InstanceGroupManagerProps {
  /** 当前选中组 id；null=未选（显示全部）。 */
  selectedGroupId: number | null
  /** 选中组变化（外壳据此查子树）。 */
  onSelectGroup: (groupId: number | null) => void
  /** 全部实例（未选组时显示全部）。 */
  instances: GroupManagerInstance[]
  /** 分组列表（扁平，用于面包屑与目标组下拉）。 */
  groups: InstanceGroupNode[]
  /** 选中组的子树实例 id；null=未选组（显示全部）。 */
  subtreeIds: number[] | null
  /** 节点名解析（外壳按节点表解析，避免每卡各自查表）。 */
  nodeNameOf: (nodeId: number) => string
  /** 打开实例详情。 */
  onOpenInstance: (id: number) => void
  /**
   * 实例生命周期动作（外壳接 mutation）。卡面上的启停/重启按钮本就在，故必须接上，
   * 否则点了没反应。
   */
  onLifecycle: (action: 'start' | 'stop' | 'restart', instanceId: number) => void
  /** 左树插槽：外壳用 `InstanceGroupTree` 接线层渲染，并把选中态回灌。 */
  tree: (args: { selectedGroupId: number | null; onSelect: (id: number | null) => void }) => ReactNode
  /** 批量标记入组（幂等），返回本次新增数。 */
  onAddMembers: (payload: { groupId: number; instanceIds: number[] }) => Promise<{ added: number }>
  /** 从当前选中组移出。 */
  onRemoveMembers: (payload: { groupId: number; instanceIds: number[] }) => Promise<void>
  /** 提示通道。 */
  notify: GroupManagerNotice
}

export function InstanceGroupManager({
  selectedGroupId,
  onSelectGroup,
  instances,
  groups,
  subtreeIds,
  nodeNameOf,
  onOpenInstance,
  onLifecycle,
  tree,
  onAddMembers,
  onRemoveMembers,
  notify,
}: InstanceGroupManagerProps) {
  const { t } = useTranslation()
  const [selectedIds, setSelectedIds] = useState<number[]>([])

  // 右列表数据：未选组=全部实例；选中组=子树实例集合过滤。
  const visible = useMemo<GroupManagerInstance[]>(() => {
    if (selectedGroupId === null) return instances
    const idSet = new Set(subtreeIds ?? [])
    return instances.filter((i) => idSet.has(i.id))
  }, [instances, selectedGroupId, subtreeIds])

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
    () => (selectedGroupId === null ? [] : groupPathOf(groups, selectedGroupId)),
    [groups, selectedGroupId],
  )

  const toggleOne = (id: number) =>
    setSelectedIds((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]))
  const clearSelection = () => setSelectedIds([])

  return (
    <div className="grid grid-cols-1 gap-3 lg:grid-cols-[18rem_1fr]">
      <Panel className="lg:h-[calc(100vh-16rem)]" bodyClassName="min-h-0 p-2">
        {tree({ selectedGroupId, onSelect: onSelectGroup })}
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
              groups={groups}
              onDone={clearSelection}
              onAddMembers={onAddMembers}
              onRemoveMembers={onRemoveMembers}
              notify={notify}
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
                        nodeName={nodeNameOf(inst.nodeId)}
                        roleBadge={null}
                        menu={null}
                        onOpen={onOpenInstance}
                        onStart={() => onLifecycle('start', inst.id)}
                        onStop={() => onLifecycle('stop', inst.id)}
                        onRestart={() => onLifecycle('restart', inst.id)}
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
  inst: GroupManagerInstance
  selected: boolean
  onToggle: () => void
  selectedIds: number[]
  /** 拖拽起止回调：父级据此把被拖行钉在渲染集合里，避免滚动时源元素被回收而中断拖拽。 */
  onDragStateChange?: (id: number | null) => void
  children: ReactNode
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
  onAddMembers,
  onRemoveMembers,
  notify,
}: {
  selectedIds: number[]
  selectedGroupId: number | null
  groups: InstanceGroupNode[]
  onDone: () => void
  onAddMembers: (payload: { groupId: number; instanceIds: number[] }) => Promise<{ added: number }>
  onRemoveMembers: (payload: { groupId: number; instanceIds: number[] }) => Promise<void>
  notify: GroupManagerNotice
}) {
  const { t } = useTranslation()
  const [target, setTarget] = useState<string>('')

  const submit = async () => {
    if (!target) return
    try {
      const res = await onAddMembers({ groupId: Number(target), instanceIds: selectedIds })
      notify('success', t('instanceGroups.markedCount', { count: res.added }))
      onDone()
    } catch {
      notify('error', t('instanceGroups.markFailed'))
    }
  }

  const removeFromCurrent = async () => {
    if (selectedGroupId === null) return
    try {
      await onRemoveMembers({ groupId: selectedGroupId, instanceIds: selectedIds })
      notify('success', t('instanceGroups.removedCount', { count: selectedIds.length }))
      onDone()
    } catch {
      notify('error', t('instanceGroups.removeFailed'))
    }
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
        <Button size="sm" onClick={() => void submit()} disabled={!target}>
          {t('instanceGroups.markInto')}
        </Button>
        {selectedGroupId !== null && (
          <Button
            size="sm"
            variant="outline"
            className="text-destructive hover:text-destructive"
            title={t('instanceGroups.removeFromCurrentHint')}
            onClick={() => void removeFromCurrent()}
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
