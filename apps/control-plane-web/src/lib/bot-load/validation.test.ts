import { describe, expect, it } from 'vitest'
import {
  previewBotNames,
  validateCommandSchedule,
  validateConnection,
  validateCountMatchesProfile,
  validateLoadProfile,
  validateThresholds,
} from './validation'
import { COMMAND_ORCHESTRATION_V1, DEFAULT_STABLE_PROFILE, DEFAULT_STRICT_THRESHOLDS } from './presets'

describe('bot-load validation', () => {
  it('接受 command-orchestration-v1 预设', () => {
    expect(validateCommandSchedule(COMMAND_ORCHESTRATION_V1)).toEqual([])
  })

  it('拒绝空命令与非法 id', () => {
    const errors = validateCommandSchedule({
      durationMs: 1000,
      commands: [{ id: 'bad id', atMs: 0, command: 'hi' }],
    })
    expect(errors.some((e) => e.path.includes('id'))).toBe(true)
  })

  it('拒绝 occurrence 超出 durationMs', () => {
    const errors = validateCommandSchedule({
      durationMs: 1000,
      commands: [{ id: 'a', atMs: 0, command: 'x', repeat: { intervalMs: 600, count: 3 } }],
    })
    expect(errors.some((e) => e.path.includes('repeat'))).toBe(true)
  })

  it('校验 stable/step/spike profile', () => {
    expect(validateLoadProfile(DEFAULT_STABLE_PROFILE)).toEqual([])
    expect(
      validateLoadProfile({
        type: 'step',
        stages: [
          { targetBots: 10, holdSeconds: 30 },
          { targetBots: 20, holdSeconds: 30 },
        ],
        stopOnThresholdFailure: true,
      }),
    ).toEqual([])
    const badStep = validateLoadProfile({
      type: 'step',
      stages: [
        { targetBots: 20, holdSeconds: 30 },
        { targetBots: 10, holdSeconds: 30 },
      ],
      stopOnThresholdFailure: true,
    })
    expect(badStep.length).toBeGreaterThan(0)
  })

  it('校验严格阈值默认值', () => {
    expect(validateThresholds(DEFAULT_STRICT_THRESHOLDS)).toEqual([])
    expect(validateThresholds({ ...DEFAULT_STRICT_THRESHOLDS, minOnlineRate: 1.5 }).length).toBeGreaterThan(0)
  })

  it('连接配置仅允许 offline', () => {
    expect(
      validateConnection({ server: '127.0.0.1', port: 25565, auth: 'offline', namePrefix: 'load' }),
    ).toEqual([])
    expect(
      validateConnection({ server: '127.0.0.1', port: 25565, auth: 'microsoft', namePrefix: 'load' }).some(
        (e) => e.path === 'config.auth',
      ),
    ).toBe(true)
  })

  it('count 必须匹配 profile 目标', () => {
    expect(validateCountMatchesProfile(50, DEFAULT_STABLE_PROFILE)).toEqual([])
    expect(validateCountMatchesProfile(10, DEFAULT_STABLE_PROFILE).length).toBeGreaterThan(0)
  })

  it('预览首末 Bot 名', () => {
    expect(previewBotNames('load', 100)).toEqual({ first: 'load-001', last: 'load-100' })
  })
})
