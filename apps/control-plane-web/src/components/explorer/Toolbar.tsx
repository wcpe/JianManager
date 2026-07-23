import { useEffect, useRef, useState, type FormEvent } from 'react'
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
  Search,
  ArrowLeft,
  ArrowRight,
  LayoutList,
  List,
  LayoutGrid,
} from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@jianmanager/ui/components/dropdown-menu'
import { breadcrumbs } from './paths'
import type { FileViewMode } from './file-sort'
import { cn } from '@jianmanager/ui'

interface ToolbarProps {
  currentDir: string
  selectedCount: number
  canPaste: boolean
  canBack?: boolean
  canForward?: boolean
  onBack?: () => void
  onForward?: () => void
  onNavigate: (dir: string) => void
  onNewFile: () => void
  onNewFolder: () => void
  onUpload: (files: FileList) => void
  onDownloadSelected: () => void
  onDeleteSelected: () => void
  onPaste: () => void
  onSelectAll: () => void
  onClearSelection: () => void
  onToggleSearch: () => void
  searchActive: boolean
  viewMode?: FileViewMode
  onViewModeChange?: (mode: FileViewMode) => void
}

/** 资源管理器工具栏（FR-070 + FR-375）：后退前进 + 地址栏 + 面包屑 + 操作 + 视图。 */
export default function Toolbar({
  currentDir,
  selectedCount,
  canPaste,
  canBack = false,
  canForward = false,
  onBack,
  onForward,
  onNavigate,
  onNewFile,
  onNewFolder,
  onUpload,
  onDownloadSelected,
  onDeleteSelected,
  onPaste,
  onSelectAll,
  onClearSelection,
  onToggleSearch,
  searchActive,
  viewMode = 'details',
  onViewModeChange,
}: ToolbarProps) {
  const { t } = useTranslation()
  const uploadRef = useRef<HTMLInputElement>(null)
  const crumbs = breadcrumbs(currentDir)
  const [addr, setAddr] = useState(currentDir)
  const addrFocused = useRef(false)
  useEffect(() => {
    if (!addrFocused.current) setAddr(currentDir)
  }, [currentDir])

  const submitAddr = (e: FormEvent) => {
    e.preventDefault()
    const next = addr.replace(/^\/+|\/+$/g, '').replace(/\\/g, '/')
    onNavigate(next)
  }

  return (
    <div className="flex shrink-0 flex-col gap-1 border-b bg-muted/30 px-2 py-1.5">
      {/* FR-375：后退 / 前进 / 地址栏 */}
      <div className="flex items-center gap-1">
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="h-7 w-7 p-0"
          disabled={!canBack}
          title={t('files.navBack')}
          aria-label={t('files.navBack')}
          onClick={onBack}
        >
          <ArrowLeft className="size-3.5" />
        </Button>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="h-7 w-7 p-0"
          disabled={!canForward}
          title={t('files.navForward')}
          aria-label={t('files.navForward')}
          onClick={onForward}
        >
          <ArrowRight className="size-3.5" />
        </Button>
        <form onSubmit={submitAddr} className="min-w-0 flex-1">
          <input
            data-addr-bar="1"
            value={addr}
            onChange={(e) => setAddr(e.target.value)}
            onFocus={() => { addrFocused.current = true }}
            onBlur={() => {
              addrFocused.current = false
              setAddr(currentDir)
            }}
            className="h-7 w-full rounded-md border bg-background px-2 font-mono text-xs"
            aria-label={t('files.addressBar')}
            placeholder="/"
          />
        </form>
        {onViewModeChange && (
          <div className="flex shrink-0 items-center rounded-md border p-0.5">
            {(
              [
                { m: 'details' as const, icon: LayoutList, label: t('files.viewDetails') },
                { m: 'list' as const, icon: List, label: t('files.viewList') },
                { m: 'icons' as const, icon: LayoutGrid, label: t('files.viewIcons') },
              ] as const
            ).map(({ m, icon: Icon, label }) => (
              <button
                key={m}
                type="button"
                title={label}
                aria-label={label}
                aria-pressed={viewMode === m}
                className={cn(
                  'rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground',
                  viewMode === m && 'bg-accent text-foreground',
                )}
                onClick={() => onViewModeChange(m)}
              >
                <Icon className="size-3.5" />
              </button>
            ))}
          </div>
        )}
      </div>

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
            if (e.target.files?.length) onUpload(e.target.files)
            e.target.value = ''
          }}
        />

        <Button
          size="sm"
          variant="outline"
          className="h-7 gap-1 px-2 text-xs"
          disabled={selectedCount === 0}
          onClick={onDownloadSelected}
        >
          <Download className="size-3.5" /> {t('files.download')}
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="h-7 gap-1 px-2 text-xs"
          disabled={selectedCount === 0}
          onClick={onDeleteSelected}
        >
          <Trash2 className="size-3.5" /> {t('files.delete')}
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="h-7 gap-1 px-2 text-xs"
          disabled={!canPaste}
          onClick={onPaste}
        >
          <ClipboardPaste className="size-3.5" /> {t('files.paste')}
        </Button>
        <Button size="sm" variant="ghost" className="h-7 gap-1 px-2 text-xs" onClick={onSelectAll}>
          <CheckSquare className="size-3.5" /> {t('files.selectAll')}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          className="h-7 gap-1 px-2 text-xs"
          disabled={selectedCount === 0}
          onClick={onClearSelection}
        >
          <XSquare className="size-3.5" /> {t('files.clear')}
        </Button>
        <Button
          size="sm"
          variant={searchActive ? 'secondary' : 'ghost'}
          className="h-7 gap-1 px-2 text-xs"
          onClick={onToggleSearch}
        >
          <Search className="size-3.5" /> {t('search.title')}
        </Button>
      </div>
    </div>
  )
}
