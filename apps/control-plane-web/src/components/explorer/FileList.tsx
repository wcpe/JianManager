import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Folder,
  FileText,
  FileArchive,
  FileCode2,
  Download,
  Pencil,
  Trash2,
  Scissors,
  Copy,
  Lock,
  ArrowUpDown,
} from 'lucide-react'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import {
  ContextMenu,
  ContextMenuTrigger,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
} from '@jianmanager/ui/components/context-menu'
import type { FileInfo } from '@/api/files'
import { isArchiveName, isClassName } from '@/api/archive'
import type { SelectionState, ClickModifiers } from './selection'
import { isSelected } from './selection'
import { cn } from '@jianmanager/ui'
import {
  sortFiles,
  toggleSort,
  type FileSortState,
  type FileSortKey,
  type FileViewMode,
} from './file-sort'

interface FileListProps {
  files: FileInfo[]
  loading: boolean
  error: string
  selection: SelectionState
  onRowClick: (name: string, mods: ClickModifiers) => void
  onOpen: (file: FileInfo) => void
  /** FR-377：携带 dataTransfer 以便写入跨窗 MIME。 */
  onDragStartItem: (name: string, dt: DataTransfer) => void
  onDropUpload: (files: FileList) => void
  /** FR-377：资源条目拖放到当前目录（非系统文件）。 */
  onDropEntries?: (dt: DataTransfer) => void
  onRename: (name: string) => void
  onDelete: (name: string) => void
  onDownload: (file: FileInfo) => void
  onCut: () => void
  onCopy: () => void
  onOpenArchive: (file: FileInfo) => void
  onDecompile: (file: FileInfo) => void
  /** FR-375 排序状态；省略则仅按名称目录优先展示。 */
  sort?: FileSortState
  onSortChange?: (sort: FileSortState) => void
  /** FR-375 视图模式。 */
  viewMode?: FileViewMode
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

function formatTime(unix: number): string {
  if (!unix) return ''
  try {
    return new Date(unix * 1000).toLocaleString()
  } catch {
    return ''
  }
}

function typeOf(f: FileInfo): string {
  if (f.isDir) return ''
  const i = f.name.lastIndexOf('.')
  return i >= 0 ? f.name.slice(i + 1).toLowerCase() : ''
}

/** 资源管理器右侧目录内容列表（FR-070 + FR-375 排序/权限列/视图）。 */
export default function FileList({
  files,
  loading,
  error,
  selection,
  onRowClick,
  onOpen,
  onDragStartItem,
  onDropUpload,
  onDropEntries,
  onRename,
  onDelete,
  onDownload,
  onCut,
  onCopy,
  onOpenArchive,
  onDecompile,
  sort = { key: 'name', asc: true },
  onSortChange,
  viewMode = 'details',
}: FileListProps) {
  const { t } = useTranslation()
  const [dragOverZone, setDragOverZone] = useState(false)

  const pendingClick = useRef<ReturnType<typeof setTimeout> | null>(null)
  const cancelPendingClick = () => {
    if (pendingClick.current !== null) {
      clearTimeout(pendingClick.current)
      pendingClick.current = null
    }
  }
  useEffect(() => cancelPendingClick, [])

  const sorted = useMemo(() => sortFiles(files, sort), [files, sort])

  const handleRowClick = (name: string, e: React.MouseEvent) => {
    const mods = { shift: e.shiftKey, ctrlOrMeta: e.ctrlKey || e.metaKey }
    if (mods.shift || mods.ctrlOrMeta) {
      onRowClick(name, mods)
      return
    }
    cancelPendingClick()
    pendingClick.current = setTimeout(() => {
      pendingClick.current = null
      onRowClick(name, mods)
    }, 200)
  }

  const headerBtn = (key: FileSortKey, label: string, className?: string) => (
    <button
      type="button"
      className={cn(
        'inline-flex items-center gap-0.5 text-left text-[11px] font-medium text-muted-foreground hover:text-foreground',
        className,
      )}
      onClick={() => onSortChange?.(toggleSort(sort, key))}
    >
      {label}
      {sort.key === key && <ArrowUpDown className="size-3 opacity-70" />}
    </button>
  )

  return (
    <div
      className={cn(
        'min-h-0 flex-1 overflow-auto overscroll-contain',
        dragOverZone && 'bg-primary/5 ring-1 ring-inset ring-primary/40',
      )}
      onDragOver={(e) => {
        const types = [...e.dataTransfer.types]
        if (types.includes('Files') || types.includes('application/x-jm-explorer-entries')) {
          e.preventDefault()
          setDragOverZone(true)
        }
      }}
      onDragLeave={(e) => {
        if (e.currentTarget === e.target) setDragOverZone(false)
      }}
      onDrop={(e) => {
        setDragOverZone(false)
        if (e.dataTransfer.files && e.dataTransfer.files.length > 0) {
          e.preventDefault()
          onDropUpload(e.dataTransfer.files)
          return
        }
        // FR-377：资源条目放入当前列表目录
        if (onDropEntries) {
          e.preventDefault()
          onDropEntries(e.dataTransfer)
        }
      }}
    >
      {loading ? (
        <p className="p-3 text-sm text-muted-foreground">{t('files.loading')}</p>
      ) : error ? (
        <p className="p-3 text-sm text-destructive">{error}</p>
      ) : files.length === 0 ? (
        <p className="p-3 text-sm text-muted-foreground">{t('files.dropToUpload')}</p>
      ) : viewMode === 'icons' ? (
        <ul className="grid grid-cols-[repeat(auto-fill,minmax(88px,1fr))] gap-2 p-2">
          {sorted.map((f) => {
            const checked = isSelected(selection, f.name)
            return (
              <li
                key={f.name}
                draggable
                onDragStart={(e) => onDragStartItem(f.name, e.dataTransfer)}
                className={cn(
                  'flex cursor-pointer flex-col items-center gap-1 rounded-md p-2 text-center text-xs hover:bg-accent/40',
                  checked && 'bg-accent/60',
                )}
                onClick={(e) => handleRowClick(f.name, e)}
                onDoubleClick={(e) => {
                  cancelPendingClick()
                  e.preventDefault()
                  onOpen(f)
                }}
              >
                {f.isDir ? (
                  <Folder className="size-8 text-amber-500" />
                ) : (
                  <FileText className="size-8 text-muted-foreground" />
                )}
                <span className="line-clamp-2 w-full break-all">{f.name}</span>
                {f.writable === false && <Lock className="size-3 text-muted-foreground" />}
              </li>
            )
          })}
        </ul>
      ) : (
        <div>
          {/* FR-375 列头（详情视图） */}
          {viewMode === 'details' && onSortChange && (
            <div className="sticky top-0 z-10 flex items-center gap-2 border-b bg-muted/40 px-3 py-1">
              <span className="w-4 shrink-0" />
              <span className="w-4 shrink-0" />
              <span className="min-w-0 flex-1">{headerBtn('name', t('files.colName'))}</span>
              <span className="w-16 shrink-0 hidden sm:block">{headerBtn('type', t('files.colType'))}</span>
              <span className="w-16 shrink-0 text-right">{headerBtn('size', t('files.colSize'), 'justify-end w-full')}</span>
              <span className="w-36 shrink-0 hidden md:block">{headerBtn('modTime', t('files.colModTime'))}</span>
              <span className="w-20 shrink-0 hidden lg:block">{headerBtn('perm', t('files.colPerm'))}</span>
            </div>
          )}
          <ul>
            {sorted.map((f) => {
              const checked = isSelected(selection, f.name)
              const archive = !f.isDir && isArchiveName(f.name)
              const klass = !f.isDir && isClassName(f.name)
              const handleDouble = () => {
                if (archive) onOpenArchive(f)
                else if (klass) onDecompile(f)
                else onOpen(f)
              }
              return (
                <ContextMenu key={f.name}>
                  <ContextMenuTrigger asChild>
                    <li
                      draggable
                      onDragStart={(e) => onDragStartItem(f.name, e.dataTransfer)}
                      className={cn(
                        'group flex items-center gap-2 border-b border-border/40 px-3 py-1.5 text-sm cursor-pointer hover:bg-accent/40',
                        checked && 'bg-accent/60',
                      )}
                      onClick={(e) => handleRowClick(f.name, e)}
                      onDoubleClick={(e) => {
                        cancelPendingClick()
                        e.preventDefault()
                        handleDouble()
                      }}
                    >
                      <span onClick={(e) => e.stopPropagation()}>
                        <Checkbox
                          checked={checked}
                          onCheckedChange={() => onRowClick(f.name, { ctrlOrMeta: true })}
                          aria-label={f.name}
                        />
                      </span>
                      {f.isDir ? (
                        <Folder className="size-4 shrink-0 text-amber-500" />
                      ) : archive ? (
                        <FileArchive className="size-4 shrink-0 text-sky-500" />
                      ) : klass ? (
                        <FileCode2 className="size-4 shrink-0 text-violet-500" />
                      ) : (
                        <FileText className="size-4 shrink-0 text-muted-foreground" />
                      )}
                      <span className="flex min-w-0 flex-1 items-center gap-1 truncate">
                        <span className="truncate">{f.name}</span>
                        {f.writable === false && (
                          <Lock className="size-3 shrink-0 text-muted-foreground" title={t('files.notWritable')} />
                        )}
                      </span>
                      {viewMode === 'details' && (
                        <>
                          <span className="hidden w-16 shrink-0 truncate text-xs text-muted-foreground sm:block">
                            {f.isDir ? t('files.folderType') : typeOf(f)}
                          </span>
                          <span className="w-16 shrink-0 text-right text-xs text-muted-foreground">
                            {f.isDir ? '' : formatSize(f.size)}
                          </span>
                          <span className="hidden w-36 shrink-0 truncate text-xs text-muted-foreground md:block">
                            {formatTime(f.modTime)}
                          </span>
                          <span className="hidden w-20 shrink-0 truncate font-mono text-[11px] text-muted-foreground lg:block">
                            {f.modeString || (f.writable === false ? t('files.readOnly') : '')}
                          </span>
                        </>
                      )}
                      {viewMode === 'list' && (
                        <span className="ml-2 shrink-0 text-xs text-muted-foreground">
                          {f.isDir ? '' : formatSize(f.size)}
                        </span>
                      )}
                    </li>
                  </ContextMenuTrigger>
                  <ContextMenuContent>
                    {archive && (
                      <ContextMenuItem onSelect={() => onOpenArchive(f)}>
                        <FileArchive className="size-4" /> {t('archive.open')}
                      </ContextMenuItem>
                    )}
                    {(klass || archive) && (
                      <ContextMenuItem onSelect={() => onDecompile(f)}>
                        <FileCode2 className="size-4" /> {t('archive.decompile')}
                      </ContextMenuItem>
                    )}
                    {!f.isDir && !archive && !klass && (
                      <ContextMenuItem onSelect={() => onOpen(f)}>
                        <Pencil className="size-4" /> {t('files.edit')}
                      </ContextMenuItem>
                    )}
                    <ContextMenuItem onSelect={() => onDownload(f)}>
                      <Download className="size-4" /> {t('files.download')}
                    </ContextMenuItem>
                    <ContextMenuItem onSelect={() => onRename(f.name)}>
                      <Pencil className="size-4" /> {t('files.rename')}
                    </ContextMenuItem>
                    <ContextMenuSeparator />
                    <ContextMenuItem onSelect={onCut}>
                      <Scissors className="size-4" /> {t('files.cut')}
                    </ContextMenuItem>
                    <ContextMenuItem onSelect={onCopy}>
                      <Copy className="size-4" /> {t('files.copy')}
                    </ContextMenuItem>
                    <ContextMenuSeparator />
                    <ContextMenuItem variant="destructive" onSelect={() => onDelete(f.name)}>
                      <Trash2 className="size-4" /> {t('files.delete')}
                    </ContextMenuItem>
                  </ContextMenuContent>
                </ContextMenu>
              )
            })}
          </ul>
        </div>
      )}
    </div>
  )
}
