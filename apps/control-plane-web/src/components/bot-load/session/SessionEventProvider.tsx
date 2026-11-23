/**
 * 会话级 SSE Provider：单 run 一条连接，tab 切换不重建。
 */
/* eslint-disable react-refresh/only-export-components -- 同文件导出 Provider + useSessionEvents hook 是既定契约 */
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { botLoadQueryKeys, botLoadStreamUrl, ensureFreshToken } from '@/api/bot-load'
import { subscribeBotLoadRunStream } from '@/lib/bot-load/bot-load-session-event-client'
import type { BotLoadEventClientStatus } from '@/lib/bot-load/bot-load-session-event-client'
import { applyStreamFrame, createSessionLiveState } from '@/lib/bot-load/bot-load-session-store'
import type { SessionLiveState } from '@/lib/bot-load/bot-load-session-store'
import type { BotLoadRunV2 } from '@/lib/bot-load/bot-load-types'
import { isTerminalRunState } from '@/lib/bot-load/bot-load-types'

interface SessionEventContextValue {
  live: SessionLiveState
  streamStatus: BotLoadEventClientStatus | string
  /** 合并 HTTP 快照与 live 后的 run。 */
  run: BotLoadRunV2 | null
}

/**
 * `counts` / `run-state` 帧触发 detail 全量重取的最小间隔。
 *
 * 服务端推流 ticker 是 2s，逐帧重取就等于每 2 秒拉一次 HTTP 整表快照；这两类帧的数据
 * 已被 `applyStreamFrame` 合并进内存态，故 detail 只做低频兜底同步。
 */
const DETAIL_REFRESH_MIN_INTERVAL_MS = 5000

const SessionEventContext = createContext<SessionEventContextValue | null>(null)

export function useSessionEvents(): SessionEventContextValue {
  const ctx = useContext(SessionEventContext)
  if (!ctx) {
    throw new Error('useSessionEvents 必须在 SessionEventProvider 内使用')
  }
  return ctx
}

export function SessionEventProvider({
  runId,
  snapshot,
  children,
}: {
  runId: number | string
  snapshot: BotLoadRunV2 | null | undefined
  children: ReactNode
}) {
  const qc = useQueryClient()
  const [live, setLive] = useState<SessionLiveState>(() => createSessionLiveState(snapshot ?? null))
  const [streamStatus, setStreamStatus] = useState<BotLoadEventClientStatus | string>('idle')
  const terminalRef = useRef(false)
  /**
   * live 的最新值镜像（同一帧内同步可见）。
   *
   * `onEvent` 必须先算出新状态、再做副作用：React 要求 setState 的 updater 是**纯函数**
   * （StrictMode 开发态会重复执行它），把失效查询/标记终态写在 updater 里会被执行两遍。
   * 帧是顺序到达的，用 ref 承接上一帧结果即可；快照合并同样经 `commitLive` 写入，两个来源不会分叉。
   */
  const liveRef = useRef(live)
  const commitLive = useCallback((next: SessionLiveState) => {
    liveRef.current = next
    setLive(next)
  }, [])
  /** 最近一次 detail 全量重取的时间戳（节流用）。 */
  const lastDetailRefreshRef = useRef(0)

  // 快照刷新时合并（不覆盖 live metrics/warnings）。setState 收在 commitLive 里，
  // 与 liveRef 一起写，保证 ref 与 state 不会分叉。
  useEffect(() => {
    if (!snapshot) return
    commitLive({
      ...liveRef.current,
      run: mergeRun(liveRef.current.run, snapshot),
    })
    if (isTerminalRunState(snapshot.runState)) {
      terminalRef.current = true
    }
  }, [snapshot, commitLive])

  /** detail 重取节流（节流口径见模块顶部的 `DETAIL_REFRESH_MIN_INTERVAL_MS`）。 */
  const refreshDetailThrottled = useCallback(() => {
    const now = Date.now()
    if (now - lastDetailRefreshRef.current < DETAIL_REFRESH_MIN_INTERVAL_MS) return
    lastDetailRefreshRef.current = now
    void qc.invalidateQueries({ queryKey: botLoadQueryKeys(runId).detail })
  }, [qc, runId])

  const onEvent = useCallback(
    (frame: { event: string; data: string; id?: string }) => {
      // ① 纯计算：先把帧应用到状态并提交（不在 updater 里做任何副作用）。
      const { state, completed } = applyStreamFrame(liveRef.current, frame.event, frame.data, frame.id)
      commitLive(state)
      // ② 副作用：终态失效是「一次性且必须立刻生效」的，不受节流影响。
      if (completed) {
        terminalRef.current = true
        lastDetailRefreshRef.current = Date.now()
        void qc.invalidateQueries({ queryKey: botLoadQueryKeys(runId).detail })
        void qc.invalidateQueries({ queryKey: ['bots', 'stress-sessions', runId, 'failures'] })
      } else if (frame.event === 'counts' || frame.event === 'run-state') {
        refreshDetailThrottled()
      }
      if (frame.event === 'failure') {
        void qc.invalidateQueries({ queryKey: botLoadQueryKeys(runId).failures() })
      }
    },
    [qc, runId, commitLive, refreshDetailThrottled],
  )
  const snapshotRunState = snapshot?.runState

  useEffect(() => {
    // 终态不建 SSE。
    if (terminalRef.current || (snapshotRunState && isTerminalRunState(snapshotRunState))) {
      setStreamStatus('closed')
      return
    }

    const unsub = subscribeBotLoadRunStream({
      runId,
      url: botLoadStreamUrl(runId),
      getToken: ensureFreshToken,
      onEvent,
      onStatus: (status) => setStreamStatus(status),
    })
    return () => {
      unsub()
    }
  }, [runId, onEvent, snapshotRunState])

  const run = live.run ?? snapshot ?? null
  const value = useMemo(
    () => ({ live, streamStatus, run }),
    [live, streamStatus, run],
  )

  return <SessionEventContext.Provider value={value}>{children}</SessionEventContext.Provider>
}

function mergeRun(live: BotLoadRunV2 | null, snap: BotLoadRunV2): BotLoadRunV2 {
  if (!live) return snap
  // 以较新 updatedAt 优先；否则用 snapshot 补全冻结字段。
  const liveTs = Date.parse(live.updatedAt || '') || 0
  const snapTs = Date.parse(snap.updatedAt || '') || 0
  if (snapTs >= liveTs && isTerminalRunState(snap.runState)) return snap
  return {
    ...snap,
    ...live,
    // 冻结配置始终以快照为准。
    loadProfile: snap.loadProfile,
    thresholds: snap.thresholds,
    commandSchedule: snap.commandSchedule,
    scenario: snap.scenario,
    orchestrationYaml: snap.orchestrationYaml,
    allocations: snap.allocations,
    config: snap.config,
    name: snap.name,
    uuid: snap.uuid,
    id: snap.id,
  }
}
