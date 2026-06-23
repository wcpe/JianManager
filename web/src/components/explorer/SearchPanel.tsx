import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Search, X, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { searchFiles, type SearchHit, type SearchMode } from '@/api/files'

/**
 * 跨文件搜索面板（FR-074，见 ADR-017）。
 *
 * 关键字 → 经 Worker 本地倒排索引查询 → 命中列表（文件/行/片段）；点击命中跳编辑器定位到行。
 * 支持两种模式：content（全文）与 filename（文件名快速打开）。输入防抖后触发查询。
 */
interface SearchPanelProps {
  /** 实例 ID。 */
  instanceId: number
  /** 点击命中：打开文件并定位到行（filename 模式 line=0）。 */
  onOpenHit: (path: string, line: number) => void
  /** 关闭搜索面板。 */
  onClose: () => void
}

export default function SearchPanel({ instanceId, onOpenHit, onClose }: SearchPanelProps) {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const [mode, setMode] = useState<SearchMode>('content')
  const [hits, setHits] = useState<SearchHit[]>([])
  const [truncated, setTruncated] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [searched, setSearched] = useState(false)
  // 自增请求序号：仅最后一次请求的结果被采纳，避免防抖竞态导致旧结果覆盖新结果。
  const reqSeq = useRef(0)

  const runSearch = useCallback(
    async (q: string, m: SearchMode) => {
      const trimmed = q.trim()
      if (!trimmed) {
        setHits([])
        setTruncated(false)
        setSearched(false)
        setError('')
        return
      }
      const seq = ++reqSeq.current
      setLoading(true)
      setError('')
      try {
        const res = await searchFiles(instanceId, trimmed, m)
        if (seq !== reqSeq.current) return
        setHits(res.hits)
        setTruncated(res.truncated)
        setSearched(true)
      } catch (err: unknown) {
        if (seq !== reqSeq.current) return
        const axiosMsg = (err as { response?: { data?: { message?: string } } })?.response?.data
          ?.message
        setError(axiosMsg || t('search.failed'))
        setHits([])
        setTruncated(false)
        setSearched(true)
      } finally {
        if (seq === reqSeq.current) setLoading(false)
      }
    },
    [instanceId, t],
  )

  // 输入/模式变化后防抖触发查询。
  useEffect(() => {
    const id = setTimeout(() => void runSearch(query, mode), 300)
    return () => clearTimeout(id)
  }, [query, mode, runSearch])

  return (
    <div className="flex h-full min-w-0 flex-col">
      {/* 头部：输入 + 模式 + 关闭 */}
      <div className="flex flex-col gap-1.5 border-b bg-muted/30 px-2 py-1.5">
        <div className="flex items-center gap-1">
          <div className="relative flex-1">
            <Search className="absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              autoFocus
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t('search.placeholder')}
              className="h-7 pl-7 text-xs"
              onKeyDown={(e) => {
                if (e.key === 'Enter') void runSearch(query, mode)
                if (e.key === 'Escape') onClose()
              }}
            />
          </div>
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
        <div className="flex items-center gap-1">
          <Button
            size="sm"
            variant={mode === 'content' ? 'default' : 'outline'}
            className="h-6 px-2 text-xs"
            onClick={() => setMode('content')}
          >
            {t('search.modeContent')}
          </Button>
          <Button
            size="sm"
            variant={mode === 'filename' ? 'default' : 'outline'}
            className="h-6 px-2 text-xs"
            onClick={() => setMode('filename')}
          >
            {t('search.modeFilename')}
          </Button>
          {loading && <Loader2 className="ml-1 size-3.5 animate-spin text-muted-foreground" />}
        </div>
      </div>

      {/* 结果列表 */}
      <div className="min-h-0 flex-1 overflow-auto">
        {error && <div className="p-2 text-xs text-destructive">{error}</div>}
        {!error && searched && hits.length === 0 && !loading && (
          <div className="p-2 text-xs text-muted-foreground">{t('search.noResults')}</div>
        )}
        {!error && hits.length > 0 && (
          <ul className="divide-y">
            {hits.map((h, i) => (
              <li key={`${h.path}:${h.line}:${i}`}>
                <button
                  className="flex w-full flex-col items-start gap-0.5 px-2 py-1.5 text-left hover:bg-accent"
                  onClick={() => onOpenHit(h.path, h.line)}
                >
                  <span className="flex w-full items-baseline gap-1 truncate text-xs font-medium">
                    <span className="truncate">{h.path}</span>
                    {h.line > 0 && (
                      <span className="shrink-0 text-muted-foreground">:{h.line}</span>
                    )}
                  </span>
                  {h.snippet && (
                    <span className="w-full truncate font-mono text-[11px] text-muted-foreground">
                      {h.snippet}
                    </span>
                  )}
                </button>
              </li>
            ))}
          </ul>
        )}
        {truncated && (
          <div className="px-2 py-1 text-[11px] text-muted-foreground">{t('search.truncated')}</div>
        )}
      </div>
    </div>
  )
}
