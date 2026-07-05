import { createContext, useContext, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { ChevronRight, File, Folder, FolderOpen, GripVertical, Lock, Trash2 } from 'lucide-react'
import {
  buildFileTree,
  collectSubtreeFiles,
  isSelfOrDescendant,
  moveDirToDir,
  moveFileToDir,
  type ManifestFileLike,
  type TreeDir,
  type TreeFile,
} from '@/lib/client-publish-wizard'
import { cn } from '@jianmanager/ui'
import { Badge } from '@jianmanager/ui/components/badge'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'

/** 平台「全部」哨兵（Radix Select 不允许空字符串值，回写时映射回 ""）。 */
const PLATFORM_ALL = '__all__'

/** 字节数转人类可读。 */
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

// ── FR-254：文件树拖拽编排 ──────────────────────────────────────────────

/** 拖拽载荷：文件节点或目录节点。 */
type DragPayload =
  | { kind: 'file'; index: number; path: string }
  | { kind: 'dir'; dirPath: string; subtree: { index: number; path: string }[] }

/** 拖拽上下文（编排态内组件树共享，避免逐层 prop 传递）。 */
interface DragContextValue {
  payload: DragPayload | null
  overPath: string | null
  setPayload: (p: DragPayload | null) => void
  setOverPath: (p: string | null) => void
}

const DragContext = createContext<DragContextValue | null>(null)

/**
 * 客户端分发文件树（FR-191，FR-254 拖拽编排）。
 * 把扁平 `ManifestFile[]` 按 `path` 目录层级渲染为 Minecraft 风格的文件树预览。
 * - `readonly`（审阅/版本详情）：纯展示，文件行显示 名称 + sync/platform 徽标 + 大小，目录可折叠。
 * - 编排态（配置步）：文件行可改目标路径 / sync / platform / 删除；内容锁定（内容寻址不可改字节）。
 *   FR-254：编排态支持**拖拽移动**文件/目录节点到其他目录，批量改目标路径。
 * 编排回调按文件在源数组中的 `index` 定位，父组件据此 patch/remove 原数组项。
 */
export interface ClientFileTreeProps {
  /** 构树所需的文件列表（与 ManifestFile 兼容的最小形态）。 */
  files: ManifestFileLike[]
  /** 只读预览（审阅/详情）。省略或 false 时为编排态。 */
  readonly?: boolean
  /** 编排：改某文件（按源数组 index）的目标路径。 */
  onPathChange?: (index: number, path: string) => void
  /** 编排：改某文件的同步策略。 */
  onSyncChange?: (index: number, sync: ManifestFileLike['sync']) => void
  /** 编排：改某文件的适用平台。 */
  onPlatformChange?: (index: number, platform: ManifestFileLike['platform']) => void
  /** 编排：移除某文件。 */
  onRemove?: (index: number) => void
}

/** 文件树根。空列表显示占位。 */
export default function ClientFileTree({
  files,
  readonly = false,
  onPathChange,
  onSyncChange,
  onPlatformChange,
  onRemove,
}: ClientFileTreeProps) {
  const { t } = useTranslation()
  const tree = useMemo(() => buildFileTree(files), [files])

  // FR-254：拖拽编排态（仅编排态启用）。
  const [dragPayload, setDragPayload] = useState<DragPayload | null>(null)
  const [dragOverPath, setDragOverPath] = useState<string | null>(null)
  const drag: DragContextValue = {
    payload: dragPayload,
    overPath: dragOverPath,
    setPayload: setDragPayload,
    setOverPath: setDragOverPath,
  }

  if (files.length === 0) {
    return (
      <div className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">
        {t('clientVersions.treeEmpty', '暂无文件')}
      </div>
    )
  }

  return (
    <DragContext.Provider value={drag}>
      <div
        className={cn(
          'rounded-lg border bg-card/30 p-1 text-sm',
          !readonly && dragOverPath === '' && 'ring-2 ring-primary/50',
        )}
        data-testid="tree-root"
        onDragOver={(e) => {
          if (readonly || !drag.payload) return
          // 仅在落到非目录区域时标为根放置目标（目录自身的 onDragOver 会 stopPropagation）。
          e.preventDefault()
          setDragOverPath('')
        }}
        onDragLeave={(e) => {
          // 离开容器边界才清（子元素的 dragenter 冒泡会触发 dragleave）。
          if (!e.currentTarget.contains(e.relatedTarget as Node | null)) {
            setDragOverPath((prev) => (prev === '' ? null : prev))
          }
        }}
        onDrop={(e) => {
          if (readonly) return
          e.preventDefault()
          handleDrop(drag, '', onPathChange)
        }}
      >
        {/* 根目录的散文件与子目录（根本身不渲染目录头） */}
        <TreeLevel
          dir={tree}
          depth={0}
          readonly={readonly}
          onPathChange={onPathChange}
          onSyncChange={onSyncChange}
          onPlatformChange={onPlatformChange}
          onRemove={onRemove}
        />
      </div>
    </DragContext.Provider>
  )
}

/** 处理拖放：把载荷（文件或目录）移动到目标目录，调用 onPathChange 批量改路径。 */
function handleDrop(
  drag: DragContextValue,
  targetDirPath: string,
  onPathChange?: (index: number, path: string) => void,
) {
  const p = drag.payload
  drag.setPayload(null)
  drag.setOverPath(null)
  if (!p || !onPathChange) return
  if (p.kind === 'file') {
    onPathChange(p.index, moveFileToDir(p.path, targetDirPath))
  } else {
    // 目录拖到自身或子目录 → 非法，忽略。
    if (isSelfOrDescendant(p.dirPath, targetDirPath)) return
    const newDir = moveDirToDir(p.dirPath, targetDirPath)
    for (const f of p.subtree) {
      // f.path 形如 "config/foo/x.txt"，p.dirPath="config/foo" → rel="/x.txt"
      const rel = f.path.substring(p.dirPath.length)
      onPathChange(f.index, newDir + rel)
    }
  }
}

interface LevelProps {
  dir: TreeDir
  depth: number
  readonly: boolean
  onPathChange?: (index: number, path: string) => void
  onSyncChange?: (index: number, sync: ManifestFileLike['sync']) => void
  onPlatformChange?: (index: number, platform: ManifestFileLike['platform']) => void
  onRemove?: (index: number) => void
}

/** 渲染一个目录层级的子目录 + 直属文件（不含本目录头）。 */
function TreeLevel(props: LevelProps) {
  const { dir, depth, readonly, onPathChange, onSyncChange, onPlatformChange, onRemove } = props
  return (
    <ul className="space-y-0.5">
      {dir.dirs.map((d) => (
        <li key={d.path}>
          <DirRow
            dir={d}
            depth={depth}
            readonly={readonly}
            onPathChange={onPathChange}
            onSyncChange={onSyncChange}
            onPlatformChange={onPlatformChange}
            onRemove={onRemove}
          />
        </li>
      ))}
      {dir.files.map((f) => (
        <li key={`${f.index}-${f.path}`}>
          <FileRow
            file={f}
            depth={depth}
            readonly={readonly}
            onPathChange={onPathChange}
            onSyncChange={onSyncChange}
            onPlatformChange={onPlatformChange}
            onRemove={onRemove}
          />
        </li>
      ))}
    </ul>
  )
}

/** 可折叠目录行（头 = 折叠箭头 + 文件夹图标 + 名 + 子树规模徽标）。 */
function DirRow({ dir, depth, ...rest }: LevelProps) {
  const { t } = useTranslation()
  const { onPathChange, readonly } = rest
  const drag = useContext(DragContext)
  const [open, setOpen] = useState(true)
  const isOver = drag?.overPath === dir.path
  return (
    <>
      <div
        className={cn(
          'flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left transition-[background-color]',
          !readonly && 'hover:bg-accent',
          isOver && 'ring-2 ring-primary/50 bg-primary/5',
        )}
        style={{ paddingLeft: `${depth * 1.25 + 0.5}rem` }}
      >
        {!readonly && (
          <GripVertical
            className="size-3.5 shrink-0 cursor-grab text-muted-foreground/40 hover:text-muted-foreground active:cursor-grabbing"
            data-testid="drag-handle-dir"
            data-dir-path={dir.path}
          />
        )}
        <button
          type="button"
          className="flex flex-1 items-center gap-1.5 text-left"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          draggable={!readonly}
          onDragStart={(e) => {
            if (readonly) return
            e.dataTransfer.effectAllowed = 'move'
            drag?.setPayload({ kind: 'dir', dirPath: dir.path, subtree: collectSubtreeFiles(dir) })
          }}
          onDragEnd={() => drag?.setPayload(null)}
          onDragOver={(e) => {
            if (readonly || !drag?.payload) return
            e.preventDefault()
            e.stopPropagation()
            drag.setOverPath(dir.path)
          }}
          onDragLeave={(e) => {
            // 离开目录头才清（移动到子元素时 relatedTarget 仍在容器外，正常清除）。
            if (!e.currentTarget.contains(e.relatedTarget as Node | null)) {
              if (drag?.overPath === dir.path) drag.setOverPath(null)
            }
          }}
          onDrop={(e) => {
            if (readonly) return
            e.preventDefault()
            e.stopPropagation()
            handleDrop(drag!, dir.path, onPathChange)
          }}
          data-testid={readonly ? undefined : 'dir-row'}
          data-dir-path={dir.path}
        >
          <ChevronRight className={cn('size-3.5 shrink-0 text-muted-foreground transition-transform', open && 'rotate-90')} />
          {open ? (
            <FolderOpen className="size-4 shrink-0 text-amber-500" />
          ) : (
            <Folder className="size-4 shrink-0 text-amber-500" />
          )}
          <span className="font-medium">{dir.name}</span>
          <span className="ml-auto shrink-0 text-xs text-muted-foreground">
            {t('clientVersions.treeDirSummary', '{{n}} 个文件 · {{size}}', {
              n: dir.fileCount,
              size: formatBytes(dir.totalSize),
            })}
          </span>
        </button>
      </div>
      {open && (
        <TreeLevel dir={dir} depth={depth + 1} {...rest} />
      )}
    </>
  )
}

/** 文件行：只读态显示徽标，编排态显示路径/sync/platform 控件 + 删除。 */
function FileRow({
  file,
  depth,
  readonly,
  onPathChange,
  onSyncChange,
  onPlatformChange,
  onRemove,
}: {
  file: TreeFile
  depth: number
  readonly: boolean
  onPathChange?: (index: number, path: string) => void
  onSyncChange?: (index: number, sync: ManifestFileLike['sync']) => void
  onPlatformChange?: (index: number, platform: ManifestFileLike['platform']) => void
  onRemove?: (index: number) => void
}) {
  const { t } = useTranslation()
  const drag = useContext(DragContext)
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
        'flex flex-col gap-2 rounded-md px-2 py-2 hover:bg-accent/50 transition-[background-color] sm:flex-row sm:items-center',
        drag?.overPath === '' && 'ring-2 ring-primary/50',
      )}
      style={pad}
      draggable
      onDragStart={(e) => {
        e.dataTransfer.effectAllowed = 'move'
        drag?.setPayload({ kind: 'file', index: file.index, path: file.path })
      }}
      onDragEnd={() => drag?.setPayload(null)}
      data-testid={readonly ? undefined : 'file-row'}
      data-file-index={file.index}
    >
      <div className="flex min-w-0 flex-1 items-center gap-2">
        <GripVertical
          className="size-3.5 shrink-0 cursor-grab text-muted-foreground/40 hover:text-muted-foreground active:cursor-grabbing"
          data-testid="drag-handle-file"
        />
        <File className="size-4 shrink-0 text-muted-foreground" />
        <input
          className="w-full rounded border bg-background p-1.5 font-mono text-xs aria-invalid:border-destructive"
          value={file.path}
          aria-invalid={file.path.trim() === '' || file.path.startsWith('/') || file.path.includes('..')}
          onChange={(e) => onPathChange?.(file.index, e.target.value)}
          aria-label={t('clientVersions.path', '路径')}
        />
        <span
          className="inline-flex shrink-0 items-center gap-1 text-[10px] text-muted-foreground"
          title={t('clientVersions.contentLocked', '内容已锁定（内容寻址，不可修改字节，仅可编排路径/策略或移除）')}
        >
          <Lock className="size-3" />
          {t('clientVersions.locked', '锁定')}
        </span>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        <Select value={file.sync} onValueChange={(v: string) => onSyncChange?.(file.index, v as ManifestFileLike['sync'])}>
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
            onPlatformChange?.(file.index, (v === PLATFORM_ALL ? '' : v) as ManifestFileLike['platform'])
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
        <button
          type="button"
          className="text-destructive hover:opacity-70"
          onClick={() => onRemove?.(file.index)}
          aria-label={t('common.delete', '删除')}
        >
          <Trash2 className="size-4" />
        </button>
      </div>
    </div>
  )
}

/** 同步策略徽标（只读预览用，配色区分 strict/once/ignore；文案中文化，UI 不显英文裸词）。 */
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
