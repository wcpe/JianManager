import type { BotLoadCommandSchedule, BotLoadProfile, BotLoadThresholds } from '@/api/botLoad'
import { estimateProfileDurationSeconds, targetBotsFromProfile } from './presets'

/** 命令计划摘要文案键数据。 */
export function summarizeCommandSchedule(schedule: BotLoadCommandSchedule): {
  commandCount: number
  durationMs: number
  occurrenceCount: number
  firstCommand: string
} {
  const cmds = schedule.commands ?? []
  const occurrenceCount = cmds.reduce((sum, c) => sum + (c.repeat?.count ?? 1), 0)
  return {
    commandCount: cmds.length,
    durationMs: schedule.durationMs,
    occurrenceCount,
    firstCommand: cmds[0]?.command ?? '',
  }
}

/** 负载曲线摘要。 */
export function summarizeLoadProfile(profile: BotLoadProfile): {
  type: BotLoadProfile['type']
  targetBots: number
  durationSeconds: number
  stageCount?: number
} {
  return {
    type: profile.type,
    targetBots: targetBotsFromProfile(profile),
    durationSeconds: estimateProfileDurationSeconds(profile),
    stageCount: profile.type === 'step' ? profile.stages.length : undefined,
  }
}

/** 阈值摘要（关键几项）。 */
export function summarizeThresholds(t: BotLoadThresholds): {
  minOnlineRate: number
  minWorkerHealthRate: number
  maxProcessCrashes: number
} {
  return {
    minOnlineRate: t.minOnlineRate,
    minWorkerHealthRate: t.minWorkerHealthRate,
    maxProcessCrashes: t.maxProcessCrashes,
  }
}

/**
 * 将 commandSchedule 序列化为可读 YAML（无额外依赖，手工生成）。
 * 仅覆盖通用命令计划字段，不做全量 YAML 解析器。
 */
export function commandScheduleToYaml(schedule: BotLoadCommandSchedule): string {
  const lines: string[] = []
  lines.push(`durationMs: ${schedule.durationMs}`)
  lines.push(`jitterMs: ${schedule.jitterMs ?? 0}`)
  lines.push('commands:')
  for (const c of schedule.commands) {
    lines.push(`  - id: ${yamlScalar(c.id)}`)
    lines.push(`    atMs: ${c.atMs}`)
    lines.push(`    command: ${yamlScalar(c.command)}`)
    if (c.repeat) {
      lines.push('    repeat:')
      lines.push(`      intervalMs: ${c.repeat.intervalMs}`)
      lines.push(`      count: ${c.repeat.count}`)
    }
  }
  return lines.join('\n') + '\n'
}

/**
 * 解析受限 YAML 命令计划（仅支持本编辑器写出的子集）。
 * 失败返回 null；成功返回结构化 schedule。
 */
export function yamlToCommandSchedule(raw: string): BotLoadCommandSchedule | null {
  try {
    // 优先尝试 JSON（高级模式也可贴 JSON）
    const trimmed = raw.trim()
    if (trimmed.startsWith('{')) {
      const parsed = JSON.parse(trimmed) as BotLoadCommandSchedule
      if (parsed && Array.isArray(parsed.commands)) return normalizeSchedule(parsed)
    }
    return parseSimpleYamlSchedule(raw)
  } catch {
    return null
  }
}

function normalizeSchedule(s: BotLoadCommandSchedule): BotLoadCommandSchedule {
  return {
    durationMs: Number(s.durationMs) || 0,
    jitterMs: s.jitterMs === undefined ? 0 : Number(s.jitterMs) || 0,
    commands: (s.commands ?? []).map((c) => ({
      id: String(c.id ?? ''),
      atMs: Number(c.atMs) || 0,
      command: String(c.command ?? ''),
      repeat: c.repeat
        ? { intervalMs: Number(c.repeat.intervalMs) || 0, count: Number(c.repeat.count) || 0 }
        : undefined,
    })),
  }
}

/** 极简 YAML 解析：仅支持本编辑器输出的缩进键值结构。 */
function parseSimpleYamlSchedule(raw: string): BotLoadCommandSchedule | null {
  const lines = raw.split(/\r?\n/)
  let durationMs = 0
  let jitterMs = 0
  const commands: BotLoadCommandSchedule['commands'] = []
  let current: BotLoadCommandSchedule['commands'][number] | null = null
  let inRepeat = false

  for (const line of lines) {
    if (!line.trim() || line.trim().startsWith('#')) continue
    const dur = line.match(/^durationMs:\s*(.+)$/)
    if (dur) {
      durationMs = Number(unquote(dur[1].trim()))
      continue
    }
    const jit = line.match(/^jitterMs:\s*(.+)$/)
    if (jit) {
      jitterMs = Number(unquote(jit[1].trim()))
      continue
    }
    if (/^commands:\s*$/.test(line)) continue
    const item = line.match(/^\s+-\s+id:\s*(.+)$/)
    if (item) {
      if (current) commands.push(current)
      current = { id: unquote(item[1].trim()), atMs: 0, command: '' }
      inRepeat = false
      continue
    }
    if (!current) continue
    const atMs = line.match(/^\s+atMs:\s*(.+)$/)
    if (atMs) {
      current.atMs = Number(unquote(atMs[1].trim()))
      inRepeat = false
      continue
    }
    const cmd = line.match(/^\s+command:\s*(.+)$/)
    if (cmd) {
      current.command = unquote(cmd[1].trim())
      inRepeat = false
      continue
    }
    if (/^\s+repeat:\s*$/.test(line)) {
      current.repeat = { intervalMs: 0, count: 0 }
      inRepeat = true
      continue
    }
    if (inRepeat && current.repeat) {
      const iv = line.match(/^\s+intervalMs:\s*(.+)$/)
      if (iv) {
        current.repeat.intervalMs = Number(unquote(iv[1].trim()))
        continue
      }
      const ct = line.match(/^\s+count:\s*(.+)$/)
      if (ct) {
        current.repeat.count = Number(unquote(ct[1].trim()))
        continue
      }
    }
  }
  if (current) commands.push(current)
  if (commands.length === 0) return null
  return normalizeSchedule({ durationMs, jitterMs, commands })
}

function yamlScalar(value: string): string {
  if (/^[\w./:@%+-]+$/.test(value) && !/^\d+$/.test(value)) return value
  return JSON.stringify(value)
}

function unquote(s: string): string {
  if ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'"))) {
    try {
      return JSON.parse(s.startsWith("'") ? `"${s.slice(1, -1).replace(/"/g, '\\"')}"` : s) as string
    } catch {
      return s.slice(1, -1)
    }
  }
  return s
}
