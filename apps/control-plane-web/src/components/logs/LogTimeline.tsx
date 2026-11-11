import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown } from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import type { LogEntry } from '@/api/logs'
import { logLevelStatus } from '@/lib/logs/logs-filters'

/**
 * 日志结果区：当前页有限分页的 DOM 列表。
 *
 * 当前页最多 100 行，全部保留在 DOM 是刻意选择：日志排障经常需要用浏览器原生
 * 拖拽选字、跨行复制和 Ctrl/Cmd+C。虚拟滚动会卸载离开视口的节点，导致原生选区
 * 在拖动或滚动时断裂；把 100 行变成原生文本比自定义一套选区状态更可靠。
 * 大范围历史由服务端游标分页承载，不把无限数据一次性放进 DOM。
 */
const ROW_HEIGHT = 30

/**
 * 日志时间线（当前页内滚动）。
 *
 * 交互约定：
 *   - 日志正文直接使用浏览器原生文本选择与复制；组件不接管 Ctrl/Cmd+A/C。
 *   - 右侧箭头展开单行完整内容与元数据，正文区域不承担整行按钮行为。
 *   - follow 只在用户明确开启且仍位于顶部时跟随；用户回看时不强制滚动。
 */
export function LogTimeline({
  items,
  total,
  follow,
  filterKey,
  loading = false,
  emptySuccessAllowed = true,
  emptyReason = 'no_data',
  emptyHint = '',
  highlightKeyword,
  sourceLabelOf,
}: {
  items: LogEntry[]
  total: number
  follow: boolean
  /** 筛选身份键变化时只复位日志窗口，不保留上一筛选的滚动位置。 */
  filterKey: string
  /** 查询尚未完成：显示固定高度 loading 槽位，不判空态。 */
  loading?: boolean
  /** false 时禁止把空列表呈成「无日志」成功空态。 */
  emptySuccessAllowed?: boolean
  /** 空结果的原因（决定提示文案与 data-empty-reason 属性）。 */
  emptyReason?: string
  /** 针对该原因的具体提示：为什么空、下一步做什么。 */
  emptyHint?: string
  /** 当前搜索词：用于行内命中高亮。 */
  highlightKeyword?: string
  /** 把一行映射为具体实例名/节点名。 */
  sourceLabelOf: (log: LogEntry) => string
}) {
  const { t } = useTranslation()
  const containerRef = useRef<HTMLDivElement>(null)
  const [scrollTop, setScrollTop] = useState(0)

  // 跟随态只把最新结果滚到顶部；列表本身始终留在固定结果槽位内。
  useEffect(() => {
    if (follow && containerRef.current) {
      containerRef.current.scrollTop = 0
      setScrollTop(0)
    }
  }, [follow, items])

  // 筛选身份变化时回到本页顶部；不触碰浏览器全局选择状态。
  useEffect(() => {
    if (containerRef.current) containerRef.current.scrollTop = 0
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setScrollTop(0)
  }, [filterKey])

  if (loading && items.length === 0) {
    return (
      <div
        data-testid="logs-loading-state"
        className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-6 text-center"
      >
        <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
      </div>
    )
  }

  if (items.length === 0 && !loading) {
    return (
      <div
        data-testid="logs-empty-state"
        data-empty-success={emptySuccessAllowed ? 'true' : 'false'}
        data-empty-reason={emptyReason}
        className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-6 text-center"
      >
        <p className="text-sm text-muted-foreground">
          {emptySuccessAllowed ? t('logs.empty') : t('logsFederation.emptyNotSuccess')}
        </p>
        <p
          data-testid="logs-empty-hint"
          className="max-w-xl text-xs leading-relaxed text-muted-foreground opacity-80"
        >
          {emptyHint}
        </p>
      </div>
    )
  }

  return (
    <div
      ref={containerRef}
      onScroll={(event) => setScrollTop(event.currentTarget.scrollTop)}
      data-testid="logs-virtual"
      data-total-count={total}
      data-rendered-count={items.length}
      data-scroll-top={scrollTop}
      className="min-h-0 flex-1 overflow-y-auto"
    >
      {items.map((log) => (
        <LogRow
          key={log.id}
          log={log}
          keyword={highlightKeyword}
          sourceLabel={sourceLabelOf(log)}
        />
      ))}
    </div>
  )
}

/**
 * 单条日志行：正文保持为普通文本 DOM，让浏览器原生选择、复制和搜索可用。
 * 只有右侧展开箭头是 button；点击正文不会触发整行动作，也不会抢走选区。
 */
export function LogRow({
  log,
  keyword,
  sourceLabel,
}: {
  log: LogEntry
  keyword?: string
  sourceLabel?: string
}) {
  const { t } = useTranslation()
  const [expanded, setExpanded] = useState(false)
  const status = logLevelStatus(log.level)

  const railClass = status === 'danger'
    ? 'bg-status-danger'
    : status === 'warning'
      ? 'bg-status-warning'
      : status === 'info'
        ? 'bg-status-info'
        : 'bg-muted-foreground/40'

  const date = new Date(log.time)
  const hms = Number.isNaN(date.getTime())
    ? log.time
    : `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}:${String(date.getSeconds()).padStart(2, '0')}`

  return (
    <div data-testid="log-row" className="border-b border-border/60">
      <div
        className="flex w-full items-center gap-3 px-2 text-left text-xs transition-colors hover:bg-accent/50"
        style={{ minHeight: ROW_HEIGHT }}
      >
        <span aria-hidden className={cn('h-4 w-0.5 shrink-0 rounded-full', railClass)} />
        <span className="w-14 shrink-0 font-mono text-[11px] tabular-nums text-muted-foreground">
          {hms}
        </span>
        <StatusBadge
          level={status}
          label={t(`logs.level_${log.level}`, log.level)}
          className="w-14 shrink-0 justify-center"
        />
        <span
          className="w-24 shrink-0 truncate text-[11px] text-muted-foreground"
          title={log.sourceRef ?? log.source}
        >
          {sourceLabel ?? t(`logs.source_${log.source}`, log.source)}
        </span>
        <span
          data-testid="log-message"
          className="min-w-0 flex-1 select-text truncate font-mono text-[11px]"
          title={log.message}
        >
          <HighlightedText text={log.message} keyword={keyword} />
        </span>
        <button
          type="button"
          data-testid="log-row-expand"
          aria-expanded={expanded}
          aria-label={t('logs.rowExpand')}
          onClick={() => setExpanded((value) => !value)}
          className="flex size-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        >
          <ChevronDown className={cn('size-3.5 transition-transform', expanded && 'rotate-180')} />
        </button>
      </div>
      {expanded && (
        <div data-testid="log-row-detail" className="border-t border-border/40 bg-muted/30 px-3 py-2">
          <pre className="m-0 max-h-72 select-text overflow-auto whitespace-pre-wrap break-all font-mono text-[11px] leading-relaxed">
            <HighlightedText text={log.message} keyword={keyword} />
          </pre>
          <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
            <span>{date.toLocaleString()}</span>
            {sourceLabel ? <span>{t('logs.rowSource')}: {sourceLabel}</span> : null}
            {log.sourceRef ? <span>source: {log.sourceRef}</span> : null}
            {log.stream ? <span>stream: {log.stream}</span> : null}
            {log.instanceId > 0 && <span>instanceId: {log.instanceId}</span>}
            {log.nodeId > 0 && <span>nodeId: {log.nodeId}</span>}
          </div>
        </div>
      )}
    </div>
  )
}

/** 把搜索词在文本里高亮出来，不使用 dangerouslySetInnerHTML。 */
export function HighlightedText({ text, keyword }: { text: string; keyword?: string }) {
  const query = keyword?.trim()
  if (!query) return <>{text}</>

  const lower = text.toLowerCase()
  const needle = query.toLowerCase()
  const parts: React.ReactNode[] = []
  let from = 0
  let hit = lower.indexOf(needle, from)
  let key = 0
  while (hit !== -1) {
    if (hit > from) parts.push(text.slice(from, hit))
    parts.push(
      <mark key={key++} className="rounded-sm bg-status-warning/30 px-0.5 text-inherit">
        {text.slice(hit, hit + needle.length)}
      </mark>,
    )
    from = hit + needle.length
    hit = lower.indexOf(needle, from)
  }
  if (from < text.length) parts.push(text.slice(from))
  return <>{parts}</>
}
