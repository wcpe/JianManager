import { describe, expect, it } from 'vitest'
import { commandScheduleToYaml, summarizeCommandSchedule, yamlToCommandSchedule } from './summaries'
import { COMMAND_ORCHESTRATION_V1 } from './presets'

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

  it('接受 JSON 高级模式', () => {
    const json = JSON.stringify(COMMAND_ORCHESTRATION_V1)
    const back = yamlToCommandSchedule(json)
    expect(back?.commands[0].id).toBe('cmd-hello')
  })
})
