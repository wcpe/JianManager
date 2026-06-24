import { useEffect, useRef, useState } from 'react'
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
} from 'lucide-react'
import { Checkbox } from '@/components/ui/checkbox'
import {
  ContextMenu,
  ContextMenuTrigger,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
} from '@/components/ui/context-menu'
import type { FileInfo } from '@/api/files'
import { isArchiveName, isClassName } from '@/api/archive'
import type { SelectionState, ClickModifiers } from './selection'
import { isSelected } from './selection'
import { cn } from '@/lib/utils'

interface FileListProps {
  files: FileInfo[]
  loading: boolean
  error: string
  selection: SelectionState
  /** 行点击（带修饰键）→ 选择。 */
  onRowClick: (name: string, mods: ClickModifiers) => void
  /** 双击：目录进入、文件打开编辑。 */
  onOpen: (file: FileInfo) => void
  /** 拖拽某文件名开始（树内移动源）。 */
  onDragStartItem: (name: string) => void
  /** 系统文件拖入（上传）。 */
  onDropUpload: (files: FileList) => void
  /** 单项操作。 */
  onRename: (name: string) => void
  onDelete: (name: string) => void
  onDownload: (file: FileInfo) => void
  onCut: () => void
  onCopy: () => void
  /** 打开归档浏览（jar/zip，FR-075）。 */
  onOpenArchive: (file: FileInfo) => void
  /** 反编译（.class/.jar，FR-075）。 */
  onDecompile: (file: FileInfo) => void
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

/** 资源管理器右侧目录内容列表（FR-070）：多选行 + 右键菜单 + 拖拽源/上传放置区。 */
export default function FileList({
  files,
  loading,
  error,
  selection,
  onRowClick,
  onOpen,
  onDragStartItem,
  onDropUpload,
  onRename,
  onDelete,
  onDownload,
  onCut,
  onCopy,
  onOpenArchive,
  onDecompile,
}: FileListProps) {
  const { t } = useTranslation()
  const [dragOverZone, setDragOverZone] = useState(false)

  // 单击选择延后一个 tick，使紧随的双击（打开）能撤销它——避免双击既打开又选中行（BUG-010）。
  // 带修饰键的点击（shift/ctrl/meta）属多选语义、绝不是打开手势，故立即生效不延后。
  const pendingClick = useRef<ReturnType<typeof setTimeout> | null>(null)
  const cancelPendingClick = () => {
    if (pendingClick.current !== null) {
      clearTimeout(pendingClick.current)
      pendingClick.current = null
    }
  }
  useEffect(() => cancelPendingClick, [])

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

  return (
    <div
      className={cn('flex-1 overflow-auto', dragOverZone && 'bg-primary/5 ring-1 ring-inset ring-primary/40')}
      onDragOver={(e) => {
        // 仅系统文件拖入才提示并接收（含 Files 类型）。
        if (e.dataTransfer.types.includes('Files')) {
          e.preventDefault()
          setDragOverZone(true)
        }
      }}
      onDragLeave={(e) => {
        // 离开整个区域时才收起（忽略子元素间冒泡）。
        if (e.currentTarget === e.target) setDragOverZone(false)
      }}
      onDrop={(e) => {
        setDragOverZone(false)
        if (e.dataTransfer.files && e.dataTransfer.files.length > 0) {
          e.preventDefault()
          onDropUpload(e.dataTransfer.files)
        }
      }}
    >
      {loading ? (
        <p className="p-3 text-sm text-muted-foreground">{t('files.loading')}</p>
      ) : error ? (
        <p className="p-3 text-sm text-destructive">{error}</p>
      ) : files.length === 0 ? (
        <p className="p-3 text-sm text-muted-foreground">{t('files.dropToUpload')}</p>
      ) : (
        <ul>
          {files.map((f) => {
            const checked = isSelected(selection, f.name)
            const archive = !f.isDir && isArchiveName(f.name)
            const klass = !f.isDir && isClassName(f.name)
            // 双击：目录进入由 onOpen 处理；归档打开归档浏览；class 反编译；其余打开文本编辑器。
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
                    onDragStart={() => onDragStartItem(f.name)}
                    className={cn(
                      'group flex items-center gap-2 px-3 py-1.5 text-sm cursor-pointer hover:bg-accent/40 border-b border-border/40',
                      checked && 'bg-accent/60',
                    )}
                    onClick={(e) => handleRowClick(f.name, e)}
                    onDoubleClick={(e) => {
                      // 双击属"打开"语义：撤销可能挂起的单击选择，仅打开不勾选该行
                      // （否则双击 jar 会既打开归档又选中行，BUG-010）。
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
                    <span className="truncate flex-1">{f.name}</span>
                    <span className="ml-2 shrink-0 text-xs text-muted-foreground">
                      {f.isDir ? '' : formatSize(f.size)}
                    </span>
                  </li>
                </ContextMenuTrigger>
                <ContextMenuContent>
                  {/* 归档：打开归档浏览（FR-075）。 */}
                  {archive && (
                    <ContextMenuItem onSelect={() => onOpenArchive(f)}>
                      <FileArchive className="size-4" /> {t('archive.open')}
                    </ContextMenuItem>
                  )}
                  {/* class/jar：反编译（FR-075）。 */}
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
      )}
    </div>
  )
}
