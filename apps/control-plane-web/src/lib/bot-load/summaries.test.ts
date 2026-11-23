import { describe, expect, it } from 'vitest'
import { commandScheduleToYaml, summarizeCommandSchedule, yamlToCommandSchedule } from '@/lib/bot-load/bot-load-summaries'
import { COMMAND_ORCHESTRATION_V1 } from '@/lib/bot-load/bot-load-presets'

describe('bot-load summaries / yaml 往返', () => {
  it('摘要命令计划', () => {
    const s = summarizeCommandSchedule(COMMAND_ORCHESTRATION_V1)
    expect(s.commandCount).toBe(3)
    expect(s.occurrenceCount).toBe(5) // 1 + 3 + 1
  })

  it('YAML 往返不丢支持字段', () => {
    const yaml = commandScheduleToYaml(COMMAND_ORCHESTRATION_V1)
    const back = yamlToCommandSchedule(yaml)
    expect(back).not.toBeNull()
    expect(back!.durationMs).toBe(COMMAND_ORCHESTRATION_V1.durationMs)
    expect(back!.commands).toHaveLength(3)
    expect(back!.commands[1].repeat?.count).toBe(3)
    expect(back!.commands[0].command).toBe(COMMAND_ORCHESTRATION_V1.commands[0].command)
  })

  it('引号标量按 YAML 规则保留命令正文', () => {
    const cases = [
      { name: '单引号 Windows 路径', scalar: String.raw`'say C:\temp'`, command: String.raw`say C:\temp` },
      { name: '单引号字面量反斜杠 n', scalar: String.raw`'say hello\nworld'`, command: String.raw`say hello\nworld` },
      { name: '单引号连续反斜杠', scalar: String.raw`'say a\\b'`, command: String.raw`say a\\b` },
      { name: '双单引号表示正文撇号', scalar: "'say it''s ready'", command: "say it's ready" },
      { name: '双引号继续解释转义', scalar: String.raw`"say hello\nworld"`, command: 'say hello\nworld' },
    ]
    for (const entry of cases) {
      const yaml = `durationMs: 1000\njitterMs: 0\ncommands:\n  - id: cmd-literal\n    atMs: 0\n    command: ${entry.scalar}\n`
      const parsed = yamlToCommandSchedule(yaml)
      expect(parsed, entry.name).not.toBeNull()
      expect.soft(parsed!.commands[0].command, entry.name).toBe(entry.command)
    }
  })

  it('接受 JSON 高级模式', () => {
    const json = JSON.stringify(COMMAND_ORCHESTRATION_V1)
    const back = yamlToCommandSchedule(json)
    expect(back?.commands[0].id).toBe('cmd-hello')
  })
})
