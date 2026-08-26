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
import {
  subscribeBotLoadRunStream,
  type BotLoadEventClientStatus,
} from '@/lib/bot-load/session-event-client'
import {
  applyStreamFrame,
  createSessionLiveState,
  type SessionLiveState,
} from '@/lib/bot-load/session-store'
import type { BotLoadRunV2 } from '@/lib/bot-load/types'
import { isTerminalRunState } from '@/lib/bot-load/types'

interface SessionEventContextValue {
  live: SessionLiveState
  streamStatus: BotLoadEventClientStatus | string
  /** 合并 HTTP 快照与 live 后的 run。 */
  run: BotLoadRunV2 | null
}

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

  // 快照刷新时合并（不覆盖 live metrics/warnings）。
  useEffect(() => {
    if (!snapshot) return
    /* eslint-disable react-hooks/set-state-in-effect -- HTTP 快照到达时合并 live.run，属外部系统同步 */
    setLive((prev) => ({
      ...prev,
      run: mergeRun(prev.run, snapshot),
    }))
    if (isTerminalRunState(snapshot.runState)) {
      terminalRef.current = true
    }
    /* eslint-enable react-hooks/set-state-in-effect */
  }, [snapshot])

  const onEvent = useCallback(
    (frame: { event: string; data: string; id?: string }) => {
      setLive((prev) => {
        const { state, completed } = applyStreamFrame(prev, frame.event, frame.data, frame.id)
        if (completed) {
          terminalRef.current = true
          void qc.invalidateQueries({ queryKey: botLoadQueryKeys(runId).detail })
          void qc.invalidateQueries({ queryKey: ['bots', 'stress-sessions', runId, 'failures'] })
        }
        if (frame.event === 'failure') {
          void qc.invalidateQueries({ queryKey: botLoadQueryKeys(runId).failures() })
        }
        if (frame.event === 'counts' || frame.event === 'run-state') {
          void qc.invalidateQueries({ queryKey: botLoadQueryKeys(runId).detail })
        }
        return state
      })
    },
    [qc, runId],
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
