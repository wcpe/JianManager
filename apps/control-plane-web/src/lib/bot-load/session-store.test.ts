import { describe, it, expect } from 'vitest'
import { applyStreamFrame, createSessionLiveState, sumCommandCounts } from './session-store'
import type { BotLoadRunV2 } from './types'

function sampleRun(partial?: Partial<BotLoadRunV2>): BotLoadRunV2 {
  return {
    schemaVersion: 2,
    id: 100,
    uuid: 'u',
    instanceId: 1,
    name: 'demo',
    namePrefix: 'load',
    count: 100,
    behavior: 'command',
    config: {},
    status: 'running',
    counts: { total: 100, byStatus: { connected: 90 } },
    allocations: [],
    batches: [],
    createdAt: '2026-06-28T00:00:00Z',
    updatedAt: '2026-06-28T00:00:00Z',
    targetBots: 100,
    runState: 'running',
    verdict: 'pending',
    verdictReasons: [],
    currentStage: 0,
    loadProfile: { type: 'stable', targetBots: 100, rampUpSeconds: 30, durationSeconds: 300 },
    thresholds: {
      minOnlineRate: 0.99,
      minCommandSentRate: 0.99,
      minScheduleCompletionRate: 0.99,
      minWorkerHealthRate: 0.99,
      minBarrierArrivalRate: 0.99,
      maxScheduleLagP95Ms: 1000,
      maxProcessCrashes: 0,
    },
    loadCounts: {
      planned: 100,
      accepted: 98,
      connecting: 5,
      connected: 90,
      disconnected: 0,
      failed: 3,
      stopped: 0,
    },
    commandCounts: { cmd_a: { planned: 10, sent: 5, failed: 1, timedOut: 0, cancelled: 0 } },
    barrier: { waiting: 0, arrived: 0, released: 0, timedOut: 0 },
    maxStableBots: 0,
    failureSummary: {},
    ...partial,
  }
}

describe('applyStreamFrame', () => {
  it('counts 更新 loadCounts 与 commandCounts', () => {
    const state = createSessionLiveState(sampleRun())
    const { state: next } = applyStreamFrame(
      state,
      'counts',
      JSON.stringify({
        counts: { planned: 100, accepted: 99, connecting: 1, connected: 95, disconnected: 0, failed: 2, stopped: 0 },
        commandCounts: { cmd_a: { planned: 10, sent: 8, failed: 1, timedOut: 0, cancelled: 0 } },
        barrier: { waiting: 0, arrived: 0, released: 0, timedOut: 0 },
        timestamp: '2026-06-28T00:01:00Z',
      }),
      '1',
    )
    expect(next.run?.loadCounts.connected).toBe(95)
    expect(next.run?.commandCounts.cmd_a.sent).toBe(8)
    expect(next.lastEventId).toBe('1')
  })

  it('history 按 eventId 去重', () => {
    let state = createSessionLiveState(sampleRun())
    const evt = {
      eventId: 'e1',
      runId: 100,
      runUuid: 'u',
      timestamp: '2026-06-28T00:00:01Z',
      type: 'run-state',
      payload: { runState: 'running' },
    }
    state = applyStreamFrame(state, 'history', JSON.stringify(evt), 'h1').state
    state = applyStreamFrame(state, 'history', JSON.stringify(evt), 'h2').state
    expect(state.historyHead).toHaveLength(1)
  })

  it('complete 标记 reportReady', () => {
    const state = createSessionLiveState(sampleRun())
    const { state: next, completed } = applyStreamFrame(
      state,
      'complete',
      JSON.stringify({
        runState: 'completed',
        verdict: 'passed',
        reportReady: true,
        timestamp: '2026-06-28T01:00:00Z',
      }),
    )
    expect(completed).toBe(true)
    expect(next.reportReady).toBe(true)
    expect(next.run?.runState).toBe('completed')
  })

  it('warning 同 code 合并', () => {
    let state = createSessionLiveState(sampleRun())
    state = applyStreamFrame(
      state,
      'warning',
      JSON.stringify({ code: 'CLOCK_SKEW', message: 'a', timestamp: 't1' }),
    ).state
    state = applyStreamFrame(
      state,
      'warning',
      JSON.stringify({ code: 'CLOCK_SKEW', message: 'b', timestamp: 't2' }),
    ).state
    expect(state.warnings).toHaveLength(1)
    expect(state.warnings[0]!.message).toBe('b')
  })
})

describe('sumCommandCounts', () => {
  it('汇总各命令计数', () => {
    const s = sumCommandCounts({
      a: { planned: 10, sent: 8, failed: 1, timedOut: 1, cancelled: 0 },
      b: { planned: 5, sent: 5, failed: 0, timedOut: 0, cancelled: 0 },
    })
    expect(s).toEqual({ planned: 15, sent: 13, failed: 1, timedOut: 1, cancelled: 0 })
  })
})
