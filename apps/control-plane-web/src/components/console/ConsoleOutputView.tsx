import {
  useCallback,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type MouseEvent,
  type ReactNode,
  type Ref,
} from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { cn } from '@jianmanager/ui'

import { copyToClipboard } from '@/lib/clipboard'
import { parseAnsi, type AnsiSegment } from '@/lib/console-ansi'
import type { LogLine } from '@/lib/console-log-line'
import {
  CONSOLE_CELL_BODY,
  CONSOLE_CELL_LEVEL,
  CONSOLE_CELL_SOURCE,
  CONSOLE_CELL_TS,
  isSameMatch,
  matchesForCell,
  type ConsoleSearchMatch,
} from '@/lib/console-search'
import {
  edgeScrollStep,
  isLineSelected,
  linesInRange,
  selectionText,
  seqRangeBounds,
  useEdgeAutoScroll,
  useSeqSelection,
  type SeqRange,
} from '@/lib/console-selection'
import { buildStackBlocks, stackBlockText, visibleConsoleRows, type StackBlock } from '@/lib/console-stack-block'
import { estimateRowLines, type WrapWidthSpec } from '@/lib/console-wrap'
import { useVirtualRows } from '@/lib/virtual-list'

/**
 * 控制台输出区（FR-415，spec §2.1；ADR-086）：**只读** DOM 虚拟列表。
 *
 * 弃 xterm 的直接动因是 canvas 上没有行节点——按级别过滤、行级悬停复制、
 * 异常堆栈折叠、行级高亮（FR-417/418）在 canvas 上一条都做不到。代价是滚动性能、
 * 宽字符、跨行选区要自己保证（ADR-086 代价 2/3）。
 *
 * 只渲染可视窗口 + 上下各 12 行 overscan；行高固定以简化定位（`seq → 下标 → offsetTop`）。
 *
 * FR-418 在此之上加两件事（spec §3）：
 * - **锚点选区**：跨行选区只记 `seq` 区间、文本从行缓冲取，规避「拖出视口即断」
 *   （屏幕外的行根本没有 DOM 节点，见 ADR-086 代价 2）；同一行内不接管，交给原生字符级选区
 * - **堆栈块折叠**：异常头 + 其全部帧折叠成一行摘要，避免一个 NPE 把有用日志挤出整屏
 */

/** 行高相对字号的倍率。固定行高是虚拟列表定位的前提，故不允许按内容自适应。 */
const ROW_HEIGHT_RATIO = 1.6

/** spec §2.1 要求的 overscan。 */
const OVERSCAN = 12

/** 换行估算：行容器左右内边距合计（px-2 → 8px × 2）。 */
const PAD_PX_TOTAL = 16

/** 堆栈块头行的「N 帧」徽标 + 复制钮占位（px，粗估，自校正兜底）。 */
const STACK_DECOR_PX = 76

/** 判定「已在底部」的容差：滚动条像素级抖动不该反复切换跟随态。 */
const BOTTOM_EPSILON = 8

function consoleRowHeight(fontSize: number): number {
  return Math.round(fontSize * ROW_HEIGHT_RATIO)
}

/** 输出区的命令式取值口（FR-417「复制当前可见屏」需要知道屏上到底是哪几行）。 */
export interface ConsoleOutputHandle {
  /**
   * 当前**真正在视口内**的行（不含 overscan）。
   *
   * 从滚动位置反算而非读 DOM：DOM 里躺着 overscan 的 24 行，照搬会把用户看不见的行
   * 也复制进去。无布局信息（jsdom / 离屏）时退化为已渲染窗口。
   */
  getVisibleLines(): readonly LogLine[]
}

export interface ConsoleOutputViewProps {
  lines: readonly LogLine[]
  /** 因环形缓冲溢出被丢弃的行数；>0 时顶部挂「更早日志需回溯」锚点（FR-419 据此加载）。 */
  droppedCount: number
  fontSize: number
  /** 搜索命中（全量）；为空即不高亮。 */
  matches?: readonly ConsoleSearchMatch[]
  /** 当前命中项，用于区分「当前」与「其余」高亮，并驱动滚动定位。 */
  currentMatch?: ConsoleSearchMatch
  /**
   * 级别过滤是否生效（FR-417）：空态文案据此区分「本来就没输出」与
   * 「被过滤器滤空了」——后者说成「暂无输出」会让用户以为服务端没在说话。
   */
  filterActive?: boolean
  onContextMenu?: (event: MouseEvent<HTMLDivElement>) => void
  /**
   * 锚点选区文本变更回调（FR-418）。
   *
   * 锚点模式会关掉原生选区，`window.getSelection()` 因此为空——外层的「复制选区」
   * 必须改从这里拿文本，否则锚点选完再走右键菜单只会复制到空串。
   */
  onSelectionChange?: (text: string) => void
  /** 「选区存为 .log」的文件名前缀（外层给实例标识）。 */
  logNamePrefix?: string
  /**
   * 滚到顶时请求更早一页（FR-419，spec §4.3）。
   *
   * 不传即完全不出现回溯横幅与顶部触发——`/super`、`/director` 等只看实时输出的宿主
   * 不该被拉进历史回溯的语义里。去重与「已到最早」的判定在调用方（回溯控制器）做，
   * 本组件只负责「什么时候该问一声」。
   */
  onLoadEarlier?: () => void
  /** 回溯状态（FR-419），驱动顶部横幅；与 onLoadEarlier 一起给。 */
  history?: ConsoleHistoryBanner
  /**
   * 定位目标（FR-419「跳到本次启动」/「跳到时间…」）。
   * 带 token 而非只给 seq：连续两次跳到同一行时 seq 不变，靠 token 变化重新触发定位。
   */
  jumpTarget?: { seq: number; token: number }
  /** 横幅右侧动作区（「跳到本次启动」/「跳到时间…」按钮，由外层组装并持有其状态）。 */
  historyActions?: ReactNode
  /** 沉浸工作台使用中性深色输出表面，避免沿用普通控制台的靛蓝底。 */
  immersive?: boolean
  /**
   * 自动换行（可用性增强）：长行在容器宽度内折行，不再横向滚动。
   *
   * 实现是**变高虚拟化**：行高 = 估算行数 × rowHeight，估算用字符分类宽度纯数学
   * （console-wrap.ts）；渲染后自校正 pass 用 DOM 实测 scrollHeight 修正已渲染行，
   * 估算只影响未渲染行的滚动条数学。关（默认）时走原等高路径，行为零变化。
   */
  wrap?: boolean
  ref?: Ref<ConsoleOutputHandle>
}

/** 回溯横幅所需状态（FR-419，spec §4.3：标明回溯到哪儿了 + 数据来自哪层）。 */
export interface ConsoleHistoryBanner {
  loading: boolean
  /** 已到数据库最早一条：横幅必须明示「已是最早」，不得静默停住。 */
  exhausted: boolean
  /** 已从数据库回溯的行数。 */
  rowCount: number
  /**
   * 内存环形缓冲里的行数。
   *
   * 由外层直接给而不是拿 `lines.length` 减：`lines` 是**级别过滤后**的行，
   * 相减会算出与实际不符（甚至为负）的「内存行数」。
   */
  memoryCount: number
  /** 已加载历史中最早一行的时间（ISO）。 */
  oldestTime?: string
  error?: string | null
}

/**
 * 距顶多少像素就预取更早一页（FR-419）。
 *
 * 不用「严格等于 0」：滚动到顶再等请求返回会让用户干等一屏空白，
 * 提前一屏左右开始取，视觉上就是「一直有内容」。
 */
const EARLIER_TRIGGER_PX = 240

/** 级别对应的文字色。ERROR/WARN 用 status token，DEBUG/TRACE 压暗，INFO 保持默认。 */
const LEVEL_CLASS: Record<string, string> = {
  ERROR: 'text-red-400',
  WARN: 'text-amber-400',
  DEBUG: 'text-gray-500',
  TRACE: 'text-gray-500',
}

/** 把一列纯文本按命中区间切成 `<mark>` 与普通文本。data 属性沿用旧搜索的契约。 */
function renderCellText(
  text: string,
  cellMatches: readonly ConsoleSearchMatch[],
  currentMatch: ConsoleSearchMatch | undefined,
): ReactNode {
  if (cellMatches.length === 0) return text
  const nodes: ReactNode[] = []
  let cursor = 0
  cellMatches.forEach((match, index) => {
    if (match.start > cursor) nodes.push(text.slice(cursor, match.start))
    const current = isSameMatch(match, currentMatch)
    nodes.push(
      <mark
        key={`${match.cell}-${match.start}-${index}`}
        data-terminal-search-match="true"
        data-terminal-search-current={current ? 'true' : undefined}
        className={cn(
          'rounded px-0.5 text-black',
          current ? 'bg-amber-300 ring-1 ring-amber-100' : 'bg-yellow-300/60',
        )}
      >
        {text.slice(match.start, match.end)}
      </mark>,
    )
    cursor = match.end
  })
  if (cursor < text.length) nodes.push(text.slice(cursor))
  return nodes
}

/** 按 ANSI 片段样式包一层 span；无样式时不加 style，省下大量无用属性。 */
function styledSpan(key: string, segment: AnsiSegment, children: ReactNode): ReactNode {
  if (!segment.color && !segment.background && !segment.bold) return <span key={key}>{children}</span>
  return (
    <span
      key={key}
      style={{ color: segment.color, background: segment.background, fontWeight: segment.bold ? 700 : undefined }}
    >
      {children}
    </span>
  )
}

/**
 * 正文渲染：ANSI 片段 × 搜索命中。
 *
 * 两套区间（着色片段、命中区间）互不对齐，故取**并集切点**逐段渲染，
 * 而不是先高亮再上色——后者会在跨片段命中处把着色 span 撕开、丢掉一半颜色。
 */
function renderBody(
  segments: readonly AnsiSegment[],
  cellMatches: readonly ConsoleSearchMatch[],
  currentMatch: ConsoleSearchMatch | undefined,
): ReactNode {
  if (cellMatches.length === 0) {
    return segments.map((segment, index) => styledSpan(String(index), segment, segment.text))
  }

  const pieces: ReactNode[] = []
  let offset = 0
  segments.forEach((segment, index) => {
    const segStart = offset
    const segEnd = segStart + segment.text.length
    offset = segEnd

    const cuts = new Set<number>([segStart, segEnd])
    for (const match of cellMatches) {
      if (match.start > segStart && match.start < segEnd) cuts.add(match.start)
      if (match.end > segStart && match.end < segEnd) cuts.add(match.end)
    }
    const points = [...cuts].sort((a, b) => a - b)

    for (let k = 0; k < points.length - 1; k++) {
      const from = points[k]
      const to = points[k + 1]
      const hit = cellMatches.find((match) => match.start <= from && match.end >= to)
      const text = segment.text.slice(from - segStart, to - segStart)
      const content = hit ? (
        <mark
          data-terminal-search-match="true"
          data-terminal-search-current={isSameMatch(hit, currentMatch) ? 'true' : undefined}
          className={cn(
            'rounded px-0.5 text-black',
            isSameMatch(hit, currentMatch) ? 'bg-amber-300 ring-1 ring-amber-100' : 'bg-yellow-300/60',
          )}
        >
          {text}
        </mark>
      ) : (
        text
      )
      pieces.push(styledSpan(`${index}-${from}`, segment, content))
    }
  })
  return pieces
}

/** 单行。等高模式固定高度不换行（容器横向滚动）；换行模式按分配高裁剪盒、顶端对齐折行。 */
function ConsoleRow({
  line,
  height,
  wrap = false,
  matches,
  currentMatch,
  block,
  collapsed,
  selected,
  onToggleBlock,
  onCopyBlock,
  onSelectBlock,
}: {
  line: LogLine
  height: number
  /** 自动换行模式：宽度吃满容器、文本折行，高度为虚拟化分配值（估算+自校正）。 */
  wrap?: boolean
  matches: readonly ConsoleSearchMatch[]
  currentMatch: ConsoleSearchMatch | undefined
  /** 本行是某个堆栈块的异常头时给出该块（FR-418，spec §3.2）。 */
  block?: StackBlock
  collapsed?: boolean
  selected?: boolean
  onToggleBlock?: (block: StackBlock) => void
  onCopyBlock?: (block: StackBlock) => void
  onSelectBlock?: (block: StackBlock) => void
}) {
  const { t } = useTranslation()
  // 绝大多数 MC 行不含 ANSI，parseAnsi 内部走快路径；此处 memo 只为滚动时避免重复解析。
  const segments = useMemo(() => parseAnsi(line.body), [line.body])
  const cellMatch = (cell: number) => matchesForCell(matches, line.seq, cell)

  return (
    <div
      data-console-line-seq={line.seq}
      data-console-line-kind={line.kind}
      data-console-line-selected={selected ? 'true' : undefined}
      style={{ height }}
      // 双击异常头 = 选中整块（spec §3.2）：折叠态下帧不在屏上，拖选圈不住它们。
      onDoubleClick={block && onSelectBlock ? () => onSelectBlock(block) : undefined}
      className={cn(
        'group flex gap-2 border-l-2 border-transparent px-2',
        wrap
          ? // 溢出裁剪盒锁在分配高内，但 scrollHeight 仍报完整内容高（自校正的测量口）。
            'w-full items-start overflow-hidden whitespace-pre-wrap [overflow-wrap:break-word]'
          : 'w-max min-w-full items-center whitespace-pre',
        // 警告/报错整行底色 + 左色条：扫读长日志时一眼定位问题行（文字色保留、对齐不偏移
        // ——所有行统一 2px 透明左框，问题行只换框色）。选中高亮优先于级别底色。
        line.level === 'ERROR' && 'border-red-500/60 bg-red-500/10',
        line.level === 'WARN' && 'border-amber-500/60 bg-amber-500/10',
        selected ? 'bg-sky-500/25' : 'hover:bg-white/5',
        line.kind === 'command' && 'text-sky-300',
        line.kind === 'system' && 'text-gray-500 italic',
        line.kind !== 'command' && line.kind !== 'system' && LEVEL_CLASS[line.level ?? ''],
      )}
    >
      {/* 命令回显用 `>` 标记与服务端输出区分：本地回显不等服务端确认（spec §2.2）。 */}
      {line.kind === 'command' && <span className="shrink-0 select-none text-sky-500">{'>'}</span>}
      {block && (
        <button
          type="button"
          data-console-stack-toggle="true"
          onClick={() => onToggleBlock?.(block)}
          aria-expanded={!collapsed}
          aria-label={collapsed ? t('instanceDetail.consoleStackExpand') : t('instanceDetail.consoleStackCollapse')}
          className="shrink-0 select-none rounded px-1 leading-none text-gray-400 hover:bg-white/10 hover:text-gray-100"
        >
          {collapsed ? '▶' : '▼'}
        </button>
      )}
      {line.ts && (
        <span className="shrink-0 text-gray-600">
          {renderCellText(line.ts, cellMatch(CONSOLE_CELL_TS), currentMatch)}
        </span>
      )}
      {line.level && line.kind !== 'command' && line.kind !== 'system' && (
        <span className="w-[3.25rem] shrink-0 text-[0.9em] opacity-80">
          {renderCellText(line.level, cellMatch(CONSOLE_CELL_LEVEL), currentMatch)}
        </span>
      )}
      {line.source && (
        <span className="shrink-0 text-violet-300/80">
          [{renderCellText(line.source, cellMatch(CONSOLE_CELL_SOURCE), currentMatch)}]
        </span>
      )}
      <span
        className={cn(
          // 换行模式下正文吃满前缀列之外的剩余宽度，才有界可折。
          'min-w-0',
          wrap && 'flex-1',
          line.kind === 'stack-frame' && 'pl-4 text-gray-500',
        )}
      >
        {renderBody(segments, cellMatch(CONSOLE_CELL_BODY), currentMatch)}
      </span>
      {block && collapsed && (
        <span className="shrink-0 rounded bg-white/10 px-1.5 text-[0.85em] leading-normal text-gray-300">
          {t('instanceDetail.consoleStackFrames', { count: block.frameSeqs.length })}
        </span>
      )}
      {block && (
        <button
          type="button"
          onClick={() => onCopyBlock?.(block)}
          // 常驻 DOM、只用透明度隐藏：按条件挂载会在悬停瞬间插入节点，让行内容左右抖动。
          className="shrink-0 rounded px-1 text-[0.85em] leading-normal text-gray-400 opacity-0 hover:bg-white/10 hover:text-gray-100 focus:opacity-100 group-hover:opacity-100"
        >
          {t('instanceDetail.consoleStackCopyBlock')}
        </button>
      )}
    </div>
  )
}

export default function ConsoleOutputView({
  lines,
  droppedCount,
  fontSize,
  matches = [],
  currentMatch,
  filterActive = false,
  onContextMenu,
  onSelectionChange,
  logNamePrefix = 'console',
  onLoadEarlier,
  history,
  jumpTarget,
  historyActions,
  immersive = false,
  wrap = false,
  ref,
}: ConsoleOutputViewProps) {
  const { t } = useTranslation()
  const rowHeight = consoleRowHeight(fontSize)
  const rowsRef = useRef<HTMLDivElement>(null)

  // ---- 堆栈块折叠（FR-418，spec §3.2）----
  const [expandedBlocks, setExpandedBlocks] = useState<ReadonlySet<number>>(() => new Set())
  /** 「只看这段」：把选区区间当临时过滤器（spec §3.1）。 */
  const [rangeFilter, setRangeFilter] = useState<SeqRange | null>(null)

  const scoped = useMemo(() => (rangeFilter ? linesInRange(lines, rangeFilter) : lines), [lines, rangeFilter])
  const blocks = useMemo(() => buildStackBlocks(scoped), [scoped])

  // 当前搜索命中落在折叠块里时**临时展开**该块：否则搜索报「第 2/5 项」却一处高亮都看不见。
  // 用派生值而不是 effect 里 setState，省掉一次多余渲染与一份要同步的状态。
  const effectiveExpanded = useMemo(() => {
    const hiddenHead = currentMatch ? scoped.find((line) => line.seq === currentMatch.seq)?.stackOf : undefined
    if (hiddenHead === undefined || !blocks.has(hiddenHead) || expandedBlocks.has(hiddenHead)) return expandedBlocks
    return new Set([...expandedBlocks, hiddenHead])
  }, [blocks, currentMatch, expandedBlocks, scoped])

  /** 屏上真正的行序列：折叠块只剩头行。虚拟列表、几何换算、可见屏取材一律以它为准。 */
  const rows = useMemo(() => visibleConsoleRows(scoped, blocks, effectiveExpanded), [blocks, effectiveExpanded, scoped])

  // ---- 自动换行（wrap）：变高虚拟化 ----
  // 高度 = 估算行数 × rowHeight；估算在渲染前就位（虚拟化排占位需要），
  // 渲染后由下方自校正 pass 以 DOM 实测修正（heightOverrides）。
  // 估算所需容器宽度经 crossW 两跳获得（hook 的 crossSize 晚于 sizes 到来），
  // 首帧按 0 宽退化成单行，挂载后一帧内刷新为真实宽度。
  const [heightOverrides, setHeightOverrides] = useState(() => new Map<number, number>())
  const [crossW, setCrossW] = useState(0)
  const wrapSpec = useMemo<WrapWidthSpec | null>(() => {
    if (!wrap) return null
    let charWidthPx = 0
    let wideWidthPx = 0
    try {
      const canvas = document.createElement('canvas')
      const ctx = canvas.getContext('2d')
      if (ctx) {
        const font = `${fontSize}px ui-monospace, SFMono-Regular, Menlo, Consolas, monospace`
        ctx.font = font
        charWidthPx = ctx.measureText('0'.repeat(100)).width / 100
        wideWidthPx = ctx.measureText('中中中').width / 3
      }
    } catch { /* jsdom：getContext 未实现，走折算回退 */ }
    if (!(charWidthPx > 0)) charWidthPx = fontSize * 0.62
    if (!(wideWidthPx > 0)) wideWidthPx = Math.max(fontSize, charWidthPx * 1.9)
    return { charWidthPx, wideWidthPx, safetyPx: 2 }
  }, [fontSize, wrap])

  const heights = useMemo(() => {
    if (!wrap || !wrapSpec) return null
    const rowWidthPx = Math.max(0, crossW - PAD_PX_TOTAL)
    return rows.map((line) => {
      const overridden = heightOverrides.get(line.seq)
      if (overridden) return overridden * rowHeight
      const est = estimateRowLines({
        ts: line.ts,
        level: line.level,
        source: line.source,
        body: line.body,
        kind: line.kind,
        blockDecorPx: blocks.has(line.seq) ? STACK_DECOR_PX : undefined,
        rowWidthPx,
        spec: wrapSpec,
      })
      return Math.max(1, est) * rowHeight
    })
  }, [blocks, crossW, heightOverrides, rowHeight, rows, wrap, wrapSpec])

  const { containerRef, onScroll, range, offsets, crossSize } = useVirtualRows({
    total: rows.length,
    itemSize: rowHeight,
    overscan: OVERSCAN,
    sizes: heights ?? undefined,
  })

  useEffect(() => {
    // crossSize（来自 hook 的 ResizeObserver）镜像进 state，供下一轮 heights 消费。
    // 放在 hook 之后：crossSize 是 hook 的返回值。
    setCrossW((prev) => (prev === crossSize ? prev : crossSize))
  }, [crossSize])

  /** 第 index 行的起始偏移；等高模式下即 index × rowHeight。 */
  const offsetAt = useCallback(
    (index: number): number => (offsets ? (offsets[index] ?? 0) : index * rowHeight),
    [offsets, rowHeight],
  )
  /** 第 index 行的高度；等高模式恒为 rowHeight。 */
  const heightAt = useCallback(
    (index: number): number => (heights ? (heights[index] ?? rowHeight) : rowHeight),
    [heights, rowHeight],
  )
  /** 包含偏移 y 的行下标（变高二分 / 等高除法）。 */
  const indexAtOffset = useCallback(
    (y: number): number => {
      if (!offsets) return Math.max(0, Math.floor(y / rowHeight))
      let lo = 0
      let hi = rows.length - 1
      let ans = 0
      while (lo <= hi) {
        const mid = (lo + hi) >> 1
        if (offsets[mid] <= y) {
          ans = mid
          lo = mid + 1
        } else {
          hi = mid - 1
        }
      }
      return ans
    },
    [offsets, rowHeight, rows.length],
  )

  // 自动滚底 + 上滚暂停（spec §2.1）。
  const [follow, setFollow] = useState(true)
  /** 离开底部那一刻的最新 seq。未读数由它与当前 seq 相减推出，不再单独累加计数。 */
  const [pausedAtSeq, setPausedAtSeq] = useState(-1)

  const lastSeq = lines.length > 0 ? lines[lines.length - 1].seq : -1
  // 用 seq 差而非数组长度差：环形缓冲丢最旧时长度可能不变，长度差会误判成「没有新行」。
  const unseen = follow ? 0 : Math.max(0, lastSeq - pausedAtSeq)

  // 跟随态下把视口推到底。effect 只同步 DOM（外部系统），不写 state。
  useLayoutEffect(() => {
    if (!follow) return
    const el = containerRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [containerRef, follow, lastSeq])

  // 搜索定位：把当前命中行滚到视口中部（变高时含行高中点）。命中在底部时不夺回跟随态。
  const currentSeq = currentMatch?.seq
  useLayoutEffect(() => {
    if (currentSeq === undefined) return
    const el = containerRef.current
    if (!el) return
    const index = rows.findIndex((line) => line.seq === currentSeq)
    if (index < 0) return
    const contentTop = rowsRef.current?.offsetTop ?? 0
    el.scrollTop = Math.max(0, contentTop + offsetAt(index) + heightAt(index) / 2 - el.clientHeight / 2)
    // rows 故意不入依赖：每来一行都重新定位会把用户从命中处甩走。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [containerRef, currentSeq, heightAt, offsetAt, rowHeight])

  // ---- 历史回溯的滚动锚定（FR-419，spec §4.3）----
  //
  // 虚拟列表 prepend 的经典坑：顶部插进 K 行后，同一个 scrollTop 指向的内容整体下移 K 行，
  // 视口会「跳」到用户刚看的位置之上，读日志的人瞬间失去上下文。
  //
  // 固定行高让补偿是精确的：拿**上一帧的首行 seq** 在新序列里的下标当作「前面插了多少行」，
  // 按 `插入行数 × 行高` 加回 scrollTop，视口里的内容像素级原地不动。
  // 用「旧首行的新下标」而不是「长度差」：环形缓冲同一帧可能既在顶部插历史、又从尾部丢最旧，
  // 长度差会把两件事混成一个错误的数字。变高模式下补偿量 = 新序列里 prevFirst 的起始偏移
  // （它原本是第 0 行，旧偏移恒为 0），等高时即 inserted × rowHeight，与原行为一致。
  const firstRowSeqRef = useRef<number | null>(null)
  useLayoutEffect(() => {
    const nextFirst = rows.length > 0 ? rows[0].seq : null
    const prevFirst = firstRowSeqRef.current
    firstRowSeqRef.current = nextFirst
    const el = containerRef.current
    // 仅在「首行变得更早」时补偿：首行变新是尾部追加/丢最旧，不该动 scrollTop。
    if (!el || prevFirst === null || nextFirst === null || nextFirst >= prevFirst) return
    const inserted = rows.findIndex((line) => line.seq === prevFirst)
    if (inserted <= 0) return
    el.scrollTop += offsetAt(inserted)
    // 程序化改 scrollTop 不保证同步派发 scroll 事件，虚拟窗口的指标会停在旧偏移上
    // （表现为补偿后渲染的还是旧那一段）。补一次事件，走与真滚动完全相同的那条路。
    el.dispatchEvent(new Event('scroll'))
  }, [containerRef, offsetAt, rows])

  // ---- 定位到指定行（FR-419「跳到本次启动」/「跳到时间…」）----
  const jumpSeq = jumpTarget?.seq
  const jumpToken = jumpTarget?.token
  useLayoutEffect(() => {
    if (jumpSeq === undefined) return
    const el = containerRef.current
    if (!el) return
    const index = rows.findIndex((line) => line.seq === jumpSeq)
    if (index < 0) return
    const contentTop = rowsRef.current?.offsetTop ?? 0
    // 目标行落在视口偏上（约 1/4 处）：跳到「本次启动」时用户想看的是它**之后**的内容。
    el.scrollTop = Math.max(0, contentTop + offsetAt(index) - el.clientHeight / 4)
    // 补派 scroll 事件而不是在这里 setState：handleScroll 会据新位置把跟随态置为 false
    // （否则下一条实时输出立刻把视口拽回底部，跳转白跳）并重新量虚拟窗口，
    // 一条路径同时办完两件事，也不必在 effect 里同步 setState。
    el.dispatchEvent(new Event('scroll'))
    // rows 故意不入依赖：每来一行都重新定位会把用户从目标处甩走；靠 token 显式重触发。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [containerRef, jumpSeq, jumpToken, offsetAt, rowHeight])

  const handleScroll = () => {
    onScroll()
    const el = containerRef.current
    if (!el) return
    // 接近顶部即预取更早一页（FR-419）。是否真发请求由回溯控制器判定（在途/已到最早时空转），
    // 故这里可以放心每次滚动都问一声。
    if (onLoadEarlier && el.scrollTop <= EARLIER_TRIGGER_PX) onLoadEarlier()
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight <= BOTTOM_EPSILON
    // 刚离开底部：把此刻的 seq 记为未读计数起点。
    if (!atBottom && follow) setPausedAtSeq(lastSeq)
    setFollow(atBottom)
  }

  const jumpToBottom = () => {
    const el = containerRef.current
    if (el) el.scrollTop = el.scrollHeight
    setFollow(true)
  }

  const visible = rows.slice(range.start, range.end)

  // FR-417：把「屏上是哪几行」暴露给复制菜单。从滚动位置反算而非读 DOM——
  // DOM 里还躺着上下各 12 行 overscan，照搬会把用户看不见的行也复制走。
  // 取材用 rows 而非 lines：折叠块（FR-418）的帧不在屏上，「复制可见屏」不该把它们算进去。
  useImperativeHandle(
    ref,
    () => ({
      getVisibleLines: () => {
        const el = containerRef.current
        if (!el || rows.length === 0) return []
        const viewport = el.clientHeight || 0
        // 无布局信息（jsdom / 离屏容器）：退化为已渲染窗口，宁可多几行也不返回空。
        if (viewport <= 0) return rows.slice(range.start, range.end)
        const contentTop = rowsRef.current?.offsetTop ?? 0
        // 变高模式按 offset 二分找「盖住视口顶/底」的逻辑行；等高时是除法。
        const first = indexAtOffset(el.scrollTop - contentTop)
        const last = indexAtOffset(el.scrollTop - contentTop + viewport - 1)
        return rows.slice(first, Math.min(rows.length, last + 1))
      },
    }),
    [containerRef, indexAtOffset, range.end, range.start, rows],
  )

  // ---- 自校正（wrap）：估算只喂虚拟化，已渲染行的高度以 DOM 实测为准 ----
  // 行容器 overflow-hidden 时 scrollHeight 仍是完整内容高——拿它与分配高比较，
  // 实测行数更多就回写 heightOverrides。jsdom 里 scrollHeight 恒 0，自然空转。
  useEffect(() => {
    if (!wrap || !rowsRef.current) return
    const corrections = new Map<number, number>()
    const els = rowsRef.current.querySelectorAll<HTMLElement>('[data-console-line-seq]')
    els.forEach((el) => {
      const seq = Number(el.dataset.consoleLineSeq)
      if (!Number.isFinite(seq)) return
      const allocated = Math.max(1, Math.round(el.clientHeight / rowHeight))
      const needed = Math.ceil(el.scrollHeight / rowHeight)
      if (needed > allocated) corrections.set(seq, needed)
    })
    if (corrections.size === 0) return
    setHeightOverrides((prev) => {
      let changed = false
      const next = new Map(prev)
      corrections.forEach((lines, seq) => {
        if (next.get(seq) !== lines) {
          next.set(seq, lines)
          changed = true
        }
      })
      return changed ? next : prev
    })
    // 窗口/高度一变就重测可见行；修正回写会再次触发本 effect 直至收敛。
  }, [heights, range.end, range.start, rowHeight, wrap])

  // 宽度/字号/开关变化后旧修正全部失效，清掉重估（渲染后自校正会重新收敛）。
  useEffect(() => {
    setHeightOverrides((prev) => (prev.size > 0 ? new Map() : prev))
  }, [crossW, rowHeight, wrap])

  // ---- 锚点选区（FR-418，spec §3.1）----
  const selection = useSeqSelection()
  const selectionRange = selection.range
  /**
   * 进行中的拖拽。
   *
   * `started=false` 表示「按下了但还没跨行」——此时**不进入锚点模式**，
   * 原生字符级选区照常工作，用户仍能精确圈一个 UUID / 坐标（spec §3.1）。
   */
  const dragRef = useRef<{ anchorSeq: number; started: boolean; clientY: number } | null>(null)
  /** 上一次按下的行，供首次 `Shift+Click`（此前还没有区间）有个锚点可用。 */
  const lastClickSeqRef = useRef<number | null>(null)
  /** 仅在锚点拖拽进行中置位：据此关掉原生选区（拖完立刻恢复，否则行内字符选取就废了）。 */
  const [anchorDragging, setAnchorDragging] = useState(false)

  /**
   * 屏幕纵坐标 → 行 seq。
   *
   * 用容器几何换算而不是 `event.target.closest()`：自动滚动时指针是静止的、行在动，
   * 靠事件目标就永远停在同一行；指针落在虚拟列表的占位空白上时也没有行节点可取。
   *
   * 行下标换算统一走 {@link indexAtOffset}：wrap（变高）模式下按 offsets 前缀和二分，
   * 等高模式内部回落为固定行高整除——不要在这里单独整除，变高行高不均时会算错位
   * （评审 P1-2：选区/拖拽扩选的 seq 全部依赖本换算）。
   */
  const seqAtClientY = useCallback(
    (clientY: number): number | null => {
      const el = containerRef.current
      if (!el || rows.length === 0) return null
      const rect = el.getBoundingClientRect()
      const contentTop = rowsRef.current?.offsetTop ?? 0
      const index = indexAtOffset(clientY - rect.top + el.scrollTop - contentTop)
      return rows[Math.min(rows.length - 1, Math.max(0, index))].seq
    },
    [containerRef, indexAtOffset, rows],
  )

  const autoScroll = useEdgeAutoScroll((step) => {
    const el = containerRef.current
    const drag = dragRef.current
    if (!el || !drag?.started) return
    const max = Math.max(0, el.scrollHeight - el.clientHeight)
    el.scrollTop = Math.min(max, Math.max(0, el.scrollTop + step))
    onScroll()
    // 指针不动但行在滚——终点必须按滚动后的几何重算，否则选区停在触发自动滚动的那一行。
    const seq = seqAtClientY(drag.clientY)
    if (seq !== null) selection.extendTo(seq)
  })

  const endDrag = useCallback(() => {
    const drag = dragRef.current
    dragRef.current = null
    autoScroll.stop()
    if (!drag?.started) return
    setAnchorDragging(false)
    // 锚点模式期间浏览器可能仍攒了半截原生选区，清掉以免与区间语义并存。
    window.getSelection()?.removeAllRanges()
  }, [autoScroll])

  const handleDragMove = useCallback(
    (clientY: number, preventDefault: () => void) => {
      const drag = dragRef.current
      const el = containerRef.current
      if (!drag || !el) return
      drag.clientY = clientY
      const seq = seqAtClientY(clientY)
      if (seq === null) return

      if (!drag.started) {
        // 同一行内不进锚点模式（spec §3.1），交给原生字符级选区。
        if (seq === drag.anchorSeq) return
        drag.started = true
        setAnchorDragging(true)
        window.getSelection()?.removeAllRanges()
        selection.setRange({ anchor: drag.anchorSeq, head: seq })
      } else {
        selection.extendTo(seq)
      }
      // 抑制原生跨行选区：它在虚拟列表下必然断裂（ADR-086 代价 2）。
      preventDefault()

      const rect = el.getBoundingClientRect()
      autoScroll.setStep(edgeScrollStep(clientY, { top: rect.top, bottom: rect.bottom }))
    },
    [autoScroll, containerRef, selection, seqAtClientY],
  )

  /**
   * 拖拽期间的 move/up 挂在 window 上：松手常发生在容器外（甚至窗口外），
   * 只听容器事件会留下一个「永远没结束」的拖拽和一个还在跑的自动滚动定时器。
   * 用 latest-ref 转发，避免 rows 每变一次就重绑一遍监听。
   */
  const latestDragHandlers = useRef({ handleDragMove, endDrag })
  useEffect(() => {
    latestDragHandlers.current = { handleDragMove, endDrag }
  }, [endDrag, handleDragMove])
  useEffect(() => {
    const onMove = (event: globalThis.MouseEvent) => {
      if (!dragRef.current) return
      latestDragHandlers.current.handleDragMove(event.clientY, () => event.preventDefault())
    }
    const onUp = () => latestDragHandlers.current.endDrag()
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
    return () => {
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
    }
  }, [])

  const handleMouseDown = (event: MouseEvent<HTMLDivElement>) => {
    if (event.button !== 0) return
    const seq = seqAtClientY(event.clientY)
    if (seq === null) return

    if (event.shiftKey) {
      // Shift+Click 扩选：锚点不动，只挪终点（spec §3.1）。
      event.preventDefault()
      const anchor = selectionRange?.anchor ?? lastClickSeqRef.current ?? seq
      selection.setRange({ anchor, head: seq })
      lastClickSeqRef.current = anchor
      return
    }

    // 不 preventDefault：同一行内的原生字符级选区必须照常工作。
    lastClickSeqRef.current = seq
    dragRef.current = { anchorSeq: seq, started: false, clientY: event.clientY }
    selection.clear()
  }

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'a') {
      // 全选**缓冲**而不是 DOM 里那几十行——这正是锚点选区存在的意义（spec §3.1）。
      event.preventDefault()
      selection.selectAll(lines)
    } else if (event.key === 'Escape') {
      selection.clear()
    }
  }

  const selectedLines = useMemo(() => linesInRange(lines, selectionRange), [lines, selectionRange])

  // 选区文本外送（外层「复制选区」用；锚点模式下原生选区为空，见 onSelectionChange 注释）。
  // 回调走 latest-ref：外层多半写内联箭头函数，直接入依赖会让这个 effect 每渲染都跑一遍。
  const onSelectionChangeRef = useRef(onSelectionChange)
  useEffect(() => {
    onSelectionChangeRef.current = onSelectionChange
  }, [onSelectionChange])
  useEffect(() => {
    onSelectionChangeRef.current?.(selectionRange ? selectionText(lines, selectionRange) : '')
    // lines 不入依赖：区间不动时新日志涌入不改变选区文本，不必每来一行都回调一次。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectionRange])

  const notifyCopy = async (text: string) => {
    if (!text) return
    // 成功/失败都必须有回执：HTTP 非安全上下文下连 execCommand 兜底也可能失败（FIX-1/FIX-2）。
    const ok = await copyToClipboard(text)
    if (ok) toast.success(t('common.copied'))
    else toast.error(t('common.copyFailed'))
  }

  const copySelection = () => {
    // 文本从行缓冲按 seq 区间取，**不从 DOM 取**——屏幕外的行没有节点（spec §3.1）。
    void notifyCopy(selectionText(lines, selectionRange))
  }

  const saveSelection = () => {
    if (!selectionRange) return
    const text = selectionText(lines, selectionRange)
    if (!text) return
    const { start, end } = seqRangeBounds(selectionRange)
    const blob = new Blob([text], { type: 'text/plain;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `${logNamePrefix}-${start}-${end}.log`
    a.click()
    URL.revokeObjectURL(url)
  }

  const onlyThisRange = () => {
    if (!selectionRange) return
    setRangeFilter(selectionRange)
    selection.clear()
  }

  const toggleBlock = (block: StackBlock) => {
    setExpandedBlocks((prev) => {
      const next = new Set(prev)
      if (!next.delete(block.headSeq)) next.add(block.headSeq)
      return next
    })
  }

  const copyBlock = (block: StackBlock) => {
    void notifyCopy(stackBlockText(lines, block))
  }

  const selectBlock = (block: StackBlock) => {
    selection.setRange({ anchor: block.headSeq, head: block.lastSeq })
  }

  return (
    // flex 列而非单纯 relative：回溯横幅是**滚动容器之外**的固定条。
    // 放进容器内会有两个坏处——它会随内容滚走（「跳到本次启动」按钮只在滚到顶时可点），
    // 或者用 sticky 常驻却把顶部几行日志盖住。
    <div className="relative flex min-h-0 flex-1 flex-col">
      {history && (
        <div
          data-testid="console-backtrack-banner"
          className={cn('flex shrink-0 items-center gap-2 rounded-t-md border-b border-white/10 px-2 py-1 text-xs text-gray-400', immersive ? 'bg-[#171b22]' : 'bg-[#16161e]')}
        >
          {history.error ? (
            // 失败不能只在控制台里 console.error：给出可点的重试，否则用户只会看到「不再加载了」。
            <button
              type="button"
              onClick={onLoadEarlier}
              className="rounded px-1 text-amber-400 underline decoration-dotted hover:bg-white/10"
            >
              {t('instanceDetail.consoleBacktrackFailed')}
            </button>
          ) : history.loading ? (
            <span role="status">{t('instanceDetail.consoleBacktrackLoading')}</span>
          ) : history.exhausted ? (
            // spec §4.3：到最早必须明示，不能静默停住（否则用户以为「卡了」而反复上滚）。
            <span data-testid="console-backtrack-earliest">{t('instanceDetail.consoleBacktrackEarliest')}</span>
          ) : (
            <span>{t('instanceDetail.consoleBacktrackHint')}</span>
          )}
          {/* 数据来自哪层（spec §4.3）：内存缓冲 vs 数据库，两个数字分别标注。 */}
          <span className="truncate text-gray-500">
            {t('instanceDetail.consoleBacktrackLayers', {
              memory: history.memoryCount,
              database: history.rowCount,
            })}
            {history.oldestTime
              ? ` · ${t('instanceDetail.consoleBacktrackOldest', {
                  time: new Date(history.oldestTime).toLocaleString(),
                })}`
              : ''}
          </span>
          {historyActions && <span className="ml-auto flex shrink-0 items-center gap-1">{historyActions}</span>}
        </div>
      )}
      <div
        ref={containerRef}
        onScroll={handleScroll}
        onContextMenu={onContextMenu}
        onMouseDown={handleMouseDown}
        onKeyDown={handleKeyDown}
        // 只读区仍需可聚焦：`Ctrl+A` 全选缓冲得有个接收者，且不能抢命令栏里 `Ctrl+A`
        // 的「回到行首」语义（spec §2.3），故把按键限制在本容器内。
        tabIndex={0}
        data-testid="console-output"
        data-console-follow={follow ? 'true' : 'false'}
        data-console-selection={
          selectionRange
            ? `${seqRangeBounds(selectionRange).start}-${seqRangeBounds(selectionRange).end}`
            : undefined
        }
        className={cn(
          'overflow-auto font-mono leading-none outline-none selection:bg-sky-500/40',
          immersive ? 'bg-[#12161d] text-slate-200' : 'bg-[#1a1b26] text-[#a9b1d6]',
          // 有横幅时圆角只留下方，且高度由 flex 分配而非 h-full（否则会把横幅顶出容器）。
          history ? 'min-h-0 flex-1 rounded-b-md' : 'h-full rounded-md',
          // 仅拖拽进行中禁用原生选区：常驻禁用会连单行字符级选取一起废掉。
          anchorDragging && 'select-none',
        )}
        style={{ fontSize }}
      >
        {/* 回溯锚点（spec §2.1）：溢出丢弃过就必须明示，否则用户以为「日志本来就这么少」。 */}
        {droppedCount > 0 && (
          <div
            data-testid="console-earlier-anchor"
            className="border-b border-white/10 bg-white/5 px-2 py-1 text-xs text-gray-400"
          >
            {t('instanceDetail.consoleEarlierDropped', { count: droppedCount })}
          </div>
        )}
        {/* 「只看这段」横幅（FR-418）：临时过滤器必须显式可退出，否则用户会以为日志断流了。 */}
        {rangeFilter && (
          <div
            data-testid="console-range-filter"
            className="flex items-center gap-2 border-b border-white/10 bg-sky-500/10 px-2 py-1 text-xs text-sky-200"
          >
            <span>{t('instanceDetail.consoleRangeFilterActive', { count: scoped.length })}</span>
            <button
              type="button"
              onClick={() => setRangeFilter(null)}
              className="rounded bg-white/10 px-1.5 py-0.5 text-gray-200 hover:bg-white/20"
            >
              {t('instanceDetail.consoleRangeFilterExit')}
            </button>
          </div>
        )}
        {rows.length === 0 ? (
          <p className="p-2 text-xs text-gray-500">
            {filterActive ? t('instanceDetail.consoleFilteredEmpty') : t('instanceDetail.consoleEmpty')}
          </p>
        ) : (
          <div ref={rowsRef} data-console-rows>
            <div style={{ height: range.before }} />
            {visible.map((line, index) => (
              <ConsoleRow
                key={line.seq}
                line={line}
                height={heights ? (heights[range.start + index] ?? rowHeight) : rowHeight}
                wrap={wrap}
                matches={matches}
                currentMatch={currentMatch}
                block={blocks.get(line.seq)}
                collapsed={blocks.has(line.seq) ? !effectiveExpanded.has(line.seq) : undefined}
                selected={isLineSelected(selectionRange, line)}
                onToggleBlock={toggleBlock}
                onCopyBlock={copyBlock}
                onSelectBlock={selectBlock}
              />
            ))}
            <div style={{ height: range.after }} />
          </div>
        )}
      </div>

      {/* 选区工具条（FR-418，spec §3.1）：操作对象是 seq 区间，不是 DOM 选区。 */}
      {selectionRange && (
        <div
          data-testid="console-selection-toolbar"
          // flex-wrap + 宽度上限：窄 pane（分屏半宽/小窗）里五个动作不再把工具条顶出屏。
          className="absolute bottom-3 left-3 z-10 flex max-w-[calc(100%-1.5rem)] flex-wrap items-center gap-1 rounded-xl border border-white/10 bg-[#1f2030]/95 px-2 py-1 text-xs text-gray-200 shadow-lg"
        >
          <span className="px-1 text-gray-400">
            {t('instanceDetail.consoleSelectionCount', { count: selectedLines.length })}
          </span>
          <button type="button" onClick={copySelection} className="rounded px-1.5 py-0.5 hover:bg-white/10">
            {t('instanceDetail.consoleSelectionCopy')}
          </button>
          <button type="button" onClick={saveSelection} className="rounded px-1.5 py-0.5 hover:bg-white/10">
            {t('instanceDetail.consoleSelectionSave')}
          </button>
          <button type="button" onClick={onlyThisRange} className="rounded px-1.5 py-0.5 hover:bg-white/10">
            {t('instanceDetail.consoleSelectionOnly')}
          </button>
          <button
            type="button"
            onClick={() => selection.clear()}
            className="rounded px-1.5 py-0.5 text-gray-400 hover:bg-white/10 hover:text-gray-100"
          >
            {t('instanceDetail.consoleSelectionCancel')}
          </button>
        </div>
      )}

      {/* 上滚暂停时的返回浮标：带未读计数，点击即回到底部并恢复跟随。 */}
      {!follow && (
        <button
          type="button"
          onClick={jumpToBottom}
          className="absolute bottom-3 right-4 rounded-full bg-primary px-3 py-1 text-xs font-medium text-primary-foreground shadow-lg hover:opacity-90"
        >
          {unseen > 0
            ? t('instanceDetail.consoleJumpToBottomCount', { count: unseen })
            : t('instanceDetail.consoleJumpToBottom')}
        </button>
      )}
    </div>
  )
}
