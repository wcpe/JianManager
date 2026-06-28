import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronRight, File, Folder, FolderOpen, Loader2, MoreVertical } from 'lucide-react'
import { cn } from '@/lib/utils'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import type { FileBrowserAction, FileBrowserSource, FileEntry } from './types'
import { buildTree, type BrowserTreeDir, type BrowserTreeFile } from './tree'

/** 字节数转人类可读。 */
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

interface FileBrowserTreeProps {
  source: FileBrowserSource
  /** 当前选中文件 path（高亮）。 */
  selectedPath: string | null
  /** 点击文件 → 选中预览。 */
  onSelectFile: (entry: FileEntry) => void
  /** 行操作（可操作态注入；空则不显示行菜单）。 */
  actions: FileBrowserAction[]
  /** 刷新信号：变化时重拉。 */
  refreshKey?: number
}

/**
 * 共享文件浏览器的目录树（FR-213）。
 *
 * 两种数据源形态统一渲染为可折叠树：
 * - 懒加载分层（`source.flat` 省略）：点目录展开时按需 `source.list(dir)` 拉该层。
 * - 扁平全量（`source.flat===true`）：一次 `source.list('')` 取全部，`buildTree` 内部成树。
 *
 * 文件行点击→选中（右栏预览）；注入 `actions` 时文件行末尾出现行操作菜单（下载/重命名/删除等，
 * 全部由调用方提供）。本组件不含任何写端点。
 */
export default function FileBrowserTree({
  source,
  selectedPath,
  onSelectFile,
  actions,
  refreshKey = 0,
}: FileBrowserTreeProps) {
  const { t } = useTranslation()

  if (source.flat) {
    return (
      <FlatTree
        source={source}
        selectedPath={selectedPath}
        onSelectFile={onSelectFile}
        actions={actions}
        refreshKey={refreshKey}
      />
    )
  }
  return (
    <LazyTree
      source={source}
      selectedPath={selectedPath}
      onSelectFile={onSelectFile}
      actions={actions}
      refreshKey={refreshKey}
      emptyLabel={t('fileBrowser.empty')}
    />
  )
}

// ── 扁平全量：一次拉取 + buildTree ────────────────────────────────────────

function FlatTree({ source, selectedPath, onSelectFile, actions, refreshKey }: FileBrowserTreeProps) {
  const { t } = useTranslation()
  const [entries, setEntries] = useState<FileEntry[] | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let alive = true
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 源/刷新变化时复位为加载态再异步拉取，属合法同步
    setEntries(null)
    setError('')
    source
      .list('')
      .then((es) => alive && setEntries(es))
      .catch((e: unknown) => alive && setError(e instanceof Error ? e.message : t('fileBrowser.loadFailed')))
    return () => {
      alive = false
    }
  }, [source, refreshKey, t])

  const tree = useMemo(() => (entries ? buildTree(entries) : null), [entries])

  if (error) return <p className="p-3 text-sm text-destructive">{error}</p>
  if (!tree) {
    return (
      <p className="flex items-center gap-2 p-3 text-sm text-muted-foreground">
        <Loader2 className="size-4 animate-spin" /> {t('fileBrowser.loading')}
      </p>
    )
  }
  if (tree.fileCount === 0 && tree.dirs.length === 0) {
    return <p className="p-3 text-sm text-muted-foreground">{t('fileBrowser.empty')}</p>
  }

  return (
    <div className="text-sm">
      <StaticLevel
        dir={tree}
        depth={0}
        selectedPath={selectedPath}
        onSelectFile={onSelectFile}
        actions={actions}
      />
    </div>
  )
}

/** 扁平树的一层（子目录 + 直属文件），目录默认展开。 */
function StaticLevel({
  dir,
  depth,
  selectedPath,
  onSelectFile,
  actions,
}: {
  dir: BrowserTreeDir
  depth: number
  selectedPath: string | null
  onSelectFile: (entry: FileEntry) => void
  actions: FileBrowserAction[]
}) {
  return (
    <ul className="space-y-0.5">
      {dir.dirs.map((d) => (
        <li key={d.path}>
          <StaticDirRow
            dir={d}
            depth={depth}
            selectedPath={selectedPath}
            onSelectFile={onSelectFile}
            actions={actions}
          />
        </li>
      ))}
      {dir.files.map((f) => (
        <li key={f.entry.path}>
          <FileRow
            file={f}
            depth={depth}
            selected={selectedPath === f.entry.path}
            onSelect={onSelectFile}
            actions={actions}
          />
        </li>
      ))}
    </ul>
  )
}

function StaticDirRow({
  dir,
  depth,
  selectedPath,
  onSelectFile,
  actions,
}: {
  dir: BrowserTreeDir
  depth: number
  selectedPath: string | null
  onSelectFile: (entry: FileEntry) => void
  actions: FileBrowserAction[]
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(true)
  return (
    <>
      <button
        type="button"
        className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left transition-[background-color] hover:bg-accent"
        style={{ paddingLeft: `${depth * 1.25 + 0.5}rem` }}
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        <ChevronRight
          className={cn('size-3.5 shrink-0 text-muted-foreground transition-transform', open && 'rotate-90')}
        />
        {open ? (
          <FolderOpen className="size-4 shrink-0 text-amber-500" />
        ) : (
          <Folder className="size-4 shrink-0 text-amber-500" />
        )}
        <span className="truncate font-medium">{dir.name}</span>
        <span className="ml-auto shrink-0 text-xs text-muted-foreground">
          {t('fileBrowser.dirSummary', { n: dir.fileCount, size: formatBytes(dir.totalSize) })}
        </span>
      </button>
      {open && (
        <StaticLevel
          dir={dir}
          depth={depth + 1}
          selectedPath={selectedPath}
          onSelectFile={onSelectFile}
          actions={actions}
        />
      )}
    </>
  )
}

// ── 懒加载分层：点目录展开时拉该层 ──────────────────────────────────────

function LazyTree({
  source,
  selectedPath,
  onSelectFile,
  actions,
  refreshKey,
  emptyLabel,
}: FileBrowserTreeProps & { emptyLabel: string }) {
  return (
    <div className="text-sm">
      <LazyDirChildren
        source={source}
        dirPath=""
        depth={0}
        selectedPath={selectedPath}
        onSelectFile={onSelectFile}
        actions={actions}
        refreshKey={refreshKey ?? 0}
        emptyLabel={emptyLabel}
      />
    </div>
  )
}

/** 拉取并渲染某目录的直接子项（懒加载）。 */
function LazyDirChildren({
  source,
  dirPath,
  depth,
  selectedPath,
  onSelectFile,
  actions,
  refreshKey,
  emptyLabel,
}: {
  source: FileBrowserSource
  dirPath: string
  depth: number
  selectedPath: string | null
  onSelectFile: (entry: FileEntry) => void
  actions: FileBrowserAction[]
  refreshKey: number
  emptyLabel: string
}) {
  const { t } = useTranslation()
  const [entries, setEntries] = useState<FileEntry[] | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let alive = true
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 目录/刷新变化时复位为加载态再异步拉该层，属合法同步
    setEntries(null)
    setError('')
    source
      .list(dirPath)
      .then((es) => alive && setEntries(es))
      .catch((e: unknown) => alive && setError(e instanceof Error ? e.message : t('fileBrowser.loadFailed')))
    return () => {
      alive = false
    }
  }, [source, dirPath, refreshKey, t])

  // 目录在前、文件在后，各自字母序。
  const sorted = useMemo(() => {
    if (!entries) return null
    const dirs = entries.filter((e) => e.isDir).sort((a, b) => a.name.localeCompare(b.name))
    const files = entries.filter((e) => !e.isDir).sort((a, b) => a.name.localeCompare(b.name))
    return [...dirs, ...files]
  }, [entries])

  if (error) {
    return (
      <p className="px-2 py-1 text-xs text-destructive" style={{ paddingLeft: `${depth * 1.25 + 0.5}rem` }}>
        {error}
      </p>
    )
  }
  if (!sorted) {
    return (
      <p
        className="flex items-center gap-1.5 px-2 py-1 text-xs text-muted-foreground"
        style={{ paddingLeft: `${depth * 1.25 + 0.5}rem` }}
      >
        <Loader2 className="size-3.5 animate-spin" /> {t('fileBrowser.loading')}
      </p>
    )
  }
  if (sorted.length === 0) {
    return (
      <p className="px-2 py-1 text-xs text-muted-foreground" style={{ paddingLeft: `${depth * 1.25 + 0.5}rem` }}>
        {depth === 0 ? emptyLabel : t('fileBrowser.emptyDir')}
      </p>
    )
  }

  return (
    <ul className="space-y-0.5">
      {sorted.map((e) =>
        e.isDir ? (
          <li key={e.path}>
            <LazyDirRow
              source={source}
              entry={e}
              depth={depth}
              selectedPath={selectedPath}
              onSelectFile={onSelectFile}
              actions={actions}
              refreshKey={refreshKey}
              emptyLabel={emptyLabel}
            />
          </li>
        ) : (
          <li key={e.path}>
            <FileRow
              file={{ entry: e, name: e.name }}
              depth={depth}
              selected={selectedPath === e.path}
              onSelect={onSelectFile}
              actions={actions}
            />
          </li>
        ),
      )}
    </ul>
  )
}

function LazyDirRow({
  source,
  entry,
  depth,
  selectedPath,
  onSelectFile,
  actions,
  refreshKey,
  emptyLabel,
}: {
  source: FileBrowserSource
  entry: FileEntry
  depth: number
  selectedPath: string | null
  onSelectFile: (entry: FileEntry) => void
  actions: FileBrowserAction[]
  refreshKey: number
  emptyLabel: string
}) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button
        type="button"
        className="flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left transition-[background-color] hover:bg-accent"
        style={{ paddingLeft: `${depth * 1.25 + 0.5}rem` }}
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        <ChevronRight
          className={cn('size-3.5 shrink-0 text-muted-foreground transition-transform', open && 'rotate-90')}
        />
        {open ? (
          <FolderOpen className="size-4 shrink-0 text-amber-500" />
        ) : (
          <Folder className="size-4 shrink-0 text-amber-500" />
        )}
        <span className="truncate font-medium">{entry.name}</span>
      </button>
      {open && (
        <LazyDirChildren
          source={source}
          dirPath={entry.path}
          depth={depth + 1}
          selectedPath={selectedPath}
          onSelectFile={onSelectFile}
          actions={actions}
          refreshKey={refreshKey}
          emptyLabel={emptyLabel}
        />
      )}
    </>
  )
}

// ── 文件行（两形态共用）────────────────────────────────────────────────

function FileRow({
  file,
  depth,
  selected,
  onSelect,
  actions,
}: {
  file: BrowserTreeFile
  depth: number
  selected: boolean
  onSelect: (entry: FileEntry) => void
  actions: FileBrowserAction[]
}) {
  const { t } = useTranslation()
  const visibleActions = actions.filter((a) => !a.visible || a.visible(file.entry))
  return (
    <div
      className={cn(
        'group flex items-center gap-2 rounded-md px-2 py-1.5 transition-[background-color] hover:bg-accent/50',
        selected && 'bg-accent',
      )}
      style={{ paddingLeft: `${depth * 1.25 + 0.5}rem` }}
    >
      <button
        type="button"
        className="flex min-w-0 flex-1 items-center gap-2 text-left"
        onClick={() => onSelect(file.entry)}
      >
        <File className="size-4 shrink-0 text-muted-foreground" />
        <span className="truncate font-mono text-xs">{file.name}</span>
        {file.entry.size != null && (
          <span className="ml-auto shrink-0 whitespace-nowrap text-xs text-muted-foreground">
            {formatBytes(file.entry.size)}
          </span>
        )}
      </button>
      {visibleActions.length > 0 && (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              type="button"
              className="shrink-0 text-muted-foreground opacity-0 transition-opacity hover:text-foreground group-hover:opacity-100 data-[state=open]:opacity-100"
              aria-label={t('fileBrowser.actions')}
            >
              <MoreVertical className="size-4" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            {visibleActions.map((a) => (
              <DropdownMenuItem key={a.key} onSelect={() => a.onAction(file.entry)}>
                {a.icon}
                {a.label}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </div>
  )
}
