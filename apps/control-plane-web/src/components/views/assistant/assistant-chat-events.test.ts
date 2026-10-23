import { describe, expect, it } from 'vitest'
import { applyEventToMessages } from '@/components/views/assistant/assistant-chat-events'
import type { AssistantEventFrame } from '@/api/assistant-chat'

/**
 * 事件 → 消息聚合（FR-519）。
 *
 * 这是聊天里最容易出「看起来能用但偶尔丢字」的地方，所以单独测：
 *  - 增量文本必须**按到达顺序拼接**，不能被后来的帧覆盖；
 *  - run_end 必须**收束流式态**并带上终态说明（note 为空 = 正常完成）；
 *  - 未知事件类型（FR-516 的工具事件）必须**忽略而不报错**——否则 FR-516 上线当天
 *    聊天会整条流断在第一帧工具事件上。
 */
function frame(partial: Partial<AssistantEventFrame> & Pick<AssistantEventFrame, 'type'>): AssistantEventFrame {
  return { seq: 1, data: null, createdAt: '2026-10-23T00:00:00Z', ...partial }
}

describe('applyEventToMessages', () => {
  it('文本增量按序拼接，且保持 streaming', () => {
    let list = applyEventToMessages([], frame({ type: 'text_delta', data: { text: '你' } }), 'run-1')
    list = applyEventToMessages(list, frame({ seq: 2, type: 'text_delta', data: { text: '好' } }), 'run-1')
    expect(list).toHaveLength(1)
    expect(list[0]!.content).toBe('你好')
    expect(list[0]!.streaming).toBe(true)
  })

  it('空文本帧不产出空消息', () => {
    const list = applyEventToMessages([], frame({ type: 'text_delta', data: { text: '' } }), 'run-1')
    expect(list).toHaveLength(0)
  })

  it('run_end 收束流式态；note 为空时不写说明', () => {
    let list = applyEventToMessages([], frame({ type: 'text_delta', data: { text: 'hi' } }), 'run-1')
    list = applyEventToMessages(list, frame({ seq: 2, type: 'run_end', data: {} }), 'run-1')
    expect(list[0]!.streaming).toBe(false)
    expect(list[0]!.note).toBeUndefined()
  })

  it('run_end 带 note 时保留说明（如预算耗尽、被取消）', () => {
    let list = applyEventToMessages([], frame({ type: 'text_delta', data: { text: 'hi' } }), 'run-1')
    list = applyEventToMessages(
      list,
      frame({ seq: 2, type: 'run_end', data: { note: '已达工具轮次上限' } }),
      'run-1',
    )
    expect(list[0]!.note).toBe('已达工具轮次上限')
  })

  it('未知事件类型被忽略且不影响既有内容（FR-516 前向兼容）', () => {
    let list = applyEventToMessages([], frame({ type: 'text_delta', data: { text: 'a' } }), 'run-1')
    const before = JSON.stringify(list)
    for (const t of ['tool_call', 'tool_status', 'tool_result', 'usage', 'context_warning']) {
      list = applyEventToMessages(list, frame({ seq: 2, type: t, data: { anything: true } }), 'run-1')
    }
    expect(JSON.stringify(list)).toBe(before)
  })

  it('error 事件结束流式并保留原因', () => {
    let list = applyEventToMessages([], frame({ type: 'text_delta', data: { text: 'a' } }), 'run-1')
    list = applyEventToMessages(
      list,
      frame({ seq: 2, type: 'error', data: { message: '上游 429' } }),
      'run-1',
    )
    expect(list[0]!.streaming).toBe(false)
    expect(list[0]!.note).toBe('上游 429')
  })

  it('不同 run 的事件互不干扰（key 隔离）', () => {
    let list = applyEventToMessages([], frame({ type: 'text_delta', data: { text: 'A' } }), 'run-1')
    list = applyEventToMessages(list, frame({ type: 'text_delta', data: { text: 'B' } }), 'run-2')
    expect(list).toHaveLength(2)
    expect(list[0]!.content).toBe('A')
    expect(list[1]!.content).toBe('B')
  })
})
