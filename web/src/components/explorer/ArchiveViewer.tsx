import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  ChevronDown,
  ChevronRight,
  FileCode2,
  FileText,
  Folder,
  FolderOpen,
  Loader2,
  X,
} from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  listArchiveEntries,
  readArchiveEntry,
  decompile,
  isClassName,
  type ArchiveEntry,
} from '@/api/archive'
import CodeEditor from './editor/CodeEditor'
import { buildEntryTree, type EntryNode } from './archive-tree'
import { cn } from '@/lib/utils'

/**
 * 归档浏览与反编译视图（FR-075，复用 FR-070 只读编辑器）。
 *
 * - 打开 jar/zip：左栏列内部条目树（Worker archive/zip 列举），点文本条目→右栏只读查看内部文本；
 * - 反编译：点 .class 条目（或对整个 jar）→ Worker 经 CFR 反编译→右栏只读 Java 源码（超时/降级/截断态提示）。
 *
 * 仅依赖归档/反编译只读端点（`@/api/archive`），不触碰工作目录写操作。
 */
interface ArchiveViewerProps {
  instanceId: number
  /** 归档文件相对工作目录的路径（.jar/.zip）。 */
  path: string
  /** 归档文件名（标题展示）。 */
  name: string
  onClose: () => void
}

/** 打开的内部条目查看态。 */
interface OpenView {
  /** 条目名（标题）。 */
  title: string
  /** 内容（文本或反编译源码）。 */
  content: string
  /** 用于语法高亮的文件名（.java 走 Java 高亮）。 */
  filename: string
  /** 是否为反编译视图（决定标题徽标）。 */
  decompiled?: boolean
  /** 反编译器标识。 */
  decompiler?: string
  truncated?: boolean
  binary?: boolean
}

export default function ArchiveViewer({ instanceId, path, name, onClose }: ArchiveViewerProps) {
  const { t } = useTranslation()
  const [entries, setEntries] = useState<ArchiveEntry[]>([])
  const [listTruncated, setListTruncated] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  const [view, setView] = useState<OpenView | null>(null)
  const [viewLoading, setViewLoading] = useState(false)

  useEffect(() => {
    let alive = true
    // 切换归档（instanceId/path 变化）时复位列举态再异步拉取，属合法同步。
    /* eslint-disable react-hooks/set-state-in-effect */
    setLoading(true)
    setError('')
    setView(null)
    /* eslint-enable react-hooks/set-state-in-effect */
    listArchiveEntries(instanceId, path)
      .then((res) => {
        if (!alive) return
        setEntries(res.entries)
        setListTruncated(res.truncated)
      })
      .catch((err: unknown) => {
        if (!alive) return
        const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
        setError(msg || t('archive.openFailed'))
      })
      .finally(() => {
        if (alive) setLoading(false)
      })
    return () => {
      alive = false
    }
  }, [instanceId, path, t])

  const tree = useMemo(() => buildEntryTree(entries), [entries])

  const toggle = useCallback((p: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(p)) next.delete(p)
      else next.add(p)
      return next
    })
  }, [])

  /** 查看内部文本条目。 */
  const openTextEntry = useCallback(
    async (entry: string, label: string) => {
      setViewLoading(true)
      try {
        const res = await readArchiveEntry(instanceId, path, entry)
        if (res.binary) {
          setView({
            title: label,
            filename: label,
            content: t('archive.binaryNotice'),
            binary: true,
          })
        } else {
          setView({
            title: label,
            filename: label,
            content: res.text,
            truncated: res.truncated,
          })
        }
      } catch {
        setView({ title: label, filename: label, content: t('archive.readFailed') })
      } finally {
        setViewLoading(false)
      }
    },
    [instanceId, path, t],
  )

  /** 反编译归档内某 .class 条目（entry 为空则反编译整个 jar）。 */
  const decompileEntry = useCallback(
    async (entry: string, label: string) => {
      setViewLoading(true)
      try {
        const res = await decompile(instanceId, path, entry)
        if (!res.success) {
          setView({
            title: label,
            filename: label + '.java',
            content: `// ${t('archive.decompileFailed')}: ${res.error ?? ''}`,
            decompiled: true,
          })
        } else {
          setView({
            title: label,
            filename: (entry || name) + '.java',
            content: res.source,
            decompiled: true,
            decompiler: res.decompiler,
            truncated: res.truncated,
          })
        }
      } catch {
        setView({
          title: label,
          filename: label + '.java',
          content: `// ${t('archive.decompileFailed')}`,
          decompiled: true,
        })
      } finally {
        setViewLoading(false)
      }
    },
    [instanceId, path, name, t],
  )

  const renderNode = (node: EntryNode, depth: number): React.ReactNode => {
    if (node.isDir) {
      const open = expanded.has(node.fullPath)
      return (
        <div key={node.fullPath}>
          <div
            className="flex cursor-pointer items-center gap-1 rounded px-1 py-0.5 text-sm hover:bg-accent/50"
            style={{ paddingLeft: `${depth * 12 + 4}px` }}
            onClick={() => toggle(node.fullPath)}
          >
            {open ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
            {open ? (
              <FolderOpen className="size-4 text-amber-500" />
            ) : (
              <Folder className="size-4 text-amber-500" />
            )}
            <span className="truncate">{node.label}</span>
          </div>
          {open && node.children.map((c) => renderNode(c, depth + 1))}
        </div>
      )
    }
    const isClass = isClassName(node.label)
    return (
      <div
        key={node.fullPath}
        className={cn(
          'flex cursor-pointer items-center gap-1 rounded px-1 py-0.5 text-sm hover:bg-accent/50',
          view?.title === node.label && 'bg-accent font-medium',
        )}
        style={{ paddingLeft: `${depth * 12 + 18}px` }}
        title={isClass ? t('archive.decompileHint') : t('archive.viewHint')}
        onClick={() =>
          isClass
            ? void decompileEntry(node.fullPath, node.label)
            : void openTextEntry(node.fullPath, node.label)
        }
      >
        {isClass ? (
          <FileCode2 className="size-4 shrink-0 text-violet-500" />
        ) : (
          <FileText className="size-4 shrink-0 text-muted-foreground" />
        )}
        <span className="truncate">{node.label}</span>
      </div>
    )
  }

  return (
    <div className="flex min-w-0 flex-1 flex-col">
      <div className="flex items-center justify-between border-b bg-muted/30 px-2 py-1 text-sm">
        <span className="truncate font-medium">
          {t('archive.title')}: {name}
        </span>
        <Button
          size="sm"
          variant="ghost"
          className="h-7 px-1.5"
          title={t('common.close')}
          onClick={onClose}
        >
          <X className="size-3.5" />
        </Button>
      </div>

      <div className="flex min-h-0 flex-1">
        {/* 归档内部条目树 */}
        <div className="w-1/2 min-w-0 overflow-auto border-r p-1">
          {loading ? (
            <p className="p-2 text-sm text-muted-foreground">{t('archive.loading')}</p>
          ) : error ? (
            <p className="p-2 text-sm text-destructive">{error}</p>
          ) : (
            <>
              <div className="mb-1 flex items-center gap-2 px-1">
                <Button
                  size="sm"
                  variant="outline"
                  className="h-6 gap-1 px-2 text-xs"
                  onClick={() => void decompileEntry('', name)}
                  title={t('archive.decompileJarHint')}
                >
                  <FileCode2 className="size-3.5" /> {t('archive.decompileJar')}
                </Button>
                {listTruncated && (
                  <span className="text-xs text-amber-500">{t('archive.listTruncated')}</span>
                )}
              </div>
              {tree.map((n) => renderNode(n, 0))}
            </>
          )}
        </div>

        {/* 内容/反编译视图 */}
        <div className="flex w-1/2 min-w-0 flex-col">
          {viewLoading ? (
            <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
              <Loader2 className="mr-2 size-4 animate-spin" /> {t('archive.processing')}
            </div>
          ) : view ? (
            <>
              <div className="flex items-center justify-between border-b bg-muted/20 px-2 py-1 text-xs">
                <span className="truncate font-medium">{view.title}</span>
                <span className="ml-2 shrink-0 text-muted-foreground">
                  {view.decompiled && view.decompiler ? view.decompiler : ''}
                  {view.truncated && ` · ${t('archive.truncated')}`}
                </span>
              </div>
              <div className="min-h-0 flex-1">
                <CodeEditor value={view.content} filename={view.filename} readOnly />
              </div>
            </>
          ) : (
            <div className="flex flex-1 items-center justify-center p-3 text-center text-sm text-muted-foreground">
              {t('archive.pickEntry')}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
