import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ChevronRight, FolderPlus, FolderTree, Pencil, Plus, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import {
  useInstanceGroups,
  useCreateInstanceGroup,
  useUpdateInstanceGroup,
  useDeleteInstanceGroup,
  useAddInstanceGroupMembers,
  type InstanceGroupNode,
} from '@/api/instanceGroups'
import { useConsoleStore } from '@/stores/console'
import {
  buildGroupTree,
  flattenVisibleGroups,
  groupBranchKey,
  type VisibleGroupRow,
} from './instance-group-tree'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog'
import { cn } from '@/lib/utils'

/** 拖拽实例入组时 dataTransfer 的自定义 MIME（与浏览器文本拖拽区分）。 */
export const INSTANCE_DND_MIME = 'application/x-jm-instance-id'

/**
 * 实例组织分组树（FR-165，design §4.4 左树 / ADR-XXXX）。
 * 文件夹式多级嵌套：新建组 / 嵌套子组 / 改名 / 删（非空后端拒删）/ 折叠优先（折叠分支只渲染分组头）/
 * 选中。每节点挂「子树聚合去重」实例数。折叠态复用 console store collapsedGroups（键 `igroup:<id>`，
 * 与侧栏实例树 `tree:` 隔离）。支持把实例从右列表拖入某组（HTML5 原生 DnD）。
 */
export function InstanceGroupTree({
  selectedGroupId,
  onSelect,
}: {
  /** 当前选中组 id；null=未选（右列表显示「全部/未选」）。 */
  selectedGroupId: number | null
  onSelect: (groupId: number | null) => void
}) {
  const { t } = useTranslation()
  const { data: groups, isLoading } = useInstanceGroups()
  const collapsedGroups = useConsoleStore((s) => s.collapsedGroups)
  const toggleGroup = useConsoleStore((s) => s.toggleGroup)

  const create = useCreateInstanceGroup()
  const update = useUpdateInstanceGroup()
  const del = useDeleteInstanceGroup()
  const addMembers = useAddInstanceGroupMembers()

  // 建组 / 改名对话框状态：mode 决定提交语义，parentId 仅建子组用。
  const [dialog, setDialog] = useState<
    | { mode: 'create-root' }
    | { mode: 'create-child'; parentId: number; parentName: string }
    | { mode: 'rename'; id: number; current: string }
    | null
  >(null)
  const [name, setName] = useState('')
  // 拖拽悬停高亮的组 id（拖实例经过时反馈可放置）。
  const [dropTarget, setDropTarget] = useState<number | null>(null)

  const rows = useMemo<VisibleGroupRow[]>(() => {
    const tree = buildGroupTree(groups ?? [])
    return flattenVisibleGroups(tree, collapsedGroups)
  }, [groups, collapsedGroups])

  const openCreateRoot = () => {
    setName('')
    setDialog({ mode: 'create-root' })
  }
  const openCreateChild = (node: InstanceGroupNode) => {
    setName('')
    setDialog({ mode: 'create-child', parentId: node.id, parentName: node.name })
  }
  const openRename = (node: InstanceGroupNode) => {
    setName(node.name)
    setDialog({ mode: 'rename', id: node.id, current: node.name })
  }

  const submitDialog = () => {
    const trimmed = name.trim()
    if (!trimmed || !dialog) return
    if (dialog.mode === 'create-root') {
      create.mutate(
        { name: trimmed },
        { onSuccess: () => setDialog(null), onError: () => toast.error(t('instanceGroups.createFailed')) },
      )
    } else if (dialog.mode === 'create-child') {
      create.mutate(
        { name: trimmed, parentId: dialog.parentId },
        { onSuccess: () => setDialog(null), onError: () => toast.error(t('instanceGroups.createFailed')) },
      )
    } else {
      update.mutate(
        { id: dialog.id, name: trimmed },
        { onSuccess: () => setDialog(null), onError: () => toast.error(t('instanceGroups.renameFailed')) },
      )
    }
  }

  const handleDelete = (node: InstanceGroupNode) => {
    del.mutate(node.id, {
      onSuccess: () => {
        if (selectedGroupId === node.id) onSelect(null)
        toast.success(t('instanceGroups.deleted'))
      },
      // 非空组后端返回 409 INSTANCE_GROUP_NOT_EMPTY：提示先清空，不级联删（验收 §5）。
      onError: () => toast.error(t('instanceGroups.deleteNotEmpty')),
    })
  }

  // 拖实例入组：把拖入的实例加入该组（幂等），成功提示新增数。
  const handleDropInstances = (groupId: number, instanceIds: number[]) => {
    setDropTarget(null)
    addMembers.mutate(
      { id: groupId, instanceIds },
      {
        onSuccess: (res) => toast.success(t('instanceGroups.markedCount', { count: res.added })),
        onError: () => toast.error(t('instanceGroups.markFailed')),
      },
    )
  }

  const dialogTitle =
    dialog?.mode === 'rename'
      ? t('instanceGroups.renameTitle')
      : dialog?.mode === 'create-child'
        ? t('instanceGroups.createChildTitle', { parent: dialog.parentName })
        : t('instanceGroups.createRootTitle')

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex items-center justify-between gap-2 px-1 pb-2">
        <div className="flex items-center gap-1.5 text-sm font-semibold text-foreground">
          <FolderTree className="size-4 text-primary" />
          {t('instanceGroups.treeTitle')}
        </div>
        <Button variant="outline" size="xs" onClick={openCreateRoot}>
          <FolderPlus className="size-3.5" /> {t('instanceGroups.newRoot')}
        </Button>
      </div>

      <div className="min-h-0 flex-1 space-y-0.5 overflow-auto pr-1">
        {/* 「全部实例」根行：选中=清空组筛选 */}
        <button
          type="button"
          onClick={() => onSelect(null)}
          className={cn(
            'flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm',
            selectedGroupId === null ? 'bg-accent font-medium' : 'hover:bg-accent/50',
          )}
        >
          <FolderTree className="size-4 shrink-0 opacity-70" />
          <span className="min-w-0 flex-1 truncate">{t('instanceGroups.allInstances')}</span>
        </button>

        {isLoading ? (
          <p className="px-2 py-2 text-xs text-muted-foreground">{t('common.loading')}</p>
        ) : rows.length === 0 ? (
          <p className="px-2 py-3 text-xs text-muted-foreground">{t('instanceGroups.empty')}</p>
        ) : (
          rows.map((row) => (
            <GroupRow
              key={row.id}
              row={row}
              selected={selectedGroupId === row.id}
              collapsed={!!collapsedGroups[groupBranchKey(row.id)]}
              isDropTarget={dropTarget === row.id}
              onSelect={() => onSelect(row.id)}
              onToggle={() => toggleGroup(groupBranchKey(row.id))}
              onCreateChild={() => openCreateChild(row)}
              onRename={() => openRename(row)}
              onDelete={() => handleDelete(row)}
              onDragEnter={() => setDropTarget(row.id)}
              onDragLeaveTarget={() => setDropTarget((cur) => (cur === row.id ? null : cur))}
              onDropInstances={(ids) => handleDropInstances(row.id, ids)}
            />
          ))
        )}
      </div>

      <Dialog open={dialog !== null} onOpenChange={(o) => !o && setDialog(null)}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{dialogTitle}</DialogTitle>
          </DialogHeader>
          <Input
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('instanceGroups.namePlaceholder')}
            onKeyDown={(e) => {
              if (e.key === 'Enter') submitDialog()
            }}
            maxLength={128}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialog(null)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={submitDialog} disabled={!name.trim() || create.isPending || update.isPending}>
              {t('common.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

/** 单条分组行：缩进 + 折叠箭头 + 名称 + 子树计数 + hover 操作（建子组/改名/删）+ 拖放目标。 */
function GroupRow({
  row,
  selected,
  collapsed,
  isDropTarget,
  onSelect,
  onToggle,
  onCreateChild,
  onRename,
  onDelete,
  onDragEnter,
  onDragLeaveTarget,
  onDropInstances,
}: {
  row: VisibleGroupRow
  selected: boolean
  collapsed: boolean
  isDropTarget: boolean
  onSelect: () => void
  onToggle: () => void
  onCreateChild: () => void
  onRename: () => void
  onDelete: () => void
  onDragEnter: () => void
  onDragLeaveTarget: () => void
  onDropInstances: (instanceIds: number[]) => void
}) {
  const { t } = useTranslation()

  const parseDnd = (e: React.DragEvent): number[] => {
    const raw = e.dataTransfer.getData(INSTANCE_DND_MIME)
    if (!raw) return []
    try {
      const parsed = JSON.parse(raw)
      if (Array.isArray(parsed)) return parsed.filter((x): x is number => typeof x === 'number')
    } catch {
      // 非本应用拖拽载荷，忽略
    }
    return []
  }

  return (
    <div
      className={cn(
        'group flex items-center gap-1 rounded-md',
        selected ? 'bg-accent' : 'hover:bg-accent/50',
        isDropTarget && 'ring-2 ring-primary ring-inset',
      )}
      style={{ paddingLeft: row.depth * 14 }}
      onDragOver={(e) => {
        if (e.dataTransfer.types.includes(INSTANCE_DND_MIME)) {
          e.preventDefault()
          e.dataTransfer.dropEffect = 'copy'
        }
      }}
      onDragEnter={(e) => {
        if (e.dataTransfer.types.includes(INSTANCE_DND_MIME)) onDragEnter()
      }}
      onDragLeave={onDragLeaveTarget}
      onDrop={(e) => {
        const ids = parseDnd(e)
        if (ids.length > 0) {
          e.preventDefault()
          onDropInstances(ids)
        }
      }}
    >
      <button
        type="button"
        onClick={onToggle}
        aria-label={t('instanceGroups.toggle')}
        className={cn('shrink-0 px-0.5 text-muted-foreground hover:text-foreground', !row.hasChildren && 'invisible')}
      >
        {collapsed ? <ChevronRight className="size-3.5" /> : <ChevronDown className="size-3.5" />}
      </button>
      <button
        type="button"
        onClick={onSelect}
        className={cn('flex min-w-0 flex-1 items-center gap-2 py-1.5 text-left text-sm', selected && 'font-medium')}
      >
        <span className="min-w-0 flex-1 truncate">{row.name}</span>
        <span className="shrink-0 tabular-nums text-xs opacity-60">{row.instanceCount}</span>
      </button>
      {/* hover 操作：建子组 / 改名 / 删 */}
      <div className="flex shrink-0 items-center opacity-0 transition-opacity group-hover:opacity-100">
        <button
          type="button"
          onClick={onCreateChild}
          aria-label={t('instanceGroups.newChild')}
          title={t('instanceGroups.newChild')}
          className="px-1 text-muted-foreground hover:text-primary"
        >
          <Plus className="size-3.5" />
        </button>
        <button
          type="button"
          onClick={onRename}
          aria-label={t('instanceGroups.rename')}
          title={t('instanceGroups.rename')}
          className="px-1 text-muted-foreground hover:text-foreground"
        >
          <Pencil className="size-3.5" />
        </button>
        <button
          type="button"
          onClick={onDelete}
          aria-label={t('common.delete')}
          title={t('common.delete')}
          className="px-1 text-muted-foreground hover:text-destructive"
        >
          <Trash2 className="size-3.5" />
        </button>
      </div>
    </div>
  )
}
