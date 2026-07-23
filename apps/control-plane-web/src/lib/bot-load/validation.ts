import type { BotLoadCommand, BotLoadCommandSchedule, BotLoadProfile, BotLoadThresholds } from '@/api/botLoad'
import { targetBotsFromProfile } from './presets'

export interface FieldError {
  path: string
  message: string
}

const CMD_ID_RE = /^[A-Za-z0-9._-]+$/
const BARRIER_KEY_RE = /^[A-Za-z0-9._-]+$/
const CONTROL_CHAR_RE = /[\u0000-\u001F\u007F]/

function isInt(n: number): boolean {
  return Number.isFinite(n) && Number.isInteger(n)
}

function utf8ByteLength(s: string): number {
  return new TextEncoder().encode(s).length
}

/** 校验命令编排计划，返回 path 级错误。 */
export function validateCommandSchedule(schedule: BotLoadCommandSchedule): FieldError[] {
  const errors: FieldError[] = []
  const cmds = schedule.commands ?? []
  if (cmds.length < 1 || cmds.length > 100) {
    errors.push({ path: 'commandSchedule.commands', message: 'commands 数量须为 1..100' })
  }
  if (!isInt(schedule.durationMs) || schedule.durationMs < 1 || schedule.durationMs > 86_400_000) {
    errors.push({ path: 'commandSchedule.durationMs', message: 'durationMs 须为 1..86400000 的整数' })
  }
  const jitter = schedule.jitterMs ?? 0
  if (!isInt(jitter) || jitter < 0 || jitter > Math.min(60000, schedule.durationMs || 0)) {
    errors.push({ path: 'commandSchedule.jitterMs', message: 'jitterMs 须为 0..min(60000,durationMs)' })
  }

  const ids = new Set<string>()
  let occurrences = 0
  cmds.forEach((cmd, i) => {
    errors.push(...validateCommand(cmd, i, schedule.durationMs, ids))
    const count = cmd.repeat?.count ?? 1
    occurrences += count
  })
  if (occurrences < 1 || occurrences > 1000) {
    errors.push({ path: 'commandSchedule.commands', message: '展开 occurrence 总数须为 1..1000' })
  }
  return errors
}

function validateCommand(
  cmd: BotLoadCommand,
  index: number,
  durationMs: number,
  ids: Set<string>,
): FieldError[] {
  const errors: FieldError[] = []
  const base = `commandSchedule.commands[${index}]`
  if (!cmd.id || cmd.id.length > 64 || !CMD_ID_RE.test(cmd.id)) {
    errors.push({ path: `${base}.id`, message: 'id 长度 1..64 且匹配 [A-Za-z0-9._-]+' })
  } else if (ids.has(cmd.id)) {
    errors.push({ path: `${base}.id`, message: '命令 id 计划内须唯一' })
  } else {
    ids.add(cmd.id)
  }
  if (!isInt(cmd.atMs) || cmd.atMs < 0 || cmd.atMs > durationMs) {
    errors.push({ path: `${base}.atMs`, message: 'atMs 须为 0..durationMs' })
  }
  const bytes = utf8ByteLength(cmd.command ?? '')
  if (bytes < 1 || bytes > 1024) {
    errors.push({ path: `${base}.command`, message: 'command UTF-8 长度须为 1..1024 bytes' })
  } else if (CONTROL_CHAR_RE.test(cmd.command)) {
    errors.push({ path: `${base}.command`, message: 'command 禁止控制字符' })
  }
  if (cmd.repeat) {
    if (!isInt(cmd.repeat.intervalMs) || cmd.repeat.intervalMs < 1 || cmd.repeat.intervalMs > 86_400_000) {
      errors.push({ path: `${base}.repeat.intervalMs`, message: 'intervalMs 须为 1..86400000' })
    }
    if (!isInt(cmd.repeat.count) || cmd.repeat.count < 1 || cmd.repeat.count > 1000) {
      errors.push({ path: `${base}.repeat.count`, message: 'count 须为 1..1000' })
    }
    for (let o = 0; o < (cmd.repeat.count ?? 0); o++) {
      const t = cmd.atMs + o * cmd.repeat.intervalMs
      if (t > durationMs) {
        errors.push({ path: `${base}.repeat`, message: 'occurrence 时间不得超过 durationMs' })
        break
      }
    }
  }
  return errors
}

/** 校验负载曲线。 */
export function validateLoadProfile(profile: BotLoadProfile): FieldError[] {
  const errors: FieldError[] = []
  if (profile.type === 'stable') {
    if (!isInt(profile.targetBots) || profile.targetBots < 1 || profile.targetBots > 12800) {
      errors.push({ path: 'loadProfile.targetBots', message: 'targetBots 须为 1..12800' })
    }
    if (!isInt(profile.rampUpSeconds) || profile.rampUpSeconds < 0 || profile.rampUpSeconds > 86400) {
      errors.push({ path: 'loadProfile.rampUpSeconds', message: 'rampUpSeconds 须为 0..86400' })
    }
    if (!isInt(profile.durationSeconds) || profile.durationSeconds < 10 || profile.durationSeconds > 86400) {
      errors.push({ path: 'loadProfile.durationSeconds', message: 'durationSeconds 须为 10..86400' })
    }
  } else if (profile.type === 'step') {
    if (!profile.stages || profile.stages.length < 1 || profile.stages.length > 64) {
      errors.push({ path: 'loadProfile.stages', message: 'stages 数量须为 1..64' })
    }
    let prev = 0
    let holdSum = 0
    profile.stages?.forEach((s, i) => {
      if (!isInt(s.targetBots) || s.targetBots < 1 || s.targetBots > 12800 || s.targetBots <= prev) {
        errors.push({ path: `loadProfile.stages[${i}].targetBots`, message: 'targetBots 须严格递增且 1..12800' })
      }
      prev = s.targetBots
      if (!isInt(s.holdSeconds) || s.holdSeconds < 10 || s.holdSeconds > 86400) {
        errors.push({ path: `loadProfile.stages[${i}].holdSeconds`, message: 'holdSeconds 须为 10..86400' })
      }
      holdSum += s.holdSeconds || 0
    })
    if (holdSum > 604800) {
      errors.push({ path: 'loadProfile.stages', message: 'holdSeconds 总和不得超过 604800' })
    }
    if (typeof profile.stopOnThresholdFailure !== 'boolean') {
      errors.push({ path: 'loadProfile.stopOnThresholdFailure', message: 'stopOnThresholdFailure 须为 boolean' })
    }
  } else if (profile.type === 'spike') {
    if (!isInt(profile.targetBots) || profile.targetBots < 1 || profile.targetBots > 12800) {
      errors.push({ path: 'loadProfile.targetBots', message: 'targetBots 须为 1..12800' })
    }
    if (
      !isInt(profile.connectWindowSeconds) ||
      profile.connectWindowSeconds < 1 ||
      profile.connectWindowSeconds > 3600
    ) {
      errors.push({ path: 'loadProfile.connectWindowSeconds', message: 'connectWindowSeconds 须为 1..3600' })
    }
    if (!isInt(profile.holdSeconds) || profile.holdSeconds < 10 || profile.holdSeconds > 86400) {
      errors.push({ path: 'loadProfile.holdSeconds', message: 'holdSeconds 须为 10..86400' })
    }
    if (profile.barrier) {
      if (
        !profile.barrier.key ||
        profile.barrier.key.length > 64 ||
        !BARRIER_KEY_RE.test(profile.barrier.key)
      ) {
        errors.push({ path: 'loadProfile.barrier.key', message: 'barrier.key 长度 1..64 且匹配 [A-Za-z0-9._-]+' })
      }
      if (
        !isInt(profile.barrier.releaseWindowMs) ||
        profile.barrier.releaseWindowMs < 1 ||
        profile.barrier.releaseWindowMs > 60000
      ) {
        errors.push({ path: 'loadProfile.barrier.releaseWindowMs', message: 'releaseWindowMs 须为 1..60000' })
      }
    }
  }
  return errors
}

/** 校验阈值。 */
export function validateThresholds(t: BotLoadThresholds): FieldError[] {
  const errors: FieldError[] = []
  const rates: Array<[keyof BotLoadThresholds, number]> = [
    ['minOnlineRate', t.minOnlineRate],
    ['minCommandSentRate', t.minCommandSentRate],
    ['minScheduleCompletionRate', t.minScheduleCompletionRate],
    ['minWorkerHealthRate', t.minWorkerHealthRate],
    ['minBarrierArrivalRate', t.minBarrierArrivalRate],
  ]
  for (const [key, val] of rates) {
    if (typeof val !== 'number' || !Number.isFinite(val) || val < 0 || val > 1) {
      errors.push({ path: `thresholds.${key}`, message: `${key} 须为 0..1` })
    }
  }
  if (!isInt(t.maxScheduleLagP95Ms) || t.maxScheduleLagP95Ms < 0 || t.maxScheduleLagP95Ms > 600000) {
    errors.push({ path: 'thresholds.maxScheduleLagP95Ms', message: 'maxScheduleLagP95Ms 须为 0..600000' })
  }
  if (!isInt(t.maxProcessCrashes) || t.maxProcessCrashes < 0 || t.maxProcessCrashes > 1000) {
    errors.push({ path: 'thresholds.maxProcessCrashes', message: 'maxProcessCrashes 须为 0..1000' })
  }
  return errors
}

/** 校验连接配置。 */
export function validateConnection(config: {
  server: string
  port: number
  auth: string
  namePrefix: string
}): FieldError[] {
  const errors: FieldError[] = []
  if (!config.server?.trim()) {
    errors.push({ path: 'config.server', message: '服务器地址必填' })
  }
  if (!isInt(config.port) || config.port < 1 || config.port > 65535) {
    errors.push({ path: 'config.port', message: '端口须为 1..65535' })
  }
  if (config.auth !== 'offline') {
    errors.push({ path: 'config.auth', message: 'V2 向导仅支持 offline' })
  }
  const prefix = config.namePrefix?.trim() ?? ''
  if (!prefix || prefix.length > 32) {
    errors.push({ path: 'namePrefix', message: 'namePrefix 长度 1..32' })
  }
  return errors
}

/** 校验目标 Bot 数与 profile 一致。 */
export function validateCountMatchesProfile(count: number, profile: BotLoadProfile): FieldError[] {
  const target = targetBotsFromProfile(profile)
  if (count !== target) {
    return [{ path: 'count', message: `count 须等于 profile 目标数 ${target}` }]
  }
  return []
}

/** 预览 namePrefix 首/末 Bot 名。 */
export function previewBotNames(namePrefix: string, count: number): { first: string; last: string } {
  const prefix = namePrefix || 'bot'
  const pad = Math.max(3, String(Math.max(count, 1)).length)
  const first = `${prefix}-${String(1).padStart(pad, '0')}`
  const last = `${prefix}-${String(Math.max(count, 1)).padStart(pad, '0')}`
  return { first, last }
}
