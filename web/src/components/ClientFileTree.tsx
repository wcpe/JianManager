import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { ChevronRight, File, Folder, FolderOpen, Lock, Trash2 } from 'lucide-react'
import {
  buildFileTree,
  type ManifestFileLike,
  type TreeDir,
  type TreeFile,
} from '@/lib/client-publish-wizard'
import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

/** 平台「全部」哨兵（Radix Select 不允许空字符串值，回写时映射回 ""）。 */
const PLATFORM_ALL = '__all__'

/** 字节数转人类可读。 */
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

/**
 * 客户端分发文件树（FR-191）。
 * 把扁平 `ManifestFile[]` 按 `path` 目录层级渲染为 Minecraft 风格的文件树预览。
 * - `readonly`（审阅/版本详情）：纯展示，文件行显示 名称 + sync/platform 徽标 + 大小，目录可折叠。
 * - 编排态（配置步）：文件行可改目标路径 / sync / platform / 删除；内容锁定（内容寻址不可改字节）。
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

  if (files.length === 0) {
    return (
      <div className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">
        {t('clientVersions.treeEmpty', '暂无文件')}
      </div>
    )
  }

  return (
    <div className="rounded-lg border bg-card/30 p-1 text-sm">
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
  )
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
  const [open, setOpen] = useState(true)
  return (
    <>
      <button
        type="button"
        className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left hover:bg-accent transition-[background-color]"
        style={{ paddingLeft: `${depth * 1.25 + 0.5}rem` }}
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
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
      className="flex flex-col gap-2 rounded-md px-2 py-2 hover:bg-accent/50 transition-[background-color] sm:flex-row sm:items-center"
      style={pad}
    >
      <div className="flex min-w-0 flex-1 items-center gap-2">
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
            <SelectItem value="strict">{t('clientVersions.syncStrict', '强制')}</SelectItem>
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
      ? t('clientVersions.syncStrict', '强制')
      : sync === 'once'
        ? t('clientVersions.syncOnce', '仅一次')
        : t('clientVersions.syncIgnore', '忽略')
  return <Badge variant="outline" className={cn('text-[10px]', tone)}>{label}</Badge>
}
