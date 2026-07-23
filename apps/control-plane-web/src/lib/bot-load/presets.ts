import type { BotLoadCommandSchedule, BotLoadProfile, BotLoadThresholds } from '@/api/botLoad'

/** 内置命令编排预设 command-orchestration-v1。 */
export const COMMAND_ORCHESTRATION_V1: BotLoadCommandSchedule = {
  commands: [
    { id: 'cmd-hello', atMs: 0, command: 'hello {{botName}}' },
    {
      id: 'cmd-status',
      atMs: 5000,
      command: 'status {{botOrdinal}}',
      repeat: { intervalMs: 10000, count: 3 },
    },
    { id: 'cmd-ping', atMs: 15000, command: 'ping' },
  ],
  durationMs: 60000,
  jitterMs: 0,
}

/** 严格阈值默认值（minWorkerHealthRate=0.99）。 */
export const DEFAULT_STRICT_THRESHOLDS: BotLoadThresholds = {
  minOnlineRate: 0.95,
  minCommandSentRate: 0.9,
  minScheduleCompletionRate: 0.9,
  minWorkerHealthRate: 0.99,
  minBarrierArrivalRate: 0.95,
  maxScheduleLagP95Ms: 5000,
  maxProcessCrashes: 0,
}

/** 默认 stable 负载曲线。 */
export const DEFAULT_STABLE_PROFILE: BotLoadProfile = {
  type: 'stable',
  targetBots: 50,
  rampUpSeconds: 30,
  durationSeconds: 300,
}

/** 从 profile 派生目标 Bot 数。 */
export function targetBotsFromProfile(profile: BotLoadProfile): number {
  if (profile.type === 'step') {
    if (profile.stages.length === 0) return 0
    return Math.max(...profile.stages.map((s) => s.targetBots))
  }
  return profile.targetBots
}

/** 估算 profile 预计总时长（秒）。 */
export function estimateProfileDurationSeconds(profile: BotLoadProfile): number {
  if (profile.type === 'stable') {
    return profile.rampUpSeconds + profile.durationSeconds
  }
  if (profile.type === 'step') {
    return profile.stages.reduce((sum, s) => sum + s.holdSeconds, 0)
  }
  return profile.connectWindowSeconds + profile.holdSeconds
}
