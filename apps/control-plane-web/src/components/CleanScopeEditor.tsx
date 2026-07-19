import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type KeyboardEvent,
} from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { ChevronRight, Eraser, Ban, ShieldCheck, Folder, FolderOpen, X } from 'lucide-react'
import {
  buildFileTreeWithDirs,
  collectAllDirPaths,
  buildCleanMap,
  computeDirVisualState,
  exportMarkings,
  getDescendantDirPaths,
  type CleanMark,
  type DirVisualState,
  type ManifestFileLike,
  type TreeDir,
} from '@/lib/client-publish-wizard'
import { ContextMenuSurface, cn } from '@jianmanager/ui'

// ── 颜色映射 ──────────────────────────────────────────────────────────

/** 视觉状态 → 行背景色 class。 */
const STATE_BG: Record<DirVisualState, string> = {
  clean: 'bg-red-500/10',
  exclude: 'bg-green-500/10',
  mixed: 'bg-orange-500/10',
  none: '',
}

/** 视觉状态 → 左侧色条 class。 */
const STATE_BAR: Record<DirVisualState, string> = {
  clean: 'bg-red-500',
  exclude: 'bg-green-500',
  mixed: 'bg-orange-500',
  none: 'bg-transparent',
}

// ── Context ───────────────────────────────────────────────────────────

/** CleanScopeEditor 内部共享状态（避免逐层 prop 传递）。 */
interface ScopeContextValue {
  t: TFunction
  cleanAll: boolean
  collapsedPaths: Set<string>
  toggleDir: (path: string) => void
  selectedPaths: Set<string>
  onDirClick: (path: string, e: ReactMouseEvent) => void
  onDirContextMenu: (path: string, e: ReactMouseEvent) => void
  visualStateOf: (path: string) => DirVisualState
}

const ScopeContext = createContext<ScopeContextValue | null>(null)

// ── 主组件 ────────────────────────────────────────────────────────────

/**
 * 清理目录树形右键菜单可视化编辑器（FR-262，替换 ManagedDirsEditor）。
 *
 * 复用 FileExplorer 同款文件树（从草稿 files 派生目录树），改为右键菜单 + 多选 + 颜色标记
 * 的交互模式。目录节点左侧色条标记四态：红=清理、绿=排除、橙=混合（子目录有不同标记）、
 * 无色=不管理。Ctrl+点击追加选、Shift+点击连选、右键选中集批量标记。父子联动：父标记→子
 * 继承、子单独改→父混合色。产出 managedDirs + cleanExclude。
 *
 * `cleanAll` 开启后全目录视觉标记为清理红色且交互禁用（产出由 ClientPublishPage 接管为
 * `["*"]` 哨兵）。
 */
export interface CleanScopeEditorProps {
  /** 构树所需的文件列表（与 ManifestFile 兼容的最小形态）。 */
  files: ManifestFileLike[]
  /** 当前标记为清理的目录路径集合。 */
  managedDirs: string[]
  /** 当前标记为排除的目录路径集合。 */
  cleanExclude: string[]
  /** 标记变化回调（产出新的 managedDirs + cleanExclude）。 */
  onChange: (managedDirs: string[], cleanExclude: string[]) => void
  /** clean-all 开关：开启后全目录标记为清理红色、交互禁用。 */
  cleanAll?: boolean
  /** 草稿外自定义目录（如 mods、custom-mods），显示在树中可标记。 */
  extraDirs?: string[]
  /** 自定义目录变化回调（添加/删除草稿外目录时触发）。 */
  onExtraDirsChange?: (dirs: string[]) => void
}

export default function CleanScopeEditor({
  files,
  managedDirs,
  cleanExclude,
  onChange,
  cleanAll = false,
  extraDirs = [],
  onExtraDirsChange,
}: CleanScopeEditorProps) {
  const { t } = useTranslation()
  const tree = useMemo(() => buildFileTreeWithDirs(files, extraDirs), [files, extraDirs])
  const [customDirInput, setCustomDirInput] = useState('')
  const allDirPaths = useMemo(() => {
    const fromFiles = collectAllDirPaths(files)
    // 合并草稿外自定义目录及其祖先路径
    const all = new Set(fromFiles)
    for (const d of extraDirs) {
      all.add(d)
      const segs = d.split('/').filter(Boolean)
      let acc = ''
      for (let i = 0; i < segs.length; i++) {
        acc = acc === '' ? segs[i] : `${acc}/${segs[i]}`
        all.add(acc)
      }
    }
    return Array.from(all)
  }, [files, extraDirs])
  const cleanMap = useMemo(
    () => buildCleanMap(managedDirs, cleanExclude),
    [managedDirs, cleanExclude],
  )

  // 多选目录路径集合。
  const [selectedPaths, setSelectedPaths] = useState<Set<string>>(() => new Set())
  // Shift 连选锚点。
  const anchorRef = useRef<string | null>(null)
  // 折叠的目录路径集合（默认空=全展开）。
  const [collapsedPaths, setCollapsedPaths] = useState<Set<string>>(() => new Set())
  // 右键菜单位置 + 目标路径集。
  const [contextMenu, setContextMenu] = useState<{ x: number; y: number; paths: string[] } | null>(null)

  // 可见目录列表（按视觉顺序，供 Shift 连选）。
  const visibleDirs = useMemo(() => collectVisibleDirs(tree, collapsedPaths), [tree, collapsedPaths])

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

  // ── 视觉状态 ──────────────────────────────────────────────────────

  const visualStateOf = useCallback(
    (path: string): DirVisualState => {
      if (cleanAll) return 'clean'
      return computeDirVisualState(path, cleanMap, allDirPaths)
    },
    [cleanAll, cleanMap, allDirPaths],
  )

  // ── 选中 ──────────────────────────────────────────────────────────

  const handleDirClick = useCallback(
    (path: string, e: ReactMouseEvent) => {
      if (cleanAll) return
      if (e.ctrlKey || e.metaKey) {
        setSelectedPaths((prev) => {
          const next = new Set(prev)
          if (next.has(path)) next.delete(path)
          else next.add(path)
          return next
        })
        anchorRef.current = path
      } else if (e.shiftKey && anchorRef.current !== null) {
        const ai = visibleDirs.indexOf(anchorRef.current)
        const ci = visibleDirs.indexOf(path)
        if (ai >= 0 && ci >= 0) {
          const [lo, hi] = ai < ci ? [ai, ci] : [ci, ai]
          setSelectedPaths(new Set(visibleDirs.slice(lo, hi + 1)))
        } else {
          setSelectedPaths(new Set([path]))
        }
      } else {
        setSelectedPaths(new Set([path]))
        anchorRef.current = path
      }
    },
    [cleanAll, visibleDirs],
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

  // ── 右键菜单 ──────────────────────────────────────────────────────

  const handleDirContextMenu = useCallback(
    (path: string, e: ReactMouseEvent) => {
      if (cleanAll) return
      e.preventDefault()
      e.stopPropagation()
      // 右键目录不在选中集 → 单选该目录；在选中集 → 对整个选中集操作。
      let paths: string[]
      if (selectedPaths.has(path) && selectedPaths.size > 1) {
        paths = Array.from(selectedPaths)
      } else {
        paths = [path]
        setSelectedPaths(new Set([path]))
        anchorRef.current = path
      }
      setContextMenu({ x: e.clientX, y: e.clientY, paths })
    },
    [cleanAll, selectedPaths],
  )

  // ── 标记操作 ──────────────────────────────────────────────────────

  const handleMark = useCallback(
    (mark: CleanMark | 'none') => {
      const paths = contextMenu?.paths ?? []
      setContextMenu(null)
      if (paths.length === 0 || cleanAll) return
      const next = new Map(cleanMap)
      for (const p of paths) {
        // 联动：标记/取消时连同所有子目录一起处理。
        const subtree = [p, ...getDescendantDirPaths(p, allDirPaths)]
        for (const sp of subtree) {
          if (mark === 'none') next.delete(sp)
          else next.set(sp, mark)
        }
      }
      const { managedDirs: md, cleanExclude: ce } = exportMarkings(next)
      onChange(md, ce)
    },
    [contextMenu, cleanAll, cleanMap, allDirPaths, onChange],
  )

  // ── Context 值 ────────────────────────────────────────────────────

  const ctx: ScopeContextValue = {
    t,
    cleanAll,
    collapsedPaths,
    toggleDir,
    selectedPaths,
    onDirClick: handleDirClick,
    onDirContextMenu: handleDirContextMenu,
    visualStateOf,
  }

  if (allDirPaths.length === 0) {
    return (
      <p
        className="rounded-md border border-dashed p-3 text-xs text-muted-foreground"
        data-testid="clean-scope-empty"
      >
        {t('clientVersions.managedDirsTreeEmpty', '暂无目录——添加文件后，此处会列出草稿文件派生的目录供标记。')}
      </p>
    )
  }

  return (
    <ScopeContext.Provider value={ctx}>
      <div
        className={cn('rounded-lg border bg-card/30 p-1 text-sm', cleanAll && 'opacity-60')}
        data-testid="clean-scope-tree"
        data-clean-all={cleanAll || undefined}
      >
        <ul className="space-y-0.5">
          {tree.dirs.map((d) => (
            <li key={d.path}>
              <DirScopeRow dir={d} depth={0} />
            </li>
          ))}
        </ul>
      </div>

      {/* 颜色图例 */}
      <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
        <LegendDot color="bg-red-500" label={t('clientVersions.cleanScopeLegendClean', '清理')} />
        <LegendDot color="bg-green-500" label={t('clientVersions.cleanScopeLegendExclude', '排除')} />
        <LegendDot color="bg-orange-500" label={t('clientVersions.cleanScopeLegendMixed', '混合')} />
        <LegendDot color="bg-muted-foreground/30" label={t('clientVersions.cleanScopeLegendNone', '不管理')} />
        {selectedPaths.size > 0 && (
          <span className="ml-auto">
            {t('clientVersions.cleanScopeSelectionCount', '已选 {{n}} 个目录', { n: selectedPaths.size })}
          </span>
        )}
      </div>

      {/* 添加自定义目录（草稿外目录，如 mods、custom-mods） */}
      {!cleanAll && onExtraDirsChange && (
        <div className="mt-2 space-y-1.5" data-testid="custom-dirs-section">
          <div className="flex items-center gap-1.5">
            <input
              className="flex-1 rounded border bg-background px-2 py-1 text-xs font-mono"
              value={customDirInput}
              onChange={(e) => setCustomDirInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key !== 'Enter') return
                e.preventDefault()
                const v = customDirInput.trim().replace(/\\/g, '/').replace(/^\/+|\/+$/g, '')
                if (!v || extraDirs.includes(v)) { setCustomDirInput(''); return }
                onExtraDirsChange([...extraDirs, v])
                setCustomDirInput('')
              }}
              placeholder={t('clientVersions.customDirPlaceholder', '输入目录路径如 mods 或 config/foo，回车添加')}
              data-testid="custom-dir-input"
            />
            <button
              type="button"
              className="shrink-0 rounded border px-2 py-1 text-xs hover:bg-accent"
              onClick={() => {
                const v = customDirInput.trim().replace(/\\/g, '/').replace(/^\/+|\/+$/g, '')
                if (!v || extraDirs.includes(v)) { setCustomDirInput(''); return }
                onExtraDirsChange([...extraDirs, v])
                setCustomDirInput('')
              }}
              data-testid="custom-dir-add-btn"
            >
              {t('clientVersions.customDirAdd', '添加')}
            </button>
          </div>
          {extraDirs.length > 0 && (
            <div className="flex flex-wrap gap-1">
              {extraDirs.map((d) => (
                <span
                  key={d}
                  className="inline-flex items-center gap-1 rounded bg-muted px-1.5 py-0.5 text-xs font-mono"
                >
                  {d}
                  <button
                    type="button"
                    className="text-muted-foreground hover:text-destructive"
                    onClick={() => onExtraDirsChange(extraDirs.filter((x) => x !== d))}
                    data-testid="custom-dir-remove"
                    data-dir={d}
                  >
                    <X className="size-3" />
                  </button>
                </span>
              ))}
            </div>
          )}
        </div>
      )}

      {/* 右键菜单 */}
      {contextMenu && (
        <ScopeContextMenu x={contextMenu.x} y={contextMenu.y} onMark={handleMark} t={t} />
      )}
    </ScopeContext.Provider>
  )
}

// ── 颜色图例点 ────────────────────────────────────────────────────────

function LegendDot({ color, label }: { color: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1">
      <span className={cn('inline-block size-2.5 rounded-sm', color)} />
      {label}
    </span>
  )
}

// ── 右键菜单 ──────────────────────────────────────────────────────────

function ScopeContextMenu({
  x,
  y,
  onMark,
  t,
}: {
  x: number
  y: number
  onMark: (mark: CleanMark | 'none') => void
  t: TFunction
}) {
  return (
    <ContextMenuSurface
      x={x}
      y={y}
      className="fixed z-50 min-w-[140px] rounded-md border bg-popover p-1 shadow-md"
      data-testid="clean-scope-context-menu"
      onClick={(e) => e.stopPropagation()}
    >
      <button
        type="button"
        className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-accent"
        onClick={() => onMark('clean')}
        data-testid="clean-scope-mark-clean"
      >
        <Eraser className="size-4 text-red-500" />
        {t('clientVersions.cleanScopeMarkClean', '标记为清理')}
      </button>
      <button
        type="button"
        className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-accent"
        onClick={() => onMark('exclude')}
        data-testid="clean-scope-mark-exclude"
      >
        <ShieldCheck className="size-4 text-green-500" />
        {t('clientVersions.cleanScopeMarkExclude', '标记为排除')}
      </button>
      <button
        type="button"
        className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm text-muted-foreground hover:bg-accent"
        onClick={() => onMark('none')}
        data-testid="clean-scope-unmark"
      >
        <Ban className="size-4" />
        {t('clientVersions.cleanScopeUnmark', '取消标记')}
      </button>
    </ContextMenuSurface>
  )
}

// ── 树渲染 ────────────────────────────────────────────────────────────

/** 渲染一个目录层级（子目录列表，不渲染文件——清理只标记目录）。 */
function TreeLevel({ dir, depth }: { dir: TreeDir; depth: number }) {
  return (
    <ul className="space-y-0.5">
      {dir.dirs.map((d) => (
        <li key={d.path}>
          <DirScopeRow dir={d} depth={depth} />
        </li>
      ))}
    </ul>
  )
}

/** 可折叠目录行：左侧色条 + 折叠箭头 + 文件夹图标 + 目录名 + 规模。 */
function DirScopeRow({ dir, depth }: { dir: TreeDir; depth: number }) {
  const ctx = useContext(ScopeContext)!
  const { t, cleanAll, collapsedPaths, toggleDir, selectedPaths, onDirClick, onDirContextMenu, visualStateOf } = ctx
  const collapsed = collapsedPaths.has(dir.path)
  const isSelected = selectedPaths.has(dir.path)
  const state = visualStateOf(dir.path)

  return (
    <>
      <div
        className={cn(
          'relative flex items-center gap-1.5 rounded-md px-2 py-1.5 transition-colors',
          !cleanAll && 'hover:bg-accent/50',
          STATE_BG[state],
          isSelected && 'ring-1 ring-primary/40',
        )}
        style={{ paddingLeft: `${depth * 1.25 + 0.5}rem` }}
        onClick={(e) => onDirClick(dir.path, e)}
        onContextMenu={(e) => onDirContextMenu(dir.path, e)}
        data-testid="clean-scope-dir-row"
        data-dir-path={dir.path}
        data-mark={state}
        data-selected={isSelected}
      >
        {/* 左侧色条 */}
        <span className={cn('absolute left-0 top-0 h-full w-1 rounded-l', STATE_BAR[state])} />
        {/* 折叠箭头 */}
        <button
          type="button"
          className="shrink-0"
          onClick={(e) => {
            e.stopPropagation()
            toggleDir(dir.path)
          }}
          aria-label={collapsed ? t('clientVersions.cleanScopeExpand', '展开') : t('clientVersions.cleanScopeCollapse', '折叠')}
          data-testid="clean-scope-toggle"
          data-dir-path={dir.path}
        >
          <ChevronRight className={cn('size-3.5 shrink-0 text-muted-foreground transition-transform', !collapsed && 'rotate-90')} />
        </button>
        {collapsed ? (
          <Folder className="size-4 shrink-0 text-amber-500" />
        ) : (
          <FolderOpen className="size-4 shrink-0 text-amber-500" />
        )}
        <span className="font-medium">{dir.name}</span>
        <span className="ml-auto shrink-0 text-xs text-muted-foreground">
          {t('clientVersions.treeDirSummary', '{{n}} 个文件 · {{size}}', {
            n: dir.fileCount,
            size: formatBytes(dir.totalSize),
          })}
        </span>
      </div>
      {!collapsed && dir.dirs.length > 0 && <TreeLevel dir={dir} depth={depth + 1} />}
    </>
  )
}

// ── 辅助函数 ──────────────────────────────────────────────────────────

/** 递归收集可见目录路径（按视觉顺序，供 Shift 连选）。 */
function collectVisibleDirs(dir: TreeDir, collapsedPaths: Set<string>): string[] {
  const out: string[] = []
  for (const d of dir.dirs) {
    out.push(d.path)
    if (!collapsedPaths.has(d.path)) {
      out.push(...collectVisibleDirs(d, collapsedPaths))
    }
  }
  return out
}

/** 字节数转人类可读（与 ClientFileTree 同口径，保持一致展示）。 */
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

// ── 自定义排除标签输入（FR-255，从 ManagedDirsEditor 迁移） ──────────────

/**
 * 自定义追加排除标签输入（FR-255）。
 *
 * 运营可填「额外永不清理」的目录/路径（如玩家自装 mod 目录），产出 `cleanExclude: string[]`。
 * 回车添加，每个标签可删除。输入合法性：非空、相对路径不逃逸、去重。
 */
export interface CleanExcludeInputProps {
  /** 当前已添加的排除项。 */
  value: string[]
  /** 变更回调。 */
  onChange: (value: string[]) => void
  /** 占位文案键。 */
  placeholder?: string
  /** 禁用。 */
  disabled?: boolean
}

export function CleanExcludeInput({ value, onChange, placeholder, disabled }: CleanExcludeInputProps) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState('')

  const add = () => {
    const v = draft.trim().replace(/\/+$/, '')
    if (v === '' || v.includes('..') || v === '*') {
      setDraft('')
      return
    }
    if (!value.includes(v)) {
      onChange([...value, v])
    }
    setDraft('')
  }

  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      add()
    } else if (e.key === 'Backspace' && draft === '' && value.length > 0) {
      onChange(value.slice(0, -1))
    }
  }

  return (
    <div className="space-y-2" data-testid="clean-exclude-input">
      <div className="flex flex-wrap gap-1.5">
        {value.map((tag) => (
          <span
            key={tag}
            className="inline-flex items-center gap-1 rounded-md border bg-muted/40 px-2 py-0.5 text-xs font-mono"
          >
            {tag}
            {!disabled && (
              <button
                type="button"
                className="text-muted-foreground hover:text-destructive"
                onClick={() => onChange(value.filter((x) => x !== tag))}
                aria-label={t('clientVersions.cleanExcludeTagRemove', '移除')}
                data-testid="clean-exclude-tag-remove"
                data-tag={tag}
              >
                <X className="size-3" />
              </button>
            )}
          </span>
        ))}
      </div>
      <input
        className="p-2 border rounded bg-background font-mono text-xs disabled:opacity-50"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={onKey}
        placeholder={placeholder ?? t('clientVersions.cleanExcludePlaceholder', '如 玩家mod、custom-mods')}
        disabled={disabled}
        data-testid="clean-exclude-field"
      />
    </div>
  )
}
