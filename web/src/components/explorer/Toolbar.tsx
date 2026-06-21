import { useRef } from 'react'
import { useTranslation } from 'react-i18next'
import {
  FilePlus,
  FolderPlus,
  Upload,
  Download,
  Trash2,
  ClipboardPaste,
  CheckSquare,
  XSquare,
  ChevronRight,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu'
import { breadcrumbs } from './paths'

interface ToolbarProps {
  currentDir: string
  selectedCount: number
  canPaste: boolean
  onNavigate: (dir: string) => void
  onNewFile: () => void
  onNewFolder: () => void
  onUpload: (files: FileList) => void
  onDownloadSelected: () => void
  onDeleteSelected: () => void
  onPaste: () => void
  onSelectAll: () => void
  onClearSelection: () => void
}

/** 资源管理器工具栏（FR-070）：面包屑 + 新建/上传/下载/删除/粘贴/全选。 */
export default function Toolbar({
  currentDir,
  selectedCount,
  canPaste,
  onNavigate,
  onNewFile,
  onNewFolder,
  onUpload,
  onDownloadSelected,
  onDeleteSelected,
  onPaste,
  onSelectAll,
  onClearSelection,
}: ToolbarProps) {
  const { t } = useTranslation()
  const uploadRef = useRef<HTMLInputElement>(null)
  const crumbs = breadcrumbs(currentDir)

  return (
    <div className="flex flex-col gap-1 border-b bg-muted/30 px-2 py-1.5">
      {/* 面包屑 */}
      <div className="flex items-center gap-0.5 overflow-x-auto text-xs text-muted-foreground">
        <button className="rounded px-1 hover:bg-accent hover:text-foreground" onClick={() => onNavigate('')}>
          /
        </button>
        {crumbs.map((c) => (
          <span key={c.path} className="flex items-center gap-0.5">
            <ChevronRight className="size-3" />
            <button className="rounded px-1 hover:bg-accent hover:text-foreground" onClick={() => onNavigate(c.path)}>
              {c.name}
            </button>
          </span>
        ))}
      </div>

      {/* 操作按钮 */}
      <div className="flex flex-wrap items-center gap-1">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button size="sm" variant="outline" className="h-7 gap-1 px-2 text-xs">
              <FilePlus className="size-3.5" /> {t('files.new')}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent>
            <DropdownMenuItem onSelect={onNewFile}>
              <FilePlus className="size-4" /> {t('files.newFile')}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={onNewFolder}>
              <FolderPlus className="size-4" /> {t('files.newFolder')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>

        <Button
          size="sm"
          variant="outline"
          className="h-7 gap-1 px-2 text-xs"
          onClick={() => uploadRef.current?.click()}
        >
          <Upload className="size-3.5" /> {t('files.upload')}
        </Button>
        <input
          ref={uploadRef}
          type="file"
          multiple
          className="hidden"
          onChange={(e) => {
            if (e.target.files && e.target.files.length > 0) onUpload(e.target.files)
            if (uploadRef.current) uploadRef.current.value = ''
          }}
        />

        <Button
          size="sm"
          variant="outline"
          className="h-7 gap-1 px-2 text-xs"
          disabled={!canPaste}
          onClick={onPaste}
        >
          <ClipboardPaste className="size-3.5" /> {t('files.paste')}
        </Button>

        <div className="mx-1 h-4 w-px bg-border" />

        <Button
          size="sm"
          variant="outline"
          className="h-7 gap-1 px-2 text-xs"
          disabled={selectedCount === 0}
          onClick={onDownloadSelected}
        >
          <Download className="size-3.5" /> {t('files.downloadZip')}
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="h-7 gap-1 px-2 text-xs text-destructive"
          disabled={selectedCount === 0}
          onClick={onDeleteSelected}
        >
          <Trash2 className="size-3.5" /> {t('files.delete')}
        </Button>

        <div className="mx-1 h-4 w-px bg-border" />

        <Button size="sm" variant="ghost" className="h-7 gap-1 px-2 text-xs" onClick={onSelectAll}>
          <CheckSquare className="size-3.5" /> {t('files.selectAll')}
        </Button>
        {selectedCount > 0 && (
          <Button size="sm" variant="ghost" className="h-7 gap-1 px-2 text-xs" onClick={onClearSelection}>
            <XSquare className="size-3.5" /> {t('files.clear')} ({selectedCount})
          </Button>
        )}
      </div>
    </div>
  )
}
