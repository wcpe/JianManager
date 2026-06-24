import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Search, X, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { searchFiles, type SearchHit, type SearchMode } from '@/api/files'

/** 「索引中」自动重试间隔（毫秒，FR-113）。 */
const INDEXING_RETRY_MS = 1000

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
  // 索引首建未就绪（FR-113，ADR-024）：显示「索引中」并自动重试同一查询，直到出结果。
  const [indexing, setIndexing] = useState(false)
  // 自增请求序号：仅最后一次请求的结果被采纳，避免防抖竞态导致旧结果覆盖新结果。
  const reqSeq = useRef(0)
  // 「索引中」自动重试计时器：新查询或卸载时清除，避免旧查询的重试覆盖。
  const retryTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  // 指向最新 runSearch，供「索引中」重试间接调用（避免 useCallback 递归自引用）。
  const runSearchRef = useRef<(q: string, m: SearchMode) => void>(() => {})

  const runSearch = useCallback(
    async (q: string, m: SearchMode) => {
      // 新一次查询取消上一次的「索引中」重试。
      if (retryTimer.current) {
        clearTimeout(retryTimer.current)
        retryTimer.current = null
      }
      const trimmed = q.trim()
      if (!trimmed) {
        setHits([])
        setTruncated(false)
        setSearched(false)
        setIndexing(false)
        setError('')
        return
      }
      const seq = ++reqSeq.current
      setLoading(true)
      setError('')
      try {
        const res = await searchFiles(instanceId, trimmed, m)
        if (seq !== reqSeq.current) return
        if (res.indexing) {
          // 索引首建中：本次无结果，显示进度并稍后用同一查询自动重试。
          setIndexing(true)
          setHits([])
          setTruncated(false)
          setSearched(true)
          retryTimer.current = setTimeout(() => runSearchRef.current(q, m), INDEXING_RETRY_MS)
          return
        }
        setIndexing(false)
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
        setIndexing(false)
        setSearched(true)
      } finally {
        if (seq === reqSeq.current) setLoading(false)
      }
    },
    [instanceId, t],
  )

  // 保持 runSearchRef 指向最新 runSearch（供重试间接调用）。
  useEffect(() => {
    runSearchRef.current = runSearch
  }, [runSearch])

  // 输入/模式变化后防抖触发查询。
  useEffect(() => {
    const id = setTimeout(() => void runSearch(query, mode), 300)
    return () => clearTimeout(id)
  }, [query, mode, runSearch])

  // 卸载时清除待执行的「索引中」重试。
  useEffect(() => () => {
    if (retryTimer.current) clearTimeout(retryTimer.current)
  }, [])

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
          {(loading || indexing) && (
            <Loader2 className="ml-1 size-3.5 animate-spin text-muted-foreground" />
          )}
        </div>
      </div>

      {/* 结果列表 */}
      <div className="min-h-0 flex-1 overflow-auto">
        {error && <div className="p-2 text-xs text-destructive">{error}</div>}
        {!error && indexing && (
          <div className="p-2 text-xs text-muted-foreground">{t('search.indexing')}</div>
        )}
        {!error && !indexing && searched && hits.length === 0 && !loading && (
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
