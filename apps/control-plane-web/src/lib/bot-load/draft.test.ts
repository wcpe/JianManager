import { describe, expect, it } from 'vitest'
import {
  createDefaultDraft,
  isPlanTokenFresh,
  wizardReducer,
} from './draft'
import { COMMAND_ORCHESTRATION_V1 } from './presets'

describe('bot-load draft reducer', () => {
  it('默认草稿含 command-orchestration-v1 与严格阈值', () => {
    const d = createDefaultDraft()
    expect(d.commandSchedule.commands[0].id).toBe(COMMAND_ORCHESTRATION_V1.commands[0].id)
    expect(d.thresholds.minWorkerHealthRate).toBe(0.99)
    expect(d.planToken).toBeNull()
  })

  it('修改命令计划会使 planToken 失效', () => {
    let state = createDefaultDraft()
    state = wizardReducer(state, {
      type: 'setPreflight',
      planToken: 'tok-1',
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      runId: 9,
    })
    expect(state.planToken).toBe('tok-1')
    state = wizardReducer(state, {
      type: 'setCommandSchedule',
      schedule: { ...COMMAND_ORCHESTRATION_V1, durationMs: 120000 },
    })
    expect(state.planToken).toBeNull()
    expect(state.planExpiresAt).toBeNull()
  })

  it('patch instanceId 会使 plan 失效', () => {
    let state = createDefaultDraft()
    state = wizardReducer(state, {
      type: 'setPreflight',
      planToken: 'tok-2',
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
      runId: 1,
    })
    state = wizardReducer(state, { type: 'patch', patch: { instanceId: 3 } })
    expect(state.planToken).toBeNull()
    expect(state.instanceId).toBe(3)
  })

  it('isPlanTokenFresh 按过期时间判定', () => {
    const future = new Date(Date.now() + 30_000).toISOString()
    const past = new Date(Date.now() - 1000).toISOString()
    expect(isPlanTokenFresh(future)).toBe(true)
    expect(isPlanTokenFresh(past)).toBe(false)
    expect(isPlanTokenFresh(null)).toBe(false)
  })
})
