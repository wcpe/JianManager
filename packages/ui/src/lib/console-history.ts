
import { createLogLineParser, type LogLine, type LogStream } from './console-log-line'
import type { LogEntry } from './console-log-types'

/**
 * 控制台历史日志回溯（FR-419，spec §4.3）。
 *
 * 数据源分层（spec §4.1）：内存环形缓冲覆盖最新输出，数据库活日志覆盖更早，归档 NDJSON
 * 本批不做。未溢出时接缝是会话开始；溢出后接缝推进到最早保留行的浏览器接收时刻，故被挤掉
 * 的会话内日志也会被数据库回溯补上。DB/WS 的极短写入时差由 mergeHistoryAndLive 去重。
 *
 * **接缝冻结**（spec §4.3 修订）：接缝只在实例切换时重置；同一实例内接缝随缓冲溢出
 * 前移不重置——DB 行不会消失，重置只会把已加载历史整体作废重拉。
 */

/** 单页行数。比页码分页默认的 50 大：回溯是「一屏一屏往上翻」，页太小会把滚动切碎成一串请求。 */
export const HISTORY_PAGE_SIZE = 200

/**
 * 一次「跳到时间点」最多连续回溯多少页。
 *
 * 跳转实现为「一直往更早翻直到覆盖目标时刻」，以保证已加载历史始终是**连续无洞**的一段
 * （挖洞式跳转会让用户以为中间那段日志不存在）。代价是目标很久远时请求数线性增长，
 * 故设上限，到顶就明确告知而不是无声地一直打后端。
 */
export const HISTORY_JUMP_MAX_PAGES = 30

/**
 * 启动完成标记（spec §4.3「跳到本次启动」）。
 *
 * 只认 MC/Paper 服务端启动完成那一行的稳定特征 `Done (12.345s)!`；
 * 宁可找不到也不误判——把随便一行认成「本次启动」比找不到更糟。
 */
const STARTUP_DONE = /\bDone\s*\([^)]*\)!/

/**
 * 去库里定位启动行时用的 LIKE 关键字。
 *
 * DB 侧只有 `message LIKE %kw%`（无正则），故用标记的稳定前缀粗筛，
 * 拿到时间后仍由 {@link findStartupSeq} 的正则做精判——粗筛只用来决定「往前翻到哪」。
 */
export const STARTUP_KEYWORD = 'Done ('

/**
 * 历史行的 seq 一律为**负数**：`-1` 是最新的历史行，越早越负。
 *
 * 环形缓冲的 seq 从 0 起单调递增，负号使两段天然不冲突且排序即时间序
 * （选区与定位一律用 seq，见 spec §1.1）。增量 prepend 时旧行 seq 不变：
 * 新页插在更负的一侧，`-1` 永远是「紧邻内存缓冲边界的那一行」。
 */
export function historySeqAt(index: number, total: number): number {
  return index - total
}

/**
 * DB 行 → 渲染行。
 *
 * 走与实时输出**同一个解析器**（spec §1.2）：同一条日志无论来自 WS 还是数据库，拆出的
 * 时间/级别/来源与堆栈归属都必须一致，否则用户滚过接缝会看到同格式的行长得不一样。
 * 级别：优先解析行内（MC 前缀 / 结构化 level=）；都没有时用 DB 的 stream 列兜底
 * （stdout→INFO、stderr→ERROR），与 log_ingest 的推断同源。
 *
 * 必须整段重解析而非只解析新页：堆栈帧的 `stackOf` 依赖前一行，新加载的更早页里可能正是
 * 某个异常头，只解析新页会让接缝处的堆栈挂错头。
 */
export function historyRowsToLines(rows: readonly LogEntry[]): LogLine[] {
  const parser = createLogLineParser()
  return rows.map((row, index) =>
    parser.parse(row.message, {
      seq: historySeqAt(index, rows.length),
      stream: (row.stream === 'stderr' ? 'stderr' : 'stdout') as LogStream,
    }),
  )
}

/**
 * 合并数据库历史与内存缓冲。
 *
 * 缓冲溢出后，回溯接缝取浏览器收到的最早保留行，而 DB 的写入时间可能早几个毫秒；
 * 因此最后几条 DB 行可能与内存首段重复。只消掉**至少两条连续原文**的重叠：单条高频
 * 文案（如 Can't keep up）可能恰好重复，宁可留一个重复也不能把真实日志误删。
 */
export function mergeHistoryAndLive(history: readonly LogLine[], live: readonly LogLine[]): readonly LogLine[] {
  const comparableLive = live.filter((line) => line.kind !== 'command' && line.kind !== 'system')
  const maxOverlap = Math.min(history.length, comparableLive.length)
  for (let size = maxOverlap; size >= 2; size--) {
    const historyTail = history.slice(-size)
    const liveHead = comparableLive.slice(0, size)
    if (historyTail.every((line, index) => line.raw === liveHead[index].raw)) {
      return [...history.slice(0, -size), ...live]
    }
  }
  return history.length > 0 ? [...history, ...live] : live
}

/** 在给定行集中找最近一次启动完成行的 seq；找不到返回 null。 */
export function findStartupSeq(lines: readonly LogLine[]): number | null {
  for (let i = lines.length - 1; i >= 0; i--) {
    if (STARTUP_DONE.test(lines[i].raw)) return lines[i].seq
  }
  return null
}

/** 「跳到时间点」的结果：到达 / 已回溯到最早仍未覆盖 / 撞单次页数上限 / 请求失败。 */
export type HistoryJumpOutcome = 'reached' | 'exhausted' | 'capped' | 'failed'

export interface UseConsoleHistoryOptions {
  instanceId: number
  /**
   * 内存缓冲历史接缝 ISO；会话尚未建立时可为 undefined，首次请求前兜底为「现在」。
   *
   * **冻结语义**：本 hook 只在实例切换时跟随该 prop 更新内部接缝，同一实例内
   * 接缝随缓冲溢出前移**不会**作废已加载历史——DB 行不会消失，本就无需重置；
   * 若每次前移都换数据 key，用户翻着历史时日志一涌、已加载的页会整体蒸发。
   */
  anchorTime?: string
}

export interface ConsoleHistoryState {
  /** 已加载的历史行（时间正序，seq 为负）。 */
  lines: readonly LogLine[]
  /** 已回溯的行数。 */
  rowCount: number
  /** 已加载历史中最早一行的时间（ISO），供横幅标明「回溯到哪儿了」。 */
  oldestTime?: string
  loading: boolean
  /** 已到数据库最早一条（后端 nextCursor 为 null）。 */
  exhausted: boolean
  error: string | null
  /** 取更早一页；正在加载 / 已到最早时空转。 */
  loadEarlier: () => void
  /** 一直回溯到覆盖目标时刻（或到最早 / 撞页数上限 / 失败）。 */
  loadUntil: (targetIso: string) => Promise<HistoryJumpOutcome>
  /**
   * 去库里问「最近一次启动完成行在什么时候」（spec §4.3：缓冲内找不到则按时间条件查库）。
   * 返回 null 表示库里也没有。
   */
  findStartupTime: () => Promise<string | null>
  /**
   * 已加载历史里**第一条不早于**目标时刻的行的 seq；目标晚于全部历史则返回 null
   * （落点在内存缓冲里，由调用方决定怎么定位）。
   *
   * 放在控制器里而不是让调用方自己算：绝对时间只在 DB 行上有（渲染行模型里只有 `HH:MM:SS`
   * 甚至没有），且必须读**刚加载完那一刻**的行——调用方闭包里的 lines 要等 React 提交才更新。
   */
  seqAtTime: (targetIso: string) => number | null
}

