import { useCallback, useMemo, useRef, useState } from 'react'

import api from '@/api/client'
import { LOGS_FEDERATION_PATH } from '@/api/logFederation'
import { type LogEntry } from '@/api/logs'

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
import type { LogEntry as _LogEntryFromPkg } from '@jianmanager/ui/lib/console-log-types'

/**
 * 历史上溯数据源适配（2026-10-29）。
 *
 * 背景：CP `logs` 表与其游标接口 `/logs` 已删除，实例日志的权威副本在 Worker 的 VL 里。
 * 本函数把联邦查询（`GET /logs/federation`）包装回历史回溯需要的「游标页」形状，
 * 使 `fetchPage` / `findStartupTime` 两处调用点只需换数据源、不必改翻页逻辑。
 *
 * 语义对应：
 *   - 原 `to`（冻结接缝）→ 联邦 `to`，仍是「只取接缝之前」，保证已加载历史连续无洞；
 *   - 原 `cursor`（上一页 nextCursor）→ 联邦 `nextCursor`，同样是“更早一页”的边界；
 *   - 联邦返回按时间倒序（最新在前），与原游标接口一致，调用侧的反转逻辑不变。
 *
 * 行字段映射：联邦事件是扁平结构（`_msg`/`level`/`stream`），这里转回 `LogEntry`，
 * 使下游解析器（`historyRowsToLines`）不必区分数据源。
 */
async function fetchHistoryCursorPage(params: {
  instanceId: number
  to?: string
  keyword?: string
  cursor?: string
  limit: number
}, signal?: AbortSignal): Promise<{ items: LogEntry[]; nextCursor: string | null }> {
  const query: Record<string, unknown> = {
    instanceId: params.instanceId,
    limit: params.limit,
  }
  if (params.to) query.to = params.to
  if (params.keyword) query.keyword = params.keyword
  if (params.cursor) query.cursor = params.cursor

  const { data } = await api.get<{ items?: unknown[]; next_cursor?: string | null; nextCursor?: string | null }>(LOGS_FEDERATION_PATH, {
    params: query,
    signal,
    // 联邦查询是重活（多目标逐目标覆盖解析，实测 6–13s），给与 LogsPage 同口径的 60s 预算。
    timeout: 60_000,
  })

  const items = Array.isArray(data?.items) ? data.items : []
  // 后端信封体是 **snake_case**（`next_cursor`，见 router/log_federation.go）。
  //
  // 2026-10-29 修：这里曾只读 `nextCursor`，于是**永远读到 undefined ⇒ 游标永为 null**，
  // 控制台「向上滚动加载更早日志」只能翻到第一页就停，且不报错——静默断头。
  // 两种写法都接受，但以真实 wire 的 snake_case 为准。
  const rawCursor = data?.next_cursor ?? data?.nextCursor
  return {
    items: items.map((raw, index) => {
      const o = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>
      return {
        id: typeof o.id === 'number' ? o.id : -(index + 1),
        source: 'instance',
        level: typeof o.level === 'string' ? o.level.toLowerCase() : 'info',
        instanceId: params.instanceId,
        instanceUuid: typeof o.instance_uuid === 'string' ? o.instance_uuid : '',
        nodeId: typeof o.node_id === 'number' ? o.node_id : 0,
        stream: typeof o.stream === 'string' ? o.stream : undefined,
        message: typeof o.message === 'string' ? o.message : typeof o._msg === 'string' ? o._msg : '',
        time: typeof o.event_time_utc === 'string'
          ? o.event_time_utc
          : typeof o.time === 'string'
            ? o.time
            : '',
      } satisfies LogEntry
    }),
    nextCursor: typeof rawCursor === 'string' ? rawCursor : null,
  }
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
      const page = await fetchHistoryCursorPage({
        instanceId: frozen.instanceId,
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
      const page = await fetchHistoryCursorPage({
        instanceId: frozen.instanceId,
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
