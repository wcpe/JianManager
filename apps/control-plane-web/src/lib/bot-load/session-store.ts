/**
 * 会话页内存快照（非 Zustand 持久化）：由 SSE 聚合事件增量更新。
 */
import type {
  BotLoadBarrierCounts,
  BotLoadCommandCounts,
  BotLoadLoadCounts,
  BotLoadMetricPoint,
  BotLoadRunEvent,
  BotLoadRunState,
  BotLoadRunV2,
  BotLoadStreamWarning,
  BotLoadVerdict,
  BotLoadVerdictReason,
} from './types'
import { appendMetricPoints } from './metrics'

export interface SessionLiveState {
  run: BotLoadRunV2 | null
  liveMetrics: BotLoadMetricPoint[]
  warnings: BotLoadStreamWarning[]
  historyHead: BotLoadRunEvent[]
  reportReady: boolean
  streamStatus: string
  lastEventId: string | null
}

export function createSessionLiveState(run?: BotLoadRunV2 | null): SessionLiveState {
  return {
    run: run ?? null,
    liveMetrics: [],
    warnings: [],
    historyHead: [],
    reportReady: false,
    streamStatus: 'idle',
    lastEventId: null,
  }
}

function parseJson<T>(raw: string): T | null {
  try {
    return JSON.parse(raw) as T
  } catch {
    return null
  }
}

/** 应用 SSE 帧到内存状态；返回是否 complete。 */
export function applyStreamFrame(
  state: SessionLiveState,
  event: string,
  data: string,
  eventId?: string,
): { state: SessionLiveState; completed: boolean } {
  const next: SessionLiveState = {
    ...state,
    lastEventId: eventId ?? state.lastEventId,
  }
  const body = parseJson<Record<string, unknown>>(data)

  switch (event) {
    case 'init': {
      if (!body) return { state: next, completed: false }
      const run = body.run as BotLoadRunV2 | undefined
      if (run) next.run = run
      if (typeof body.lastEventId === 'string') next.lastEventId = body.lastEventId
      return { state: next, completed: false }
    }
    case 'run-state': {
      if (!body || !next.run) return { state: next, completed: false }
      next.run = {
        ...next.run,
        runState: (body.runState as BotLoadRunState) ?? next.run.runState,
        verdict: (body.verdict as BotLoadVerdict) ?? next.run.verdict,
        verdictReasons: (body.verdictReasons as BotLoadVerdictReason[]) ?? next.run.verdictReasons,
        currentStage: typeof body.currentStage === 'number' ? body.currentStage : next.run.currentStage,
        status: mapRunStateToLegacyStatus(String(body.runState ?? next.run.runState)),
        updatedAt: typeof body.timestamp === 'string' ? body.timestamp : next.run.updatedAt,
      }
      return { state: next, completed: false }
    }
    case 'counts': {
      if (!body || !next.run) return { state: next, completed: false }
      const loadCounts = (body.counts as BotLoadLoadCounts) ?? next.run.loadCounts
      const commandCounts =
        (body.commandCounts as Record<string, BotLoadCommandCounts>) ?? next.run.commandCounts
      const barrier = (body.barrier as BotLoadBarrierCounts) ?? next.run.barrier
      next.run = {
        ...next.run,
        loadCounts,
        commandCounts,
        barrier,
        counts: {
          total: loadCounts.planned ?? next.run.counts.total,
          byStatus: {
            connected: loadCounts.connected,
            connecting: loadCounts.connecting,
            failed: loadCounts.failed,
            stopped: loadCounts.stopped,
            disconnected: loadCounts.disconnected,
          },
        },
        updatedAt: typeof body.timestamp === 'string' ? body.timestamp : next.run.updatedAt,
      }
      return { state: next, completed: false }
    }
    case 'stage': {
      if (!body || !next.run) return { state: next, completed: false }
      next.run = {
        ...next.run,
        currentStage: typeof body.stageIndex === 'number' ? body.stageIndex : next.run.currentStage,
      }
      return { state: next, completed: false }
    }
    case 'metric': {
      if (!body) return { state: next, completed: false }
      const point = body as unknown as BotLoadMetricPoint
      if (point.timestamp) {
        next.liveMetrics = appendMetricPoints(state.liveMetrics, point)
      }
      return { state: next, completed: false }
    }
    case 'command': {
      if (!body || !next.run) return { state: next, completed: false }
      const stepId = String(body.stepId ?? body.commandId ?? 'cmd')
      const prev = next.run.commandCounts[stepId] ?? {
        planned: 0,
        sent: 0,
        failed: 0,
        timedOut: 0,
        cancelled: 0,
      }
      next.run = {
        ...next.run,
        commandCounts: {
          ...next.run.commandCounts,
          [stepId]: {
            planned: typeof body.planned === 'number' ? body.planned : prev.planned,
            sent: typeof body.sent === 'number' ? body.sent : prev.sent,
            failed: typeof body.failed === 'number' ? body.failed : prev.failed,
            timedOut: typeof body.timedOut === 'number' ? body.timedOut : prev.timedOut,
            cancelled: typeof body.cancelled === 'number' ? body.cancelled : prev.cancelled,
          },
        },
      }
      return { state: next, completed: false }
    }
    case 'failure': {
      if (!body || !next.run) return { state: next, completed: false }
      const category = String(body.category ?? 'internal')
      const delta = typeof body.delta === 'number' ? body.delta : 1
      next.run = {
        ...next.run,
        failureSummary: {
          ...next.run.failureSummary,
          [category]: (next.run.failureSummary[category] ?? 0) + delta,
        },
      }
      return { state: next, completed: false }
    }
    case 'warning': {
      if (!body) return { state: next, completed: false }
      const w: BotLoadStreamWarning = {
        code: String(body.code ?? 'UNKNOWN'),
        message: String(body.message ?? ''),
        timestamp: String(body.timestamp ?? new Date().toISOString()),
      }
      // 同 code 合并：保留最新一条。
      const filtered = state.warnings.filter((x) => x.code !== w.code)
      next.warnings = [w, ...filtered].slice(0, 20)
      return { state: next, completed: false }
    }
    case 'history': {
      if (!body) return { state: next, completed: false }
      const evt = body as unknown as BotLoadRunEvent
      if (!evt.eventId) return { state: next, completed: false }
      if (state.historyHead.some((h) => h.eventId === evt.eventId)) {
        return { state: next, completed: false }
      }
      next.historyHead = [evt, ...state.historyHead].slice(0, 100)
      return { state: next, completed: false }
    }
    case 'complete': {
      if (body && next.run) {
        next.run = {
          ...next.run,
          runState: (body.verdict === 'aborted' ? 'cancelled' : next.run.runState) as BotLoadRunState,
          verdict: (body.verdict as BotLoadVerdict) ?? next.run.verdict,
          verdictReasons: (body.verdictReasons as BotLoadVerdictReason[]) ?? next.run.verdictReasons,
          status: mapRunStateToLegacyStatus(
            body.verdict === 'aborted'
              ? 'cancelled'
              : body.verdict === 'failed'
                ? 'failed'
                : 'completed',
          ),
        }
        // complete 时后端给出终态 runState 优先。
        if (typeof body.runState === 'string') {
          next.run = {
            ...next.run,
            runState: body.runState as BotLoadRunState,
            status: mapRunStateToLegacyStatus(String(body.runState)),
          }
        }
      }
      next.reportReady = body?.reportReady === true || body?.reportReady === undefined
      return { state: next, completed: true }
    }
    default:
      return { state: next, completed: false }
  }
}

function mapRunStateToLegacyStatus(runState: string): string {
  switch (runState) {
    case 'pending':
    case 'preflighting':
    case 'ready':
    case 'starting':
      return 'pending'
    case 'running':
    case 'degraded':
    case 'stopping':
    case 'cancelling':
      return 'running'
    case 'completed':
    case 'cancelled':
      return 'stopped'
    case 'failed':
      return 'error'
    default:
      return runState
  }
}

export function sumCommandCounts(
  commandCounts: Record<string, BotLoadCommandCounts> | undefined,
): BotLoadCommandCounts {
  const acc: BotLoadCommandCounts = { planned: 0, sent: 0, failed: 0, timedOut: 0, cancelled: 0 }
  if (!commandCounts) return acc
  for (const c of Object.values(commandCounts)) {
    acc.planned += c.planned
    acc.sent += c.sent
    acc.failed += c.failed
    acc.timedOut += c.timedOut
    acc.cancelled += c.cancelled
  }
  return acc
}
