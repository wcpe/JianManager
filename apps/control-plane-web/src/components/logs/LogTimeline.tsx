import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, ChevronDown, Copy } from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import type { LogEntry } from '@/api/logs'
import { copyToClipboard } from '@/lib/shared/clipboard'
import { computeVirtualWindow, logLevelStatus } from '@/lib/logs/logs-filters'
import { formatLogsForCopy } from '@/lib/logs/logs-copy'

/**
 * 日志列表：虚拟滚动 + 多选复制。
 *
 * # 为何把选择/复制放进虚拟滚动里
 *
 * 行是虚拟渲染的（只挂视口附近几十行），选择状态**不能**存在行内 state：
 * 滚出视口再滚回来就会丢。选中集必须提升到这个列表组件，按日志 id 记账。
 *
 * # 为何不用原生文本选区
 *
 * 日志行是固定高度、等宽字体的表格形态，原生选区在虚拟滚动下会因行被卸载
 * 而断裂（拖到一半 DOM 节点没了）。所以自己实现区间选择：按下行 → 记录锚点 →
 * 拖过若干行 → 选中整个区间。这与 Kibana、Datadog 的日志列表一致。
 */

// 虚拟滚动：固定行高 + 视口上下各预渲染 8 行。
const ROW_HEIGHT = 30
const OVERSCAN = 8
const VIEWPORT_HEIGHT = 460

/** 列表与行共用同一套交互约定，便于测试与后续复用。 */
export interface LogListSelectionApi {
  selected: Set<number>
  /** 切换单行选中。 */
  toggle: (id: number, event?: { shiftKey?: boolean; metaKey?: boolean; ctrlKey?: boolean }) => void
  /** 全选/清空当前页。 */
  toggleAll: () => void
  /** 复制选中行（无选中时不复制）。 */
  copySelected: () => Promise<void>
}

/**
 * 日志时间线（虚拟滚动）。
 *
 * 交互约定（对齐 Kibana / Datadog）：
 *   - 单击行 → 选中/取消该行
 *   - Shift + 单击 → 从锚点到该行整段选中
 *   - 按住鼠标拖过若干行 → 整段选中（跨行选择的主要方式）
 *   - Ctrl/Cmd + A → 全选当前页
 *   - Ctrl/Cmd + C → 复制选中行（无选中时不管）
 *   - 右侧留一个展开按钮 → 看完整正文与元数据（为了避免“点一下到底是选中还是展开”的歧义）
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
  onCopyResult,
}: {
  items: LogEntry[]
  total: number
  follow: boolean
  /**
   * 筛选身份键（view/来源/在线过滤/实例/节点/级别/关键字/范围；**不含 page** ✗——翻页需保留滚动位置）。
   * 变化即重置滚动与选择：切实例后把上一实例的选中行带过来再复制，会把 A 实例的日志
   * 当成 B 实例的输出。
   */
  filterKey: string
  /** 查询尚未完成：此时不判空态，避免把「未就绪」误报为空结果。 */
  loading?: boolean
  /** false 时禁止把空列表呈成「无日志」成功空态。 */
  emptySuccessAllowed?: boolean
  /** 空结果的原因（决定提示文案与 data-empty-reason 属性，便于测试断言）。 */
  emptyReason?: string
  /** 针对该原因的具体提示：为什么空、下一步做什么。 */
  emptyHint?: string
  /** 当前搜索词：用于行内命中高亮。 */
  highlightKeyword?: string
  /** 把一行映射为「具体是谁」显示名（实例名/节点名）。 */
  sourceLabelOf: (log: LogEntry) => string
  /** 复制结果回调：由页面弹 toast（列表不自带通知组件）。 */
  onCopyResult?: (ok: boolean, count: number) => void
}) {
  const { t } = useTranslation()
  const containerRef = useRef<HTMLDivElement>(null)
  const [scrollTop, setScrollTop] = useState(0)
  const [selected, setSelected] = useState<Set<number>>(() => new Set())
  // 锚点：Shift 区间选择的起点（存索引而非 id，因为“区间”是视觉区间）。
  const anchorRef = useRef<number | null>(null)
  // 拖拽中：null = 未拖拽；数字 = 拖拽起点索引。
  const draggingRef = useRef<number | null>(null)
  const [dragging, setDragging] = useState(false)

  /**
   * 视口高度**实测**，不写死。
   *
   * 原实现 `VIEWPORT_HEIGHT = 460` 与容器实际高度无关：小屏下列表区只占半截，
   * 大屏下下方留一大片空白；而虚拟滚动的窗口计算恰恰依赖这个高度（算多渲染几行）。
   * 容器高度由工具栏、洞察栏、覆盖横幅、页脚共同决定，它们会随状态变化，
   * 所以必须盯容器自身（ResizeObserver）而不是 window.innerHeight。
   */
  const [viewportHeight, setViewportHeight] = useState(VIEWPORT_HEIGHT)
  useEffect(() => {
    const el = containerRef.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver((entries) => {
      const h = entries[0]?.contentRect.height ?? 0
      if (h > 0) setViewportHeight(h)
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  // 跟随态：新数据到来时滚回顶部，保证最新行可见。
  useEffect(() => {
    if (follow && containerRef.current) {
      containerRef.current.scrollTop = 0
      setScrollTop(0)
    }
  }, [follow, items])

  // 筛选变化（非翻页）⇒ 滚动与选择一并复位。
  useEffect(() => {
    if (containerRef.current) containerRef.current.scrollTop = 0
    // 外部系统同步（DOM scrollTop）而非派生 state，同一次提交内完成。
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setScrollTop(0)
    setSelected(new Set())
    anchorRef.current = null
  }, [filterKey])

  // 拖拽结束：在 window 上监听而不是行上，否则松手在列表外就解不掉。
  useEffect(() => {
    if (!dragging) return
    const stop = () => {
      draggingRef.current = null
      setDragging(false)
    }
    window.addEventListener('mouseup', stop)
    return () => window.removeEventListener('mouseup', stop)
  }, [dragging])

  const selectRange = useCallback((from: number, to: number, additive = false) => {
    const start = Math.min(from, to)
    const end = Math.max(from, to)
    setSelected((current) => {
      const next = additive ? new Set(current) : new Set<number>()
      for (let i = start; i <= end; i += 1) {
        const item = items[i]
        if (item) next.add(item.id)
      }
      return next
    })
  }, [items])

  const toggle = useCallback((id: number, index: number, event?: { shiftKey?: boolean; metaKey?: boolean; ctrlKey?: boolean }) => {
    if (event?.shiftKey && anchorRef.current !== null) {
      selectRange(anchorRef.current, index)
      return
    }
    anchorRef.current = index
    setSelected((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [selectRange])

  const toggleAll = useCallback(() => {
    setSelected((current) => current.size === items.length ? new Set() : new Set(items.map((it) => it.id)))
  }, [items])

  const copySelected = useCallback(async () => {
    const chosen = items.filter((it) => selected.has(it.id))
    if (!chosen.length) return
    const text = formatLogsForCopy(chosen, { sourceLabelOf })
    const ok = await copyToClipboard(text)
    onCopyResult?.(ok, chosen.length)
  }, [items, selected, sourceLabelOf, onCopyResult])

  const win = computeVirtualWindow({
    scrollTop,
    viewportHeight,
    rowHeight: ROW_HEIGHT,
    total: items.length,
    overscan: OVERSCAN,
  })
  const visible = items.slice(win.startIndex, win.endIndex)

  // 查询尚未完成时不判空：交由页面 loading 分支呈现，避免把「未就绪」误报为空结果。
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
        {/* 为什么空、下一步做什么。
         * 三类原因的处置完全不同，却共用一句「暂无日志」：
         *   - 选了「平台」：设计如此，重试一万次也还是空；
         *   - 选了「节点」但没登记源：要去节点配置，不是等；
         *   - 覆盖未校验：等一下重试就行。 */}
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
    <div className="flex min-h-0 flex-1 flex-col">
      {/* 选择条：有选中才出现，把“选中了几条 / 复制 / 清除”放在指尖可达处。
          固定高度占位不采用：空白条会白占一行，反而把列表压矮。 */}
      {selected.size > 0 && (
        <div
          data-testid="logs-selection-bar"
          data-selected-count={selected.size}
          className="flex shrink-0 items-center gap-2 border-b bg-accent/40 px-2 py-1 text-[11px]"
        >
          <Check className="size-3 shrink-0 text-status-success" />
          <span>{t('logs.selectedCount', { count: selected.size })}</span>
          <button
            type="button"
            data-testid="logs-copy-selected"
            onClick={() => void copySelected()}
            className="ml-1 inline-flex items-center gap-1 rounded-sm bg-background px-1.5 py-0.5 text-[11px] hover:bg-accent"
          >
            <Copy className="size-3" />
            {t('logs.copySelected')}
          </button>
          <button
            type="button"
            onClick={() => void toggleAll()}
            className="rounded-sm px-1.5 py-0.5 text-[11px] text-muted-foreground hover:bg-accent"
          >
            {selected.size === items.length ? t('logs.clearSelection') : t('logs.selectAll')}
          </button>
          <button
            type="button"
            onClick={() => { setSelected(new Set()); anchorRef.current = null }}
            className="rounded-sm px-1.5 py-0.5 text-[11px] text-muted-foreground hover:bg-accent"
          >
            {t('logs.clearSelection')}
          </button>
          <span className="ml-auto hidden text-muted-foreground sm:inline">
            {t('logs.copyHint')}
          </span>
        </div>
      )}
      <div
        ref={containerRef}
        onScroll={(e) => setScrollTop(e.currentTarget.scrollTop)}
        // 列表获得焦点后才接 Ctrl+A / Ctrl+C：否则会抢走搜索框里的同名快捷键。
        tabIndex={0}
        onKeyDown={(e) => {
          if (!(e.ctrlKey || e.metaKey)) return
          if (e.key === 'a' || e.key === 'A') { e.preventDefault(); void toggleAll() }
          else if (e.key === 'c' || e.key === 'C') { e.preventDefault(); void copySelected() }
        }}
        data-testid="logs-virtual"
        data-total-count={total}
        className="min-h-0 flex-1 overflow-y-auto focus-visible:outline-none"
      >
        <div style={{ height: win.padTop }} />
        {visible.map((log, offset) => {
          const index = win.startIndex + offset
          return (
            <LogRow
              key={log.id}
              log={log}
              keyword={highlightKeyword}
              selected={selected.has(log.id)}
              sourceLabel={sourceLabelOf(log)}
              onClick={(event) => toggle(log.id, index, event)}
              onMouseDown={(event) => {
                // Shift 按下时**不得**重置锚点：那会把「从第 1 行选到第 3 行」变成“只选第 3 行”，
                // 因为 mousedown 先于 click 发生，锚点已被改成目标行。
                if (event.shiftKey) return
                draggingRef.current = index
                setDragging(true)
                anchorRef.current = index
                // 拖拽起手不立即改选中：单击与拖拽区分开，否则单击永远变成“选中一段”。
              }}
              onMouseEnter={() => {
                if (draggingRef.current === null) return
                selectRange(draggingRef.current, index)
              }}
            />
          )
        })}
        <div style={{ height: win.padBottom }} />
      </div>
    </div>
  )
}

/**
 * 单条日志行。
 *
 * # 设计取舍（对齐 Kibana Discover / Grafana Explore 的通行做法）
 *
 *  1. **左侧级别色条**：比徒标更快一眼定位，并且不占宽度。扫日志时人的视线
 *     沿左边缘下移，颜色块比文字更快被识别。
 *  2. **时间只显时分秒**：原来把完整日期塞进每一行，而 99% 的排查都在同一天内，
 *     日期全是重复噪音。完整时间放 hover 与展开区。
 *  3. **来源显示具体名称**：`inst:2/file` 没人看得懂，写「实例」又丢了“是哪个”。
 *     正确形态是实例名（如 `logtest-mc`）。
 *  4. **关键字高亮**：搜什么就高亮什么。
 *  5. **选中高亮 + 展开分离**：点行是选中（拖拽多选的前提），右侧箭头才展开。
 */
export function LogRow({
  log,
  keyword,
  selected = false,
  sourceLabel,
  onClick,
  onMouseDown,
  onMouseEnter,
}: {
  log: LogEntry
  keyword?: string
  selected?: boolean
  /** 具体来源名称（实例名/节点名）。 */
  sourceLabel?: string
  onClick?: (event: React.MouseEvent) => void
  onMouseDown?: (event: React.MouseEvent) => void
  onMouseEnter?: (event: React.MouseEvent) => void
}) {
  const { t } = useTranslation()
  const [expanded, setExpanded] = useState(false)
  const status = logLevelStatus(log.level)

  // 级别色条：直接吃状态 token，与告警页/标签页同口径，不另造一套颜色。
  const railClass = status === 'danger'
    ? 'bg-status-danger'
    : status === 'warning'
      ? 'bg-status-warning'
      : status === 'info'
        ? 'bg-status-info'
        : 'bg-muted-foreground/40'

  const d = new Date(log.time)
  // 时分秒：排查时看的是“什么时候”，不是“哪一年”。
  const hms = Number.isNaN(d.getTime())
    ? log.time
    : `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}:${String(d.getSeconds()).padStart(2, '0')}`

  return (
    <div
      data-testid="log-row"
      data-selected={selected ? 'true' : 'false'}
      className={cn('border-b border-border/60', selected && 'bg-accent/50')}
    >
      <div
        className={cn(
          'flex w-full select-none items-center gap-3 px-2 text-left text-xs transition-colors',
          !selected && 'hover:bg-accent/50',
        )}
        style={{ height: ROW_HEIGHT }}
      >
        <button
          type="button"
          data-testid="log-row-toggle"
          onClick={onClick}
          onMouseDown={onMouseDown}
          onMouseEnter={onMouseEnter}
          title={new Date(log.time).toLocaleString()}
          className="flex min-w-0 flex-1 items-center gap-3 text-left focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        >
          {/* 选中态额外给一个勾：仅靠背景色区分在小比例尺显示器上不够明确。 */}
          <span aria-hidden className={cn('h-4 w-0.5 shrink-0 rounded-full', railClass)} />
          <span className="w-4 shrink-0 text-status-success">{selected ? <Check className="size-3" /> : null}</span>
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
            {/* 名称缺失时源标签已包含 #id，仍比泛称有用。 */}
            {sourceLabel ?? t(`logs.source_${log.source}`, log.source)}
          </span>
          <span className="min-w-0 flex-1 truncate font-mono text-[11px]">
            <HighlightedText text={log.message} keyword={keyword} />
          </span>
        </button>
        <button
          type="button"
          data-testid="log-row-expand"
          aria-expanded={expanded}
          aria-label={t('logs.rowExpand')}
          onClick={() => setExpanded((v) => !v)}
          className="flex size-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        >
          <ChevronDown className={cn('size-3.5 transition-transform', expanded && 'rotate-180')} />
        </button>
      </div>
      {expanded && (
        <div
          data-testid="log-row-detail"
          className="border-t border-border/40 bg-muted/30 px-3 py-2"
        >
          <pre className="m-0 max-h-72 overflow-auto whitespace-pre-wrap break-all font-mono text-[11px] leading-relaxed">
            <HighlightedText text={log.message} keyword={keyword} />
          </pre>
          <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
            <span>{new Date(log.time).toLocaleString()}</span>
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

/**
 * 把搜索词在文本里高亮出来（大小写不敏感，支持多处命中）。
 *
 * # 为何不引第三方高亮库
 *
 * 只需要「把一段纯文本按关键字切成片段」，一个 `indexOf` 循环就够；
 * 而引入 mark.js 之类会给日志页码上一个只为十几行逻辑服务的依赖。
 *
 * 注意用 `split` 拼 React 节点而不是 `dangerouslySetInnerHTML`：日志内容是**不可信输入**
 * （可能是任意进程写的），拼接 HTML 等于给自己开一个 XSS 口。
 */
export function HighlightedText({ text, keyword }: { text: string; keyword?: string }) {
  const kw = keyword?.trim()
  if (!kw) return <>{text}</>

  const lower = text.toLowerCase()
  const needle = kw.toLowerCase()
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
