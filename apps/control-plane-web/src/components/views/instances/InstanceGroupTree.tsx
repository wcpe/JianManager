import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ChevronRight, FolderPlus, FolderTree, Pencil, Plus, Trash2 } from 'lucide-react'
import { useVirtualRows } from '@/lib/shared/virtual-list'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { cn } from '@jianmanager/ui'
import type { InstanceGroupNode } from '@/lib/instances/instance-group'
import { buildGroupTree, flattenVisibleGroups, groupBranchKey } from '@/lib/instances/instance-group-tree'
import type { GroupTreeNode, VisibleGroupRow } from '@/lib/instances/instance-group-tree'

/** 实例拖入分组用的自定义 MIME（与工作区拖拽载荷同源，由实例库/分组管理页发出）。 */
export const INSTANCE_DND_MIME = 'application/x-jm-instances'

/** 分组树行高（px）。虚拟化按行定位，须与真实渲染高度一致。 */
/**
 * 虚拟化行高（= 行盒高度，px）。
 *
 * 【为什么容器不能用 space-y-*】间距不计入行高：第 N 行的真实偏移是 N×行高 + 间距×(N−1)，
 * 而等距虚拟化按 index×itemSize 定位，偏移会随已渲染行数累积。故不设行间距，
 * 把呼吸空间折进行盒——行盒用本常量显式定高，「声明值 = 真实值」由构造保证。
 * 同一取舍见 `views/networks/NetworksPageView.tsx`。
 */
const GROUP_TREE_ROW_HEIGHT = 34

/** 提示通道：视图算好文案交外壳展示（本包不弹 toast）。 */
export type GroupTreeNotice = (kind: 'success' | 'error', message: string) => void

function normalizeGroupQuery(value: string): string {
  return value.trim().toLocaleLowerCase()
}

function flattenSearchGroups(tree: GroupTreeNode[], query: string): VisibleGroupRow[] {
  const collect = (node: GroupTreeNode): VisibleGroupRow[] => {
    const childRows = node.children.flatMap(collect)
    if (!node.name.toLocaleLowerCase().includes(query) && childRows.length === 0) return []
    return [{ ...node, hasChildren: node.children.length > 0 }, ...childRows]
  }
  return tree.flatMap(collect)
}

/**
 * 实例组织分组树（FR-165，design §4.4 左树 / ADR-033）。
 * 文件夹式多级嵌套：新建组 / 嵌套子组 / 改名 / 删（非空后端拒删）/ 折叠优先（折叠分支只渲染分组头）/
 * 选中。每节点挂「子树聚合去重」实例数。折叠态由外壳注入（键 `igroup:<id>`，
 * 与侧栏实例树 `tree:` 隔离）。支持把实例从右列表拖入某组（HTML5 原生 DnD）。
 *
 * 受控视图（ADR-097 b 范式）：不取数、不发请求、不弹 toast —— 分组列表、折叠态与四个
 * 写动作都由外壳注入。树构建、搜索剪枝、虚拟化、键盘导航、拖放解析留在视图内。
 */
export interface InstanceGroupTreeProps {
  /** 当前选中组 id；null=未选（右列表显示「全部/未选」）。 */
  selectedGroupId: number | null
  onSelect: (groupId: number | null) => void
  /** 分组列表（扁平）。 */
  groups: InstanceGroupNode[]
  /** 加载中。 */
  loading?: boolean
  /** 折叠态映射（键 `igroup:<id>`）。 */
  collapsedGroups: Record<string, boolean>
  /** 切换某分支的折叠态（传 `igroup:<id>`）。 */
  onToggleCollapsed: (branchKey: string) => void
  /** 建组（根或子组）。失败请抛错，视图按语义提示。 */
  onCreate: (payload: { name: string; parentId?: number }) => Promise<void>
  /** 改名。 */
  onRename: (payload: { id: number; name: string }) => Promise<void>
  /** 删除（非空组后端拒删）。 */
  onDelete: (id: number) => Promise<void>
  /** 把实例拖入某组（幂等），返回本次新增数。 */
  onDropInstances: (payload: { groupId: number; instanceIds: number[] }) => Promise<{ added: number }>
  /** 提示通道。 */
  notify: GroupTreeNotice
}

export function InstanceGroupTree({
  selectedGroupId,
  onSelect,
  groups,
  loading = false,
  collapsedGroups,
  onToggleCollapsed,
  onCreate,
  onRename,
  onDelete,
  onDropInstances,
  notify,
}: InstanceGroupTreeProps) {
  const { t } = useTranslation()

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
  const [query, setQuery] = useState('')
  const [submitting, setSubmitting] = useState(false)

  const tree = useMemo(() => buildGroupTree(groups), [groups])
  const normalizedQuery = useMemo(() => normalizeGroupQuery(query), [query])
  const rows = useMemo<VisibleGroupRow[]>(
    () =>
      normalizedQuery.length > 0
        ? flattenSearchGroups(tree, normalizedQuery)
        : flattenVisibleGroups(tree, collapsedGroups),
    [collapsedGroups, normalizedQuery, tree],
  )
  const { containerRef, onScroll, range } = useVirtualRows({
    total: rows.length,
    itemSize: GROUP_TREE_ROW_HEIGHT,
    overscan: 8,
    fallbackViewportSize: 420,
  })
  const virtualRows = rows.slice(range.start, range.end)

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

  const submitDialog = async () => {
    const trimmed = name.trim()
    if (!trimmed || !dialog) return
    setSubmitting(true)
    try {
      if (dialog.mode === 'create-root') {
        await onCreate({ name: trimmed })
      } else if (dialog.mode === 'create-child') {
        await onCreate({ name: trimmed, parentId: dialog.parentId })
      } else {
        await onRename({ id: dialog.id, name: trimmed })
      }
      setDialog(null)
    } catch {
      notify('error', t(dialog.mode === 'rename' ? 'instanceGroups.renameFailed' : 'instanceGroups.createFailed'))
    } finally {
      setSubmitting(false)
    }
  }

  const handleDelete = async (node: InstanceGroupNode) => {
    try {
      await onDelete(node.id)
      if (selectedGroupId === node.id) onSelect(null)
      notify('success', t('instanceGroups.deleted'))
    } catch {
      // 非空组后端返回 409 INSTANCE_GROUP_NOT_EMPTY：提示先清空，不级联删（验收 §5）。
      notify('error', t('instanceGroups.deleteNotEmpty'))
    }
  }

  // 拖实例入组：把拖入的实例加入该组（幂等），成功提示新增数。
  const handleDropInstances = async (groupId: number, instanceIds: number[]) => {
    setDropTarget(null)
    try {
      const res = await onDropInstances({ groupId, instanceIds })
      notify('success', t('instanceGroups.markedCount', { count: res.added }))
    } catch {
      notify('error', t('instanceGroups.markFailed'))
    }
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
      <div className="px-1 pb-2">
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t('instanceGroups.searchPlaceholder')}
          aria-label={t('instanceGroups.searchLabel')}
          className="h-8"
        />
      </div>

      <div
        ref={containerRef}
        onScroll={onScroll}
        role="tree"
        aria-label={t('instanceGroups.treeTitle')}
        className="min-h-0 flex-1 overflow-auto pr-1"
      >
        {/* 「全部实例」根行：选中=清空组筛选 */}
        <button
          type="button"
          role="treeitem"
          aria-selected={selectedGroupId === null}
          onClick={() => onSelect(null)}
          className={cn(
            'flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm',
            selectedGroupId === null ? 'bg-accent font-medium' : 'hover:bg-accent/50',
          )}
        >
          <FolderTree className="size-4 shrink-0 opacity-70" />
          <span className="min-w-0 flex-1 truncate">{t('instanceGroups.allInstances')}</span>
        </button>

        {loading ? (
          <p className="px-2 py-2 text-xs text-muted-foreground">{t('common.loading')}</p>
        ) : rows.length === 0 ? (
          normalizedQuery ? (
            <p className="px-2 py-3 text-xs text-muted-foreground">{t('instanceGroups.noSearchResults')}</p>
          ) : (
            <div className="space-y-2 px-2 py-3">
              <p className="text-xs font-medium text-foreground">{t('instanceGroups.empty')}</p>
              <p className="text-xs text-muted-foreground">{t('instanceGroups.emptyHint')}</p>
              <Button type="button" size="xs" variant="outline" onClick={openCreateRoot}>
                <FolderPlus className="size-3.5" /> {t('instanceGroups.emptyCta')}
              </Button>
            </div>
          )
        ) : (
          <>
            {range.before > 0 && <div aria-hidden="true" style={{ height: range.before }} />}
            {virtualRows.map((row) => (
              <GroupRow
                key={row.id}
                row={row}
                selected={selectedGroupId === row.id}
                collapsed={!!collapsedGroups[groupBranchKey(row.id)]}
                isDropTarget={dropTarget === row.id}
                onSelect={() => onSelect(row.id)}
                onToggle={() => onToggleCollapsed(groupBranchKey(row.id))}
                onCreateChild={() => openCreateChild(row)}
                onRename={() => openRename(row)}
                onDelete={() => void handleDelete(row)}
                onDragEnter={() => setDropTarget(row.id)}
                onDragLeaveTarget={() => setDropTarget((cur) => (cur === row.id ? null : cur))}
                onDropInstances={(ids) => void handleDropInstances(row.id, ids)}
              />
            ))}
            {range.after > 0 && <div aria-hidden="true" style={{ height: range.after }} />}
          </>
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
              if (e.key === 'Enter') void submitDialog()
            }}
            maxLength={128}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialog(null)}>
              {t('common.cancel')}
            </Button>
            <Button onClick={() => void submitDialog()} disabled={!name.trim() || submitting}>
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

  const handleKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.target !== e.currentTarget) return
    if (e.key === 'Enter') {
      e.preventDefault()
      onSelect()
    } else if (e.key === ' ') {
      e.preventDefault()
      if (row.hasChildren) onToggle()
      else onSelect()
    }
  }

  return (
    <div
      role="treeitem"
      tabIndex={0}
      aria-level={row.depth + 1}
      aria-selected={selected}
      aria-expanded={row.hasChildren ? !collapsed : undefined}
      className={cn(
        'group flex items-center gap-1 rounded-md',
        selected ? 'bg-accent' : 'hover:bg-accent/50',
        isDropTarget && 'ring-2 ring-primary ring-inset',
      )}
      style={{ paddingLeft: row.depth * 14, height: GROUP_TREE_ROW_HEIGHT }}
      onKeyDown={handleKeyDown}
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
