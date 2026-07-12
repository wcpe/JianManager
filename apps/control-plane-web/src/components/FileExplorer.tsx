import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type DragEvent,
} from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import {
  ChevronRight,
  File,
  Folder,
  FolderOpen,
  FolderPlus,
  GripVertical,
  Lock,
  Pencil,
  Trash2,
} from 'lucide-react'
import {
  buildFileTreeWithDirs,
  collectSubtreeFiles,
  detectConflicts,
  isSelfOrDescendant,
  keepBothPath,
  moveDirToDir,
  moveFileToDir,
  nextUniqueName,
  normalizeManifestPath,
  renamePathSegment,
  type ConflictResolution,
  type LocalUnit,
  type ManifestFileLike,
  type TreeDir,
  type TreeFile,
} from '@/lib/client-publish-wizard'
import { cn } from '@jianmanager/ui'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import DangerConfirm from '@/components/DangerConfirm'

/** 平台「全部」哨兵（Radix Select 不允许空字符串值，回写时映射回 ""）。 */
const PLATFORM_ALL = '__all__'

/** 字节数转人类可读。 */
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

// ── 拖拽移动载荷 ──────────────────────────────────────────────────────

/** 拖拽移动载荷：文件节点或目录节点（FR-254 拖拽编排，FR-261 复用）。 */
type DragPayload =
  | { kind: 'file'; index: number; path: string }
  | { kind: 'dir'; dirPath: string; subtree: { index: number; path: string }[] }

// ── 右键 / 重命名目标 ────────────────────────────────────────────────

/** 右键菜单目标。 */
type ContextTarget =
  | { kind: 'file'; index: number; path: string }
  | { kind: 'dir'; dirPath: string }
  | { kind: 'root' }

/** 正在重命名的目标。 */
type RenamingTarget =
  | { kind: 'file'; index: number; path: string }
  | { kind: 'dir'; dirPath: string }

// ── Context ───────────────────────────────────────────────────────────

/** 文件资源管理器内部共享状态（避免逐层 prop 传递）。 */
interface ExplorerContextValue {
  readonly: boolean
  t: TFunction
  // 拖拽移动
  dragPayload: DragPayload | null
  setDragPayload: (p: DragPayload | null) => void
  dragOverPath: string | null
  setDragOverPath: (p: string | null) => void
  // 选中
  selectedIndices: Set<number>
  onFileClick: (index: number, e: ReactMouseEvent) => void
  // 折叠
  collapsedPaths: Set<string>
  toggleDir: (path: string) => void
  // 重命名
  renamingTarget: RenamingTarget | null
  startRename: (target: RenamingTarget) => void
  commitRename: (newName: string) => void
  cancelRename: () => void
  // 右键
  openContextMenu: (e: ReactMouseEvent, target: ContextTarget) => void
  // 拖拽移动落区（统一处理文件/目录移动 + emptyDirs 同步）
  onMoveDrop: (targetDirPath: string) => void
  // 外部文件拖入
  resolveDrop?: ((files: File[], targetDirPath: string) => Promise<LocalUnit[]>) | null
  handleExternalDrop: (files: File[], targetDirPath: string) => void
  // 编排回调
  onPathChange?: (index: number, path: string) => void
  onSyncChange?: (index: number, sync: ManifestFileLike['sync']) => void
  onPlatformChange?: (index: number, platform: ManifestFileLike['platform']) => void
}

const ExplorerContext = createContext<ExplorerContextValue | null>(null)

// ── 主组件 ────────────────────────────────────────────────────────────

/**
 * 发布页文件资源管理器（FR-261）。
 *
 * 替换 ClientFileTree 的专业文件树：工具栏（新建文件夹/删除/展开全部/折叠全部）+
 * 右键菜单（重命名/删除/新建文件夹）+ 多选（Ctrl/Shift）+ Delete 删除 + 双击重命名 +
 * 拖拽移动 + 拖拽上传落区 + 同名冲突弹窗。保留「本地编排 → 点发布才上传」架构。
 *
 * 只读模式（review/详情）：纯展示，无工具栏/右键/拖拽，文件行显示 sync/platform 徽标。
 */
export interface FileExplorerProps {
  files: ManifestFileLike[]
  readonly?: boolean
  onPathChange?: (index: number, path: string) => void
  onSyncChange?: (index: number, sync: ManifestFileLike['sync']) => void
  onPlatformChange?: (index: number, platform: ManifestFileLike['platform']) => void
  onRemove?: (index: number) => void
  /** 批量删除（多选后 Delete）。省略时退化为逐个 onRemove。 */
  onRemoveMultiple?: (indices: number[]) => void
  /** 拖拽上传：页面层解压 zip 后返回本地单元，组件做冲突检测 + 弹窗。 */
  resolveDrop?: (files: File[], targetDir: string) => Promise<LocalUnit[]>
  /** 冲突解决后 / 无冲突时，把单元累加进草稿。 */
  onAddFiles?: (units: LocalUnit[]) => void
}

export default function FileExplorer({
  files,
  readonly = false,
  onPathChange,
  onSyncChange,
  onPlatformChange,
  onRemove,
  onRemoveMultiple,
  resolveDrop,
  onAddFiles,
}: FileExplorerProps) {
  const { t } = useTranslation()

  // 空目录（新建文件夹的空占位，不进 manifest）。
  const [emptyDirs, setEmptyDirs] = useState<string[]>([])
  // 折叠的目录路径集合（默认空=全展开）。
  const [collapsedPaths, setCollapsedPaths] = useState<Set<string>>(() => new Set())
  // 多选文件 index 集合。
  const [selectedIndices, setSelectedIndices] = useState<Set<number>>(() => new Set())
  // Shift 连选锚点。
  const anchorRef = useRef<number | null>(null)
  // 拖拽移动载荷。
  const [dragPayload, setDragPayload] = useState<DragPayload | null>(null)
  const [dragOverPath, setDragOverPath] = useState<string | null>(null)
  // 重命名目标。
  const [renamingTarget, setRenamingTarget] = useState<RenamingTarget | null>(null)
  // 右键菜单位置 + 目标。
  const [contextMenu, setContextMenu] = useState<{ x: number; y: number; target: ContextTarget } | null>(null)
  // 删除确认弹窗（待删除的文件 index 列表）。
  const [deleteConfirm, setDeleteConfirm] = useState<{ indices: number[]; dirPaths: string[] } | null>(null)
  // 冲突弹窗。
  const [pendingConflicts, setPendingConflicts] = useState<{
    units: LocalUnit[]
    existingPaths: string[]
    conflicts: string[]
  } | null>(null)
  // 拖拽上传高亮。
  const [dropActive, setDropActive] = useState(false)
  // 上传中禁用。
  const [resolving, setResolving] = useState(false)

  // 有效空目录（排除已被文件占据的）——派生值而非 effect 同步。
  const effectiveEmptyDirs = useMemo(() => {
    const occupied = new Set<string>()
    for (const f of files) {
      const segs = normalizeManifestPath(f.path).split('/').filter((s) => s !== '')
      let acc = ''
      for (let i = 0; i < segs.length - 1; i++) {
        acc = acc === '' ? segs[i] : `${acc}/${segs[i]}`
        occupied.add(acc)
      }
    }
    return emptyDirs.filter((d) => !occupied.has(d))
  }, [files, emptyDirs])

  const tree = useMemo(() => buildFileTreeWithDirs(files, effectiveEmptyDirs), [files, effectiveEmptyDirs])

  // 点击外部关闭右键菜单。
  useEffect(() => {
    if (!contextMenu) return
    const close = () => setContextMenu(null)
    document.addEventListener('click', close)
    document.addEventListener('contextmenu', close)
    return () => {
      document.removeEventListener('click', close)
      document.removeEventListener('contextmenu', close)
    }
  }, [contextMenu])

  // 收集可见文件列表（按视觉顺序，供 Shift 连选）。
  const visibleFiles = useMemo(() => collectVisibleFiles(tree, collapsedPaths), [tree, collapsedPaths])

  // ── 选中 ──────────────────────────────────────────────────────────

  const handleFileClick = useCallback(
    (index: number, e: ReactMouseEvent) => {
      if (readonly) return
      if (e.ctrlKey || e.metaKey) {
        setSelectedIndices((prev) => {
          const next = new Set(prev)
          if (next.has(index)) next.delete(index)
          else next.add(index)
          return next
        })
        anchorRef.current = index
      } else if (e.shiftKey && anchorRef.current !== null) {
        const positions = visibleFiles.map((f) => f.index)
        const ai = positions.indexOf(anchorRef.current)
        const ci = positions.indexOf(index)
        if (ai >= 0 && ci >= 0) {
          const [lo, hi] = ai < ci ? [ai, ci] : [ci, ai]
          setSelectedIndices(new Set(positions.slice(lo, hi + 1)))
        } else {
          setSelectedIndices(new Set([index]))
        }
      } else {
        setSelectedIndices(new Set([index]))
        anchorRef.current = index
      }
    },
    [readonly, visibleFiles],
  )

  // ── 折叠 ──────────────────────────────────────────────────────────

  const toggleDir = useCallback((path: string) => {
    setCollapsedPaths((prev) => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }, [])

  const expandAll = useCallback(() => setCollapsedPaths(new Set()), [])
  const collapseAll = useCallback(() => {
    const all = collectAllDirPaths(tree)
    setCollapsedPaths(new Set(all))
  }, [tree])

  // ── 重命名 ────────────────────────────────────────────────────────

  const startRename = useCallback((target: RenamingTarget) => {
    setSelectedIndices(new Set())
    setRenamingTarget(target)
  }, [])

  const cancelRename = useCallback(() => setRenamingTarget(null), [])

  const commitRename = useCallback(
    (newName: string) => {
      const target = renamingTarget
      setRenamingTarget(null)
      if (!target) return
      const name = newName.trim()
      if (name === '' || name.includes('/')) return
      if (target.kind === 'file') {
        onPathChange?.(target.index, renamePathSegment(target.path, name))
      } else {
        // 重命名目录：改子文件 path + 改 emptyDirs
        const oldPath = target.dirPath
        const parent = oldPath.includes('/') ? oldPath.slice(0, oldPath.lastIndexOf('/')) : ''
        const newPath = parent === '' ? name : `${parent}/${name}`
        if (newPath === oldPath) return
        // 找到树中的目录节点（可能为空目录或有文件）
        const dirNode = findDirNode(tree, oldPath)
        if (dirNode) {
          for (const f of collectSubtreeFiles(dirNode)) {
            const rel = f.path.substring(oldPath.length)
            onPathChange?.(f.index, newPath + rel)
          }
        }
        setEmptyDirs((prev) =>
          prev.map((d) => {
            if (d === oldPath) return newPath
            if (d.startsWith(`${oldPath}/`)) return newPath + d.substring(oldPath.length)
            return d
          }),
        )
      }
    },
    [renamingTarget, onPathChange, tree],
  )

  // ── 新建文件夹 ────────────────────────────────────────────────────

  const createFolder = useCallback(
    (parentDirPath: string) => {
      if (readonly) return
      // 同级已有目录名（从树中取 parentDirPath 下的子目录）
      const parentNode = parentDirPath === '' ? tree : findDirNode(tree, parentDirPath)
      const existingNames = parentNode ? parentNode.dirs.map((d) => d.name) : []
      const base = t('fileExplorer.newFolderName', '新建文件夹')
      const name = nextUniqueName(base, existingNames)
      const newPath = parentDirPath === '' ? name : `${parentDirPath}/${name}`
      setEmptyDirs((prev) => [...prev, newPath])
      // 展开父目录（确保新建目录可见）
      setCollapsedPaths((prev) => {
        const next = new Set(prev)
        next.delete(parentDirPath)
        return next
      })
      // 进入重命名模式
      setTimeout(() => setRenamingTarget({ kind: 'dir', dirPath: newPath }), 0)
    },
    [readonly, tree, t],
  )

  // ── 删除 ──────────────────────────────────────────────────────────

  const requestDelete = useCallback(
    (target: ContextTarget) => {
      if (readonly) return
      if (target.kind === 'file') {
        const indices = selectedIndices.has(target.index) && selectedIndices.size > 1
          ? Array.from(selectedIndices)
          : [target.index]
        setDeleteConfirm({ indices, dirPaths: [] })
      } else if (target.kind === 'dir') {
        const dirNode = findDirNode(tree, target.dirPath)
        const indices = dirNode ? collectSubtreeFiles(dirNode).map((f) => f.index) : []
        setDeleteConfirm({ indices, dirPaths: [target.dirPath] })
      } else {
        // root：删除全部
        const indices = files.map((_, i) => i)
        setDeleteConfirm({ indices, dirPaths: [] })
      }
    },
    [readonly, selectedIndices, tree, files],
  )

  const confirmDelete = useCallback(() => {
    const dc = deleteConfirm
    setDeleteConfirm(null)
    if (!dc) return
    // 删除文件
    if (dc.indices.length > 0) {
      if (onRemoveMultiple) onRemoveMultiple(dc.indices)
      else dc.indices.forEach((i) => onRemove?.(i))
    }
    // 删除空目录（及其子空目录）
    if (dc.dirPaths.length > 0) {
      setEmptyDirs((prev) =>
        prev.filter((d) => !dc.dirPaths.some((dp) => d === dp || d.startsWith(`${dp}/`))),
      )
    }
    setSelectedIndices(new Set())
  }, [deleteConfirm, onRemoveMultiple, onRemove])

  // ── 右键菜单 ──────────────────────────────────────────────────────

  const openContextMenu = useCallback((e: ReactMouseEvent, target: ContextTarget) => {
    if (readonly) return
    e.preventDefault()
    e.stopPropagation()
    setContextMenu({ x: e.clientX, y: e.clientY, target })
  }, [readonly])

  // ── Delete 键 ─────────────────────────────────────────────────────

  useEffect(() => {
    if (readonly) return
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== 'Delete') return
      // 编辑中（input/textarea 聚焦）不触发
      const tag = (e.target as HTMLElement)?.tagName
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return
      if (selectedIndices.size === 0) return
      e.preventDefault()
      setDeleteConfirm({ indices: Array.from(selectedIndices), dirPaths: [] })
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [readonly, selectedIndices])

  // ── 拖拽移动（落区处理） ──────────────────────────────────────────

  const handleMoveDrop = useCallback(
    (targetDirPath: string) => {
      const p = dragPayload
      setDragPayload(null)
      setDragOverPath(null)
      if (!p || !onPathChange) return
      if (p.kind === 'file') {
        onPathChange(p.index, moveFileToDir(p.path, targetDirPath))
      } else {
        if (isSelfOrDescendant(p.dirPath, targetDirPath)) return
        const newDir = moveDirToDir(p.dirPath, targetDirPath)
        for (const f of p.subtree) {
          const rel = f.path.substring(p.dirPath.length)
          onPathChange(f.index, newDir + rel)
        }
        // 同步移动 emptyDirs 中的子空目录
        setEmptyDirs((prev) =>
          prev.map((d) => {
            if (d === p.dirPath) return newDir
            if (d.startsWith(`${p.dirPath}/`)) return newDir + d.substring(p.dirPath.length)
            return d
          }),
        )
      }
    },
    [dragPayload, onPathChange],
  )

  // ── 拖拽上传（外部文件拖入） ──────────────────────────────────────

  const handleDrop = useCallback(
    async (e: DragEvent<HTMLDivElement>, targetDirPath: string) => {
      e.preventDefault()
      e.stopPropagation()
      setDropActive(false)
      if (readonly) return
      // 内部拖拽移动优先
      if (dragPayload) {
        handleMoveDrop(targetDirPath)
        return
      }
      // 外部文件拖入
      if (!resolveDrop) return
      const dropped = Array.from(e.dataTransfer.files ?? [])
      if (dropped.length === 0) return
      setResolving(true)
      try {
        const units = await resolveDrop(dropped, targetDirPath)
        await applyIncoming(units)
      } finally {
        setResolving(false)
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps -- applyIncoming 在下方定义，调用时已存在
    [readonly, dragPayload, resolveDrop, handleMoveDrop],
  )

  /** 处理 incoming 单元：检测冲突 → 弹窗 / 直接添加。 */
  const applyIncoming = useCallback(
    async (units: LocalUnit[]) => {
      if (units.length === 0) return
      const existingPaths = files.map((f) => normalizeManifestPath(f.path))
      const incomingPaths = units.map((u) => u.path)
      const conflicts = detectConflicts(incomingPaths, existingPaths)
      if (conflicts.length === 0) {
        onAddFiles?.(units)
        return
      }
      setPendingConflicts({ units, existingPaths, conflicts })
    },
    [files, onAddFiles],
  )

  /** 应用冲突解决（批量）。 */
  const resolveConflicts = useCallback(
    (resolution: ConflictResolution) => {
      const pc = pendingConflicts
      setPendingConflicts(null)
      if (!pc) return
      const result: LocalUnit[] = []
      const toRemove: number[] = []
      const allPaths = [...pc.existingPaths]
      for (const unit of pc.units) {
        const isConflict = pc.existingPaths.includes(unit.path)
        if (!isConflict) {
          result.push(unit)
          allPaths.push(unit.path)
          continue
        }
        if (resolution === 'skip') continue
        if (resolution === 'replace') {
          const idx = files.findIndex((f) => normalizeManifestPath(f.path) === unit.path)
          if (idx >= 0) toRemove.push(idx)
          result.push(unit)
          allPaths.push(unit.path)
        } else {
          // keepBoth
          const newPath = keepBothPath(unit.path, allPaths)
          result.push({ ...unit, path: newPath })
          allPaths.push(newPath)
        }
      }
      if (toRemove.length > 0) {
        if (onRemoveMultiple) onRemoveMultiple(toRemove)
        else toRemove.forEach((i) => onRemove?.(i))
      }
      if (result.length > 0) onAddFiles?.(result)
    },
    [pendingConflicts, files, onRemoveMultiple, onRemove, onAddFiles],
  )

  // ── Context 值 ────────────────────────────────────────────────────

  const ctx: ExplorerContextValue = {
    readonly,
    t,
    dragPayload,
    setDragPayload,
    dragOverPath,
    setDragOverPath,
    selectedIndices,
    onFileClick: handleFileClick,
    collapsedPaths,
    toggleDir,
    renamingTarget,
    startRename,
    commitRename,
    cancelRename,
    openContextMenu,
    onMoveDrop: handleMoveDrop,
    resolveDrop,
    handleExternalDrop: (dropped: File[], targetDirPath: string) => {
      setResolving(true)
      resolveDrop?.(dropped, targetDirPath)
        .then((units) => applyIncoming(units))
        .finally(() => setResolving(false))
    },
    onPathChange,
    onSyncChange,
    onPlatformChange,
  }

  if (files.length === 0 && emptyDirs.length === 0) {
    return (
      <div className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">
        {t('fileExplorer.empty', '暂无文件，拖拽文件/文件夹/ZIP 到此处或添加文件开始')}
      </div>
    )
  }

  return (
    <ExplorerContext.Provider value={ctx}>
      <div className="space-y-2" data-testid="file-explorer-root">
        {!readonly && (
          <Toolbar
            onCreateFolder={() => createFolder(currentSelectedDir(selectedIndices, tree))}
            onDelete={() => {
              if (selectedIndices.size > 0) {
                setDeleteConfirm({ indices: Array.from(selectedIndices), dirPaths: [] })
              }
            }}
            onExpandAll={expandAll}
            onCollapseAll={collapseAll}
            disableDelete={selectedIndices.size === 0}
            t={t}
          />
        )}

        <div
          className={cn(
            'rounded-lg border bg-card/30 p-1 text-sm transition-colors',
            !readonly && (dropActive || dragOverPath !== null) && 'ring-2 ring-primary/50',
          )}
          data-testid="fe-tree-root"
          onDragOver={(e) => {
            if (readonly) return
            // 内部拖拽移动或外部文件拖入都需 preventDefault 才能触发 drop
            if (dragPayload || (resolveDrop && e.dataTransfer.types.includes('Files'))) {
              e.preventDefault()
              if (!dragPayload) setDropActive(true)
              if (dragPayload) setDragOverPath('')
            }
          }}
          onDragLeave={(e) => {
            if (!e.currentTarget.contains(e.relatedTarget as Node | null)) {
              setDropActive(false)
              setDragOverPath((prev) => (prev === '' ? null : prev))
            }
          }}
          onDrop={(e) => {
            if (readonly) return
            // 落到根（非目录区域）
            if (dragPayload) {
              handleMoveDrop('')
            } else if (resolveDrop && e.dataTransfer.files.length > 0) {
              void handleDrop(e, '')
            } else {
              e.preventDefault()
            }
          }}
        >
          <TreeLevel dir={tree} depth={0} />
        </div>

        {resolving && (
          <p className="text-xs text-muted-foreground animate-pulse">{t('fileExplorer.processing', '处理中…')}</p>
        )}
      </div>

      {/* 右键菜单 */}
      {contextMenu && (
        <ContextMenu
          x={contextMenu.x}
          y={contextMenu.y}
          target={contextMenu.target}
          onRename={() => {
            const tg = contextMenu.target
            setContextMenu(null)
            if (tg.kind === 'file') startRename({ kind: 'file', index: tg.index, path: tg.path })
            else if (tg.kind === 'dir') startRename({ kind: 'dir', dirPath: tg.dirPath })
          }}
          onDelete={() => {
            const tg = contextMenu.target
            setContextMenu(null)
            requestDelete(tg)
          }}
          onNewFolder={() => {
            const tg = contextMenu.target
            setContextMenu(null)
            createFolder(tg.kind === 'dir' ? tg.dirPath : '')
          }}
          t={t}
        />
      )}

      {/* 删除确认 */}
      <DangerConfirm
        open={deleteConfirm !== null}
        title={t('fileExplorer.deleteConfirmTitle', '确认删除？')}
        description={t('fileExplorer.deleteConfirmDesc', '将删除选中的 {{n}} 个文件，此操作不可撤销。', {
          n: deleteConfirm?.indices.length ?? 0,
        })}
        confirmLabel={t('common.delete', '删除')}
        onConfirm={confirmDelete}
        onCancel={() => setDeleteConfirm(null)}
      />

      {/* 同名冲突弹窗 */}
      <Dialog open={pendingConflicts !== null} onOpenChange={(v) => { if (!v) setPendingConflicts(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('fileExplorer.conflictTitle', '同名文件冲突')}</DialogTitle>
            <DialogDescription>
              {t('fileExplorer.conflictDesc', '以下文件与已有文件同名，请选择处理方式（可批量应用）。')}
            </DialogDescription>
          </DialogHeader>
          <ul className="max-h-48 space-y-1 overflow-y-auto rounded-md border p-2 text-xs font-mono">
            {pendingConflicts?.conflicts.map((p) => (
              <li key={p} className="truncate text-muted-foreground">{p}</li>
            ))}
          </ul>
          <DialogFooter className="flex-col gap-2 sm:flex-row">
            <Button
              variant="outline"
              className="flex-1"
              data-testid="fe-conflict-skip-all"
              onClick={() => resolveConflicts('skip')}
            >
              {t('fileExplorer.conflictSkipAll', '全部忽略')}
            </Button>
            <Button
              variant="outline"
              className="flex-1"
              data-testid="fe-conflict-replace-all"
              onClick={() => resolveConflicts('replace')}
            >
              {t('fileExplorer.conflictReplaceAll', '全部覆盖')}
            </Button>
            <Button
              variant="default"
              className="flex-1"
              data-testid="fe-conflict-keep-both-all"
              onClick={() => resolveConflicts('keepBoth')}
            >
              {t('fileExplorer.conflictKeepBothAll', '全部保留两者')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </ExplorerContext.Provider>
  )
}

// ── 工具栏 ────────────────────────────────────────────────────────────

function Toolbar({
  onCreateFolder,
  onDelete,
  onExpandAll,
  onCollapseAll,
  disableDelete,
  t,
}: {
  onCreateFolder: () => void
  onDelete: () => void
  onExpandAll: () => void
  onCollapseAll: () => void
  disableDelete: boolean
  t: TFunction
}) {
  return (
    <div className="flex items-center gap-1.5">
      <Button variant="outline" size="sm" onClick={onCreateFolder} data-testid="fe-new-folder">
        <FolderPlus className="size-4" />
        {t('fileExplorer.newFolder', '新建文件夹')}
      </Button>
      <Button variant="outline" size="sm" onClick={onDelete} disabled={disableDelete} data-testid="fe-delete">
        <Trash2 className="size-4" />
        {t('common.delete', '删除')}
      </Button>
      <div className="ml-auto flex items-center gap-1.5">
        <Button variant="ghost" size="sm" onClick={onExpandAll} data-testid="fe-expand-all">
          <ChevronRight className="size-4 rotate-90" />
          {t('fileExplorer.expandAll', '展开全部')}
        </Button>
        <Button variant="ghost" size="sm" onClick={onCollapseAll} data-testid="fe-collapse-all">
          <ChevronRight className="size-4" />
          {t('fileExplorer.collapseAll', '折叠全部')}
        </Button>
      </div>
    </div>
  )
}

// ── 右键菜单 ──────────────────────────────────────────────────────────

function ContextMenu({
  x,
  y,
  target,
  onRename,
  onDelete,
  onNewFolder,
  t,
}: {
  x: number
  y: number
  target: ContextTarget
  onRename: () => void
  onDelete: () => void
  onNewFolder: () => void
  t: TFunction
}) {
  const isRoot = target.kind === 'root'
  return (
    <div
      className="fixed z-50 min-w-[140px] rounded-md border bg-popover p-1 shadow-md"
      style={{ left: x, top: y }}
      data-testid="fe-context-menu"
      onClick={(e) => e.stopPropagation()}
    >
      <button
        type="button"
        className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-accent"
        onClick={onNewFolder}
      >
        <FolderPlus className="size-4" />
        {t('fileExplorer.newFolder', '新建文件夹')}
      </button>
      {!isRoot && (
        <>
          <button
            type="button"
            className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-accent"
            onClick={onRename}
          >
            <Pencil className="size-4" />
            {t('fileExplorer.rename', '重命名')}
          </button>
          <button
            type="button"
            className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm text-destructive hover:bg-destructive/10"
            onClick={onDelete}
          >
            <Trash2 className="size-4" />
            {t('common.delete', '删除')}
          </button>
        </>
      )}
    </div>
  )
}

// ── 树渲染 ────────────────────────────────────────────────────────────

interface LevelProps {
  dir: TreeDir
  depth: number
}

/** 渲染一个目录层级的子目录 + 直属文件。 */
function TreeLevel({ dir, depth }: LevelProps) {
  return (
    <ul className="space-y-0.5">
      {dir.dirs.map((d) => (
        <li key={d.path}>
          <DirRow dir={d} depth={depth} />
        </li>
      ))}
      {dir.files.map((f) => (
        <li key={`${f.index}-${f.path}`}>
          <FileRow file={f} depth={depth} />
        </li>
      ))}
    </ul>
  )
}

/** 可折叠目录行（头 = 折叠箭头 + 文件夹图标 + 名 + 规模；支持拖拽/右键/重命名/多选 drop）。 */
function DirRow({ dir, depth }: { dir: TreeDir; depth: number }) {
  const ctx = useContext(ExplorerContext)!
  const { t, readonly, collapsedPaths, toggleDir, renamingTarget } = ctx
  const collapsed = collapsedPaths.has(dir.path)
  const isOver = ctx.dragOverPath === dir.path
  const isRenaming = renamingTarget?.kind === 'dir' && renamingTarget.dirPath === dir.path
  const [editName, setEditName] = useState(dir.name)

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 重命名开始时同步目录名到编辑框
    if (isRenaming) setEditName(dir.name)
  }, [isRenaming, dir.name])

  return (
    <>
      <div
        className={cn(
          'flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left transition-[background-color]',
          !readonly && 'hover:bg-accent',
          isOver && 'ring-2 ring-primary/50 bg-primary/5',
        )}
        style={{ paddingLeft: `${depth * 1.25 + 0.5}rem` }}
        onContextMenu={(e) => ctx.openContextMenu(e, { kind: 'dir', dirPath: dir.path })}
      >
        {!readonly && (
          <GripVertical
            className="size-3.5 shrink-0 cursor-grab text-muted-foreground/40 hover:text-muted-foreground active:cursor-grabbing"
            data-testid="fe-drag-handle-dir"
            data-dir-path={dir.path}
          />
        )}
        <div
          className="flex flex-1 items-center gap-1.5 text-left"
          onClick={() => !readonly && toggleDir(dir.path)}
          draggable={!readonly}
          onDragStart={(e) => {
            if (readonly) return
            e.dataTransfer.effectAllowed = 'move'
            ctx.setDragPayload({ kind: 'dir', dirPath: dir.path, subtree: collectSubtreeFiles(dir) })
          }}
          onDragEnd={() => ctx.setDragPayload(null)}
          onDragOver={(e) => {
            if (readonly) return
            // 内部拖拽移动或外部文件拖入都需 preventDefault 才能触发 drop
            if (ctx.dragPayload || (ctx.resolveDrop && e.dataTransfer.types.includes('Files'))) {
              e.preventDefault()
              e.stopPropagation()
              ctx.setDragOverPath(dir.path)
            }
          }}
          onDragLeave={(e) => {
            if (!e.currentTarget.contains(e.relatedTarget as Node | null)) {
              if (ctx.dragOverPath === dir.path) ctx.setDragOverPath(null)
            }
          }}
          onDrop={(e) => {
            if (readonly) return
            e.preventDefault()
            e.stopPropagation()
            if (ctx.dragPayload) {
              // 内部拖拽移动
              ctx.setDragOverPath(null)
              const p = ctx.dragPayload
              ctx.setDragPayload(null)
              if (p.kind === 'file') {
                ctx.onPathChange?.(p.index, moveFileToDir(p.path, dir.path))
              } else {
                if (isSelfOrDescendant(p.dirPath, dir.path)) return
                const newDir = moveDirToDir(p.dirPath, dir.path)
                for (const f of p.subtree) {
                  const rel = f.path.substring(p.dirPath.length)
                  ctx.onPathChange?.(f.index, newDir + rel)
                }
              }
            } else if (ctx.resolveDrop && e.dataTransfer.files.length > 0) {
              // 外部文件拖入目录
              ctx.setDragOverPath(null)
              ctx.handleExternalDrop(Array.from(e.dataTransfer.files), dir.path)
            }
          }}
          onDoubleClick={() => !readonly && ctx.startRename({ kind: 'dir', dirPath: dir.path })}
          data-testid={readonly ? undefined : 'fe-dir-row'}
          data-dir-path={dir.path}
        >
          <ChevronRight className={cn('size-3.5 shrink-0 text-muted-foreground transition-transform', !collapsed && 'rotate-90')} />
          {collapsed ? (
            <Folder className="size-4 shrink-0 text-amber-500" />
          ) : (
            <FolderOpen className="size-4 shrink-0 text-amber-500" />
          )}
          {isRenaming ? (
            <input
              autoFocus
              className="w-full rounded border bg-background p-0.5 px-1 text-sm"
              value={editName}
              data-testid="fe-rename-input"
              onChange={(e) => setEditName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') ctx.commitRename(editName)
                if (e.key === 'Escape') ctx.cancelRename()
              }}
              onBlur={() => ctx.commitRename(editName)}
              onClick={(e) => e.stopPropagation()}
            />
          ) : (
            <span className="font-medium">{dir.name}</span>
          )}
          {!isRenaming && (
            <span className="ml-auto shrink-0 text-xs text-muted-foreground">
              {t('clientVersions.treeDirSummary', '{{n}} 个文件 · {{size}}', {
                n: dir.fileCount,
                size: formatBytes(dir.totalSize),
              })}
            </span>
          )}
        </div>
      </div>
      {!collapsed && <TreeLevel dir={dir} depth={depth + 1} />}
    </>
  )
}

/** 文件行：编排态显示路径/sync/platform 控件 + 删除；只读态显示徽标。 */
function FileRow({ file, depth }: { file: TreeFile; depth: number }) {
  const ctx = useContext(ExplorerContext)!
  const { t, readonly, selectedIndices, renamingTarget } = ctx
  const isSelected = selectedIndices.has(file.index)
  const isRenaming = renamingTarget?.kind === 'file' && renamingTarget.index === file.index
  const [editName, setEditName] = useState(file.name)

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 重命名开始时同步文件名到编辑框
    if (isRenaming) setEditName(file.name)
  }, [isRenaming, file.name])

  const pad = { paddingLeft: `${depth * 1.25 + 0.5}rem` }

  if (readonly) {
    return (
      <div className="flex items-center gap-2 rounded-md px-2 py-1.5" style={pad}>
        <File className="size-4 shrink-0 text-muted-foreground" />
        <span className="font-mono text-xs truncate">{file.name}</span>
        <span className="ml-auto flex shrink-0 items-center gap-1.5">
          <SyncBadge sync={file.sync} t={t} />
          {file.platform && <Badge variant="outline" className="text-[10px]">{file.platform}</Badge>}
          <span className="text-xs text-muted-foreground whitespace-nowrap">{formatBytes(file.size)}</span>
        </span>
      </div>
    )
  }

  return (
    <div
      className={cn(
        'flex flex-col gap-2 rounded-md px-2 py-2 transition-[background-color] sm:flex-row sm:items-center',
        isSelected ? 'bg-primary/10 ring-1 ring-primary/30' : 'hover:bg-accent/50',
        ctx.dragOverPath === '' && 'ring-2 ring-primary/50',
      )}
      style={pad}
      draggable
      onDragStart={(e) => {
        e.dataTransfer.effectAllowed = 'move'
        ctx.setDragPayload({ kind: 'file', index: file.index, path: file.path })
      }}
      onDragEnd={() => ctx.setDragPayload(null)}
      onClick={(e) => ctx.onFileClick(file.index, e)}
      onContextMenu={(e) => ctx.openContextMenu(e, { kind: 'file', index: file.index, path: file.path })}
      onDoubleClick={() => ctx.startRename({ kind: 'file', index: file.index, path: file.path })}
      data-testid="fe-file-row"
      data-file-index={file.index}
      data-selected={isSelected}
    >
      <div className="flex min-w-0 flex-1 items-center gap-2">
        <GripVertical className="size-3.5 shrink-0 cursor-grab text-muted-foreground/40 hover:text-muted-foreground active:cursor-grabbing" data-testid="fe-drag-handle-file" />
        <File className="size-4 shrink-0 text-muted-foreground" />
        {isRenaming ? (
          <input
            autoFocus
            className="w-full max-w-[280px] rounded border bg-background p-1 font-mono text-xs"
            value={editName}
            data-testid="fe-rename-input"
            onChange={(e) => setEditName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') ctx.commitRename(editName)
              if (e.key === 'Escape') ctx.cancelRename()
            }}
            onBlur={() => ctx.commitRename(editName)}
            onClick={(e) => e.stopPropagation()}
          />
        ) : (
          <>
            <span className="truncate font-mono text-xs">{file.path}</span>
            <span
              className="inline-flex shrink-0 items-center gap-1 text-[10px] text-muted-foreground"
              title={t('clientVersions.contentLocked', '内容已锁定（内容寻址，不可修改字节，仅可编排路径/策略或移除）')}
            >
              <Lock className="size-3" />
              {t('clientVersions.locked', '锁定')}
            </span>
          </>
        )}
      </div>
      {!isRenaming && (
        <div className="flex shrink-0 items-center gap-2">
          <Select value={file.sync} onValueChange={(v: string) => ctx.onSyncChange?.(file.index, v as ManifestFileLike['sync'])}>
            <SelectTrigger size="sm" className="w-28"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="strict">{t('clientVersions.syncStrict', '覆盖')}</SelectItem>
              <SelectItem value="once">{t('clientVersions.syncOnce', '仅一次')}</SelectItem>
              <SelectItem value="ignore">{t('clientVersions.syncIgnore', '忽略')}</SelectItem>
            </SelectContent>
          </Select>
          <Select
            value={file.platform === '' ? PLATFORM_ALL : file.platform}
            onValueChange={(v: string) =>
              ctx.onPlatformChange?.(file.index, (v === PLATFORM_ALL ? '' : v) as ManifestFileLike['platform'])
            }
          >
            <SelectTrigger size="sm" className="w-28"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value={PLATFORM_ALL}>{t('clientVersions.platformAll', '全平台')}</SelectItem>
              <SelectItem value="windows">windows</SelectItem>
              <SelectItem value="macos">macos</SelectItem>
              <SelectItem value="linux">linux</SelectItem>
            </SelectContent>
          </Select>
          <span className="text-xs text-muted-foreground whitespace-nowrap">{formatBytes(file.size)}</span>
        </div>
      )}
    </div>
  )
}

/** 同步策略徽标（只读预览用）。 */
function SyncBadge({ sync, t }: { sync: ManifestFileLike['sync']; t: TFunction }) {
  const tone =
    sync === 'strict'
      ? 'border-primary/40 text-primary'
      : sync === 'once'
        ? 'border-amber-500/40 text-amber-600 dark:text-amber-500'
        : 'border-muted-foreground/30 text-muted-foreground'
  const label =
    sync === 'strict'
      ? t('clientVersions.syncStrict', '覆盖')
      : sync === 'once'
        ? t('clientVersions.syncOnce', '仅一次')
        : t('clientVersions.syncIgnore', '忽略')
  return <Badge variant="outline" className={cn('text-[10px]', tone)}>{label}</Badge>
}

// ── 辅助函数 ──────────────────────────────────────────────────────────

/** 递归收集可见文件（按视觉顺序，供 Shift 连选）。 */
function collectVisibleFiles(dir: TreeDir, collapsedPaths: Set<string>): TreeFile[] {
  const out: TreeFile[] = []
  for (const d of dir.dirs) {
    if (!collapsedPaths.has(d.path)) {
      out.push(...collectVisibleFiles(d, collapsedPaths))
    }
  }
  out.push(...dir.files)
  return out
}

/** 收集树中所有目录路径（供折叠全部）。 */
function collectAllDirPaths(dir: TreeDir): string[] {
  const out: string[] = []
  for (const d of dir.dirs) {
    out.push(d.path)
    out.push(...collectAllDirPaths(d))
  }
  return out
}

/** 在树中查找指定路径的目录节点。 */
function findDirNode(root: TreeDir, path: string): TreeDir | null {
  if (path === '') return root
  const segs = path.split('/').filter((s) => s !== '')
  let cursor = root
  for (const seg of segs) {
    const child = cursor.dirs.find((d) => d.name === seg)
    if (!child) return null
    cursor = child
  }
  return cursor
}

/** 当前选中文件所在的目录路径（新建文件夹时用）。 */
function currentSelectedDir(selectedIndices: Set<number>, tree: TreeDir): string {
  if (selectedIndices.size === 0) return ''
  const firstIdx = selectedIndices.values().next().value
  if (firstIdx === undefined) return ''
  // 在树中找该 index 的文件，取其父目录
  const found = findFileInTree(tree, firstIdx)
  if (!found) return ''
  const slash = found.lastIndexOf('/')
  return slash >= 0 ? found.slice(0, slash) : ''
}

/** 在树中递归查找指定 index 的文件路径。 */
function findFileInTree(dir: TreeDir, index: number): string | null {
  for (const f of dir.files) {
    if (f.index === index) return f.path
  }
  for (const d of dir.dirs) {
    const found = findFileInTree(d, index)
    if (found) return found
  }
  return null
}
