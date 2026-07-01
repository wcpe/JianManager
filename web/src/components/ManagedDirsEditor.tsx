import { useMemo, useState, type KeyboardEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Folder, FolderOpen, ChevronRight, X } from 'lucide-react'
import {
  buildFileTree,
  collectAllDirPaths,
  type ManifestFileLike,
  type TreeDir,
} from '@/lib/client-publish-wizard'
import { cn } from '@/lib/utils'
import { Checkbox } from '@/components/ui/checkbox'

/**
 * 自动清理目录树勾选编辑器（FR-255）。
 *
 * 由草稿文件 path 派生目录结构（含深层嵌套），渲染为可折叠的目录树 + 复选框，
 * 勾选哪些目录纳入自动清理。产出 `managedDirs: string[]`（可含嵌套路径串，客户端已支持）。
 *
 * `disabled` 时整树禁用（如开启「清空整个 gameDir」后，managedDirs 由哨兵 `"*"` 接管，
 * 目录勾选无意义）。纯展示+勾选，不含拖拽编排（那是 ClientFileTree 的职责）。
 */
export interface ManagedDirsEditorProps {
  /** 构树所需的文件列表（与 ManifestFile 兼容的最小形态）。 */
  files: ManifestFileLike[]
  /** 当前已勾选的目录路径集合。 */
  selected: string[]
  /** 勾选变更回调。 */
  onChange: (selected: string[]) => void
  /** 整树禁用（clean-all 接管时）。 */
  disabled?: boolean
}

export default function ManagedDirsEditor({ files, selected, onChange, disabled }: ManagedDirsEditorProps) {
  const { t } = useTranslation()
  const tree = useMemo(() => buildFileTree(files), [files])
  const selectedSet = useMemo(() => new Set(selected), [selected])

  const toggle = (dirPath: string) => {
    if (disabled) return
    const next = selectedSet.has(dirPath)
      ? selected.filter((d) => d !== dirPath)
      : [...selected, dirPath]
    onChange(next)
  }

  if (collectAllDirPaths(files).length === 0) {
    return (
      <p className="rounded-md border border-dashed p-3 text-xs text-muted-foreground" data-testid="managed-dirs-empty">
        {t('clientVersions.managedDirsTreeEmpty', '暂无目录——添加文件后，此处会列出草稿文件派生的目录供勾选。')}
      </p>
    )
  }

  return (
    <div className={cn('rounded-lg border bg-card/30 p-1 text-sm', disabled && 'opacity-50')} data-testid="managed-dirs-tree">
      <ul className="space-y-0.5">
        {tree.dirs.map((d) => (
          <li key={d.path}>
            <DirCheckRow dir={d} depth={0} selectedSet={selectedSet} onToggle={toggle} disabled={disabled} />
          </li>
        ))}
      </ul>
    </div>
  )
}

/** 可折叠目录勾选行：复选框 + 折叠箭头 + 文件夹图标 + 目录名 + 子树。 */
function DirCheckRow({
  dir,
  depth,
  selectedSet,
  onToggle,
  disabled,
}: {
  dir: TreeDir
  depth: number
  selectedSet: Set<string>
  onToggle: (dirPath: string) => void
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(true)
  const checked = selectedSet.has(dir.path)
  return (
    <>
      <div
        className={cn(
          'flex items-center gap-1.5 rounded-md px-2 py-1.5 transition-colors',
          !disabled && 'hover:bg-accent',
        )}
        style={{ paddingLeft: `${depth * 1.25 + 0.5}rem` }}
      >
        <Checkbox
          checked={checked}
          disabled={disabled}
          onCheckedChange={() => onToggle(dir.path)}
          aria-label={dir.path}
          data-testid="managed-dirs-checkbox"
          data-dir-path={dir.path}
        />
        <button
          type="button"
          className="flex flex-1 items-center gap-1.5 text-left"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          disabled={disabled}
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
      {open && dir.dirs.length > 0 && (
        <ul className="space-y-0.5">
          {dir.dirs.map((d) => (
            <li key={d.path}>
              <DirCheckRow dir={d} depth={depth + 1} selectedSet={selectedSet} onToggle={onToggle} disabled={disabled} />
            </li>
          ))}
        </ul>
      )}
    </>
  )
}

/** 字节数转人类可读（与 ClientFileTree 同口径，保持一致展示）。 */
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

// ── 自定义排除标签输入（FR-255） ──────────────────────────────────────────

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
