import { useRef, useState, type FormEvent, type ReactNode } from 'react'
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
  MoreHorizontal,
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
  /**
   * FR-422：横栏左端插槽。宿主（如资源卡的「管理/文件/浏览」分段）把自己的切换控件塞进本行，
   * 而不是在工具栏之上再叠一条只放几个 pill 的横栏。
   */
  leading?: ReactNode
  /**
   * FR-422：当前目录条目数。`undefined` = 未知（加载中/出错），此时不渲染汇总——
   * 汇总是给人做判断的事实，宁缺勿造。
   */
  itemCount?: number
  /** FR-422：当前目录内文件字节总计（不含子目录递归）。0 或 undefined 不渲染。 */
  totalSize?: number
}

/** 目录汇总用的字节格式化。比 FileList 的单文件版多一档 GB——目录总量常达 GB 级。 */
function formatTotalSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`
}

/**
 * 资源管理器工具栏（FR-070 + FR-375 + FR-422）：**单条**横栏。
 *
 * FR-422 之前这里是三条横栏（导航+地址栏+视图 / 面包屑 / 八个平铺按钮，实测 93px），叠上宿主的
 * 分段栏（36px + 21px 间距）共四条 149px。1920×1030 实测：合并后横栏 37px，文件列表可视区
 * 613px→725px，同屏完整可见文件行 17→21。压成一条的三个手段：
 *
 * 1. **面包屑只留一份**。原先地址栏输入框与其下的面包屑是同一信息的两种写法。现在默认渲染面包屑
 *    （可点跳转，且顺带显示目录汇总），点击其右侧空白区（Windows 资源管理器的老习惯）或「编辑路径」
 *    才换成可输入的地址栏——路径直输的能力一个字没少，只是不再常占一条横栏。
 * 2. **按钮按使用频度分层**。新建/上传/搜索是常用动作，留明文；全选/清空选择收进 `⋯`；
 *    下载/删除**有选中时提升为明文**（无选中时它们本就是永久 disabled 的噪声，收进 `⋯` 更干净），
 *    粘贴始终在 `⋯` 内（其可用性由剪贴板决定，与当前选中无关）。收进下拉的项一个没删，
 *    `⋯` 用 Radix DropdownMenu，键盘可开可选。
 * 3. **分段栏并入本行**（`leading` 插槽），见该 prop 注释。
 *
 * `flex-wrap`：窄屏（或分段+全量按钮同时在场）时折成第二行而不是横向溢出裁掉按钮——
 * 宁可多一行也不能有点不到的按钮。
 */
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
  leading,
  itemCount,
  totalSize,
}: ToolbarProps) {
  const { t } = useTranslation()
  const uploadRef = useRef<HTMLInputElement>(null)
  const crumbs = breadcrumbs(currentDir)

  // 地址栏编辑态：默认展示面包屑，进入编辑才渲染输入框（同一横栏内互换，不各占一行）。
  const [editingPath, setEditingPath] = useState(false)
  const [addr, setAddr] = useState(currentDir)

  const startEditPath = () => {
    setAddr(currentDir)
    setEditingPath(true)
  }

  const submitAddr = (e: FormEvent) => {
    e.preventDefault()
    const next = addr.replace(/^\/+|\/+$/g, '').replace(/\\/g, '/')
    setEditingPath(false)
    onNavigate(next)
  }

  const hasSelection = selectedCount > 0
  const summaryParts: string[] = []
  if (itemCount !== undefined && itemCount > 0) summaryParts.push(t('files.dirSummaryItems', { count: itemCount }))
  if (totalSize !== undefined && totalSize > 0) summaryParts.push(formatTotalSize(totalSize))
  const summary = summaryParts.join(' · ')

  return (
    <div
      data-testid="explorer-toolbar"
      className="flex shrink-0 flex-wrap items-center gap-1 border-b bg-muted/30 px-2 py-1"
    >
      {/* 分段等宿主控件（FR-422）。 */}
      {leading}

      {/* 后退 / 前进（FR-375） */}
      <Button
        type="button"
        size="sm"
        variant="ghost"
        className="h-7 w-7 shrink-0 p-0"
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
        className="h-7 w-7 shrink-0 p-0"
        disabled={!canForward}
        title={t('files.navForward')}
        aria-label={t('files.navForward')}
        onClick={onForward}
      >
        <ArrowRight className="size-3.5" />
      </Button>

      {/* 路径：面包屑（含目录汇总）⇄ 地址栏输入，占中间弹性空间 */}
      <div className="flex min-w-[10rem] flex-1 items-center gap-0.5">
        {editingPath ? (
          <form onSubmit={submitAddr} className="min-w-0 flex-1">
            <input
              data-addr-bar="1"
              autoFocus
              value={addr}
              onChange={(e) => setAddr(e.target.value)}
              onBlur={() => setEditingPath(false)}
              onKeyDown={(e) => {
                // Esc 退回面包屑：编辑态是临时的，键盘用户要能不提交就退出。
                if (e.key === 'Escape') setEditingPath(false)
              }}
              className="h-7 w-full rounded-md border bg-background px-2 font-mono text-xs"
              aria-label={t('files.addressBar')}
              placeholder="/"
            />
          </form>
        ) : (
          <>
            <nav
              aria-label={t('files.pathBreadcrumb')}
              className="flex min-w-0 shrink items-center gap-0.5 overflow-x-auto text-xs text-muted-foreground"
            >
              <button
                type="button"
                className="shrink-0 rounded px-1 hover:bg-accent hover:text-foreground"
                onClick={() => onNavigate('')}
              >
                /
              </button>
              {crumbs.map((c) => (
                <span key={c.path} className="flex shrink-0 items-center gap-0.5">
                  <ChevronRight className="size-3" />
                  <button
                    type="button"
                    className="rounded px-1 hover:bg-accent hover:text-foreground"
                    onClick={() => onNavigate(c.path)}
                  >
                    {c.name}
                  </button>
                </span>
              ))}
            </nav>
            {/* 面包屑右侧空白即「切到地址栏」的点击区（Windows 资源管理器同款），
                同时承载目录汇总——既省一条横栏，又让这块空白有信息量。 */}
            <button
              type="button"
              className="min-w-0 flex-1 truncate rounded px-1 py-0.5 text-left text-[11px] text-muted-foreground hover:bg-accent/60"
              aria-label={t('files.editPath')}
              title={t('files.editPath')}
              onClick={startEditPath}
            >
              {summary}
            </button>
          </>
        )}
      </div>

      {/* 高频动作：新建 / 上传 / （有选中时）下载 / 删除 / 搜索 */}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button size="sm" variant="outline" className="h-7 shrink-0 gap-1 px-2 text-xs">
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
        className="h-7 shrink-0 gap-1 px-2 text-xs"
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

      {hasSelection && (
        <>
          <Button
            size="sm"
            variant="outline"
            className="h-7 shrink-0 gap-1 px-2 text-xs"
            onClick={onDownloadSelected}
          >
            <Download className="size-3.5" /> {t('files.download')}
          </Button>
          <Button
            size="sm"
            variant="outline"
            className="h-7 shrink-0 gap-1 px-2 text-xs"
            onClick={onDeleteSelected}
          >
            <Trash2 className="size-3.5" /> {t('files.delete')}
          </Button>
        </>
      )}

      <Button
        size="sm"
        variant={searchActive ? 'secondary' : 'ghost'}
        className="h-7 shrink-0 gap-1 px-2 text-xs"
        onClick={onToggleSearch}
      >
        <Search className="size-3.5" /> {t('search.title')}
      </Button>

      {/* 低频动作收纳。收进来的项**一个都没删**：disabled 语义与原按钮逐一对应。 */}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            size="sm"
            variant="ghost"
            className="h-7 w-7 shrink-0 p-0"
            title={t('files.moreActions')}
            aria-label={t('files.moreActions')}
          >
            <MoreHorizontal className="size-3.5" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {/* 无选中时下载/删除在此（永久 disabled 的按钮不值得占明文位）；有选中时它们已提升为明文。 */}
          {!hasSelection && (
            <>
              <DropdownMenuItem disabled onSelect={onDownloadSelected}>
                <Download className="size-4" /> {t('files.download')}
              </DropdownMenuItem>
              <DropdownMenuItem disabled onSelect={onDeleteSelected}>
                <Trash2 className="size-4" /> {t('files.delete')}
              </DropdownMenuItem>
            </>
          )}
          <DropdownMenuItem disabled={!canPaste} onSelect={onPaste}>
            <ClipboardPaste className="size-4" /> {t('files.paste')}
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={onSelectAll}>
            <CheckSquare className="size-4" /> {t('files.selectAll')}
          </DropdownMenuItem>
          <DropdownMenuItem disabled={!hasSelection} onSelect={onClearSelection}>
            <XSquare className="size-4" /> {t('files.clear')}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      {/* 视图切换留在最右端（FR-375 三视图） */}
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
  )
}
