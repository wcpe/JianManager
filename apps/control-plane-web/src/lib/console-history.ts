import { useCallback, useMemo, useRef, useState } from 'react'

import { fetchLogCursorPage } from '@/api/logs'

// 纯逻辑（历史分页常量、序号/行合并、启动行定位、类型）已迁至 `@jianmanager/ui`；
// 此处转出，调用点零改动。
export * from '@jianmanager/ui/lib/console-history'

import {
  HISTORY_JUMP_MAX_PAGES,
  HISTORY_PAGE_SIZE,
  historyRowsToLines,
  historySeqAt,
  STARTUP_KEYWORD,
  type ConsoleHistoryState,
  type HistoryJumpOutcome,
  type UseConsoleHistoryOptions,
} from '@jianmanager/ui/lib/console-history'
import type { LogEntry } from '@jianmanager/ui/lib/console-log-types'

/** 一次取页的结果，供 loadUntil 判断是否继续（只被下面的 hook 用）。 */
interface FetchPageResult {
  added: number
  exhausted: boolean
  ok: boolean
}

/**
 * 已加载历史 + 它所属的实例与回溯接缝（同上）。
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

/** 查询去重键（只被下面的 hook 用，故留在这里）。 */
function dataKey(instanceId: number, anchorTime: string | undefined): string {
  return `${instanceId}:${anchorTime ?? ''}`
}

/** 空数据占位（同上）。 */
const EMPTY_DATA = { rows: [], exhausted: false, error: null as string | null }
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
