import { useCallback, useMemo, useRef, useState } from 'react'

import { fetchLogCursorPage, type LogEntry } from '@/api/logs'
import { createLogLineParser, type LogLine, type LogStream } from './console-log-line'

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
 * 级别兜底用 DB 的 `stream` 列（stdout→INFO、stderr→ERROR），与 log_ingest 的推断同源。
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

/** 一次取页的结果，供 loadUntil 判断是否继续。 */
interface FetchPageResult {
  added: number
  exhausted: boolean
  ok: boolean
}

/**
 * 已加载历史 + 它所属的实例与回溯接缝。
 *
 * `key` 与状态绑在一起，使「实例或接缝变了 → 已加载的行作废」可以**派生**出来，
 * 不必在渲染期改 ref、也不必用 effect 事后重置（两者都会开出一段旧游标仍生效的窗口）。
 */
interface HistoryData {
  key: string
  rows: readonly LogEntry[]
  exhausted: boolean
  error: string | null
}

const EMPTY_DATA = { rows: [] as readonly LogEntry[], exhausted: false, error: null as string | null }

function dataKey(instanceId: number, anchorTime: string | undefined): string {
  return `${instanceId}|${anchorTime ?? ''}`
}

/**
 * 历史回溯控制器。
 *
 * 串行保证：`cursor` 是「翻到哪了」的唯一状态，所有取页都经内部 fetchPage 走同一把在途锁。
 * 并发触发（滚动抖动连发、跳转循环与滚动触发撞上）只产生一个真请求，不会出现两个请求
 * 拿同一个 cursor 而把同一页 prepend 两次。
 */
export function useConsoleHistory({ instanceId, anchorTime }: UseConsoleHistoryOptions): ConsoleHistoryState {
  // **接缝冻结**：内部接缝（frozen.anchorTime）只在实例切换时跟随 prop 更新，
  // 同一实例内接缝随缓冲溢出前移不变化——若直接用 prop 做 key，每丢一行前移一次
  // 就把已加载历史整体作废重拉（用户翻着历史时日志一涌、加载的页全部蒸发）。
  //
  // 重置用 React 官方的「渲染期调整 state」模式（条件式 setState during render）：
  // 不用 effect，避免「新实例已提交、effect 未跑」的窗口里拿着旧接缝发请求；
  // 同实例内 anchorTime 前移不进入条件，被有意忽略（冻结语义）。
  const [frozen, setFrozen] = useState(() => ({ instanceId, anchorTime }))
  const [frozenFor, setFrozenFor] = useState(instanceId)
  if (frozenFor !== instanceId) {
    setFrozenFor(instanceId)
    setFrozen({ instanceId, anchorTime })
  }

  // 已加载历史与其所属实例、接缝**打包成一份状态**。
  //
  // 把 key 放进状态里，是为了让「实例或接缝变了 → 已加载的行作废」成为**派生结论**而不是一次写操作：
  // 上面的渲染期 setState 只调整冻结接缝本身，**不直接清空历史数据**——若在渲染期比对并重置
  // 数据，就得在渲染里改 ref / setState 清空 rows；若用 effect 重置，则在提交前的
  // 那段窗口里 loadEarlier 会拿着旧游标发请求，新接缝的页被 prepend 到旧实例的行前面，
  // 屏上出现一段混合结果。派生方式下，清空发生在下一次取页时（见 fetchPage 的 base）。
  const [state, setState] = useState<HistoryData>(() => ({
    key: dataKey(frozen.instanceId, frozen.anchorTime),
    ...EMPTY_DATA,
  }))
  const [loading, setLoading] = useState(false)

  const key = dataKey(frozen.instanceId, frozen.anchorTime)
  // 实例或接缝不匹配即视为「什么都还没加载」：真正的清空发生在下一次取页时（见 fetchPage 的 base）。
  const data = state.key === key ? state : { key, ...EMPTY_DATA }

  // 游标 / 在途标志 / 状态镜像放 ref：它们决定「下一次请求打哪」，必须在同一 tick 内立即可见。
  // 等 React 提交才生效的话，连发两次滚动事件会用同一个游标各取一页（重复行），
  // 而 loadUntil 的多页循环也会因读到旧 rows 而永远判定「还没覆盖目标」。
  // 这些 ref 只在回调里读写，绝不在渲染期碰。
  const cursorRef = useRef<string | undefined>(undefined)
  const inFlightRef = useRef(false)
  const dataRef = useRef<HistoryData>(state)

  const fetchPage = useCallback(async (): Promise<FetchPageResult> => {
    // 实例或接缝变了：游标与已加载行一并作废，从最新一页重新开始。
    const base = dataRef.current.key === key ? dataRef.current : { key, ...EMPTY_DATA }
    if (base !== dataRef.current) cursorRef.current = undefined
    if (inFlightRef.current || base.exhausted) {
      return { added: 0, exhausted: base.exhausted, ok: true }
    }
    inFlightRef.current = true
    setLoading(true)
    const commit = (next: HistoryData) => {
      dataRef.current = next
      setState(next)
    }
    commit({ ...base, error: null })
    try {
      const page = await fetchLogCursorPage({
        instanceId: frozen.instanceId,
        source: 'instance',
        // to 是数据层接缝：只取内存缓冲最早保留行之前的日志（spec §4.1）。
        // 冻结接缝：多次取页共用同一上界，同实例内缓冲溢出前移不改变它。
        to: frozen.anchorTime ?? new Date().toISOString(),
        cursor: cursorRef.current,
        limit: HISTORY_PAGE_SIZE,
      })
      cursorRef.current = page.nextCursor ?? undefined
      const done = page.nextCursor === null
      // 后端按 time DESC 返回（最新在前）；本地统一按时间正序存放，与输出区渲染顺序一致。
      const older = [...page.items].reverse()
      commit({ key, rows: older.length > 0 ? [...older, ...base.rows] : base.rows, exhausted: done, error: null })
      return { added: older.length, exhausted: done, ok: true }
    } catch (err) {
      commit({ ...dataRef.current, error: err instanceof Error ? err.message : String(err) })
      return { added: 0, exhausted: false, ok: false }
    } finally {
      inFlightRef.current = false
      setLoading(false)
    }
  }, [frozen, key])

  const loadEarlier = useCallback(() => {
    void fetchPage()
  }, [fetchPage])

  const loadUntil = useCallback(
    async (targetIso: string): Promise<HistoryJumpOutcome> => {
      const target = Date.parse(targetIso)
      // 用毫秒数而非 ISO 字符串比较：DB 返回的时间可能带非 Z 时区偏移，字符串序不等于时间序。
      const covered = () => {
        const oldest = dataRef.current.key === key ? dataRef.current.rows[0] : undefined
        return oldest !== undefined && Date.parse(oldest.time) <= target
      }
      for (let page = 0; page < HISTORY_JUMP_MAX_PAGES; page++) {
        if (covered()) return 'reached'
        const result = await fetchPage()
        if (!result.ok) return 'failed'
        if (result.exhausted || result.added === 0) return covered() ? 'reached' : 'exhausted'
      }
      return covered() ? 'reached' : 'capped'
    },
    [fetchPage, key],
  )

  const findStartupTime = useCallback(async (): Promise<string | null> => {
    try {
      // limit=1 + 关键字：只要「最近一条」的时间，不拉整段——定位用不到内容。
      // 不带 to 上界：启动行可能位于会话起点之后（缓冲丢过行的那一段），先拿到时间再说。
      const page = await fetchLogCursorPage({
        instanceId: frozen.instanceId,
        source: 'instance',
        keyword: STARTUP_KEYWORD,
        limit: 1,
      })
      return page.items[0]?.time ?? null
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      const next = { ...dataRef.current, error: message }
      dataRef.current = next
      setState(next)
      return null
    }
  }, [frozen.instanceId])

  const seqAtTime = useCallback(
    (targetIso: string): number | null => {
      const target = Date.parse(targetIso)
      const rows = dataRef.current.key === key ? dataRef.current.rows : []
      const index = rows.findIndex((row) => Date.parse(row.time) >= target)
      return index < 0 ? null : historySeqAt(index, rows.length)
    },
    [key],
  )

  const lines = useMemo(() => historyRowsToLines(data.rows), [data.rows])

  return {
    lines,
    rowCount: data.rows.length,
    oldestTime: data.rows[0]?.time,
    loading,
    exhausted: data.exhausted,
    error: data.error,
    loadEarlier,
    loadUntil,
    findStartupTime,
    seqAtTime,
  }
}
