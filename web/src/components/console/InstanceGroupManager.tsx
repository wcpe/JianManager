import { Fragment, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronRight, FolderTree, Tag } from 'lucide-react'
import { toast } from 'sonner'
import { useInstances, type InstanceInfo } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import {
  useInstanceGroups,
  useInstanceGroupSubtree,
  useAddInstanceGroupMembers,
  type InstanceGroupNode,
} from '@/api/instanceGroups'
import { InstanceWorktableCard } from './InstanceWorktableCard'
import { InstanceGroupTree, INSTANCE_DND_MIME } from './InstanceGroupTree'
import { groupPathOf } from './instance-group-path'
import { Panel } from '@/components/ui/panel'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { cn } from '@/lib/utils'

/**
 * 实例多级分组完整视图（FR-165，design §4.4）：左 = 分组树，右 = 选中组（含子树）的实例列表。
 * 右列表复用工作台卡骨架（§4.5），头部为组路径面包屑 + 批量「标记入组」；实例卡可拖入左树某组。
 * 与 InstancesPage 既有筛选/分组视图正交并列——本视图是「按组织归类浏览」的专用形态。
 */
export function InstanceGroupManager() {
  const { t } = useTranslation()
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

      <Panel bodyClassName="p-3">
        {/* 组路径面包屑 */}
        <div className="mb-3 flex flex-wrap items-center gap-1 text-sm">
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
          <MarkIntoGroupBar
            selectedIds={selectedIds}
            groups={groups ?? []}
            onDone={clearSelection}
          />
        )}

        {visible.length === 0 ? (
          <p className="py-10 text-center text-sm text-muted-foreground">
            {selectedGroupId === null ? t('instances.empty') : t('instanceGroups.groupEmpty')}
          </p>
        ) : (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {visible.map((inst) => (
              <DraggableInstance
                key={inst.id}
                inst={inst}
                selected={selectedIds.includes(inst.id)}
                onToggle={() => toggleOne(inst.id)}
                selectedIds={selectedIds}
              >
                <InstanceWorktableCard
                  inst={inst}
                  nodeName={nodeName(inst.nodeId)}
                  roleBadge={null}
                  menu={null}
                />
              </DraggableInstance>
            ))}
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
  children,
}: {
  inst: InstanceInfo
  selected: boolean
  onToggle: () => void
  selectedIds: number[]
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
      }}
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
  groups,
  onDone,
}: {
  selectedIds: number[]
  groups: InstanceGroupNode[]
  onDone: () => void
}) {
  const { t } = useTranslation()
  const [target, setTarget] = useState<string>('')
  const addMembers = useAddInstanceGroupMembers()

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
        <Button size="sm" variant="ghost" onClick={onDone}>
          {t('common.cancel')}
        </Button>
      </div>
    </div>
  )
}
