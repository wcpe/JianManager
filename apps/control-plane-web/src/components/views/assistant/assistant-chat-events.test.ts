import { describe, expect, it } from 'vitest'
import { applyEventToMessages } from '@/components/views/assistant/assistant-chat-events'
import { isToolDispatcher, isToolExploration } from '@/components/views/assistant/assistant-tool-status'
import type { AssistantEventFrame } from '@/api/assistant-chat'

/** 
 * 事件 → 消息聚合。
 *
 * 这是聊天里最容易出「看起来能用但偶尔丢字」的地方，所以单独测：
 *  - 增量文本必须**按到达顺序拼接**，不能被后来的帧覆盖；
 *  - run_end 必须**收束流式态**并带上终态说明（note 为空 = 正常完成）；
 *  - 未知事件类型（的工具事件）必须**忽略而不报错**——否则上线当天
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

  it('run_end 不带 note 时**不清掉**之前 error 写的失败原因', () => {
    // 真实上游不可达时的事件序列就是 error → run_end（run_end 的 data 里没有 note）。
    // 若无条件用 run_end 的 note 覆盖，界面会只剩一条没有回复也没有提示的提问。
    let list = applyEventToMessages(
      [],
      frame({ seq: 1, type: 'error', data: { message: '请求上游失败：connection refused' } }),
      'run-1',
    )
    list = applyEventToMessages(list, frame({ seq: 2, type: 'run_end', data: {} }), 'run-1')
    expect(list[0]!.streaming).toBe(false)
    expect(list[0]!.note).toBe('请求上游失败：connection refused')
  })

  it('未知事件类型被忽略且不影响既有内容（前向兼容）', () => {
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

/** 
 * 渐进式披露的工具分类。
 *
 * 断言点：`call_tool` **不算探索**。它是真正执行——若把它也标成「探索」，
 * 用户会把一次真实的写操作误读成「它只是在查工具」，那是危险的误标。
 */
describe('isToolExploration / isToolDispatcher', () => {
  it('search 与 describe 属于探索', () => {
    expect(isToolExploration('system_search_tools')).toBe(true)
    expect(isToolExploration('system_describe_tool')).toBe(true)
  })

  it('call_tool 不算探索，而是执行入口', () => {
    expect(isToolExploration('system_call_tool')).toBe(false)
    expect(isToolDispatcher('system_call_tool')).toBe(true)
  })

  it('普通业务工具两者都不是', () => {
    for (const name of ['agent_get_instance', 'instance_start', 'conn_slug_tool']) {
      expect(isToolExploration(name)).toBe(false)
      expect(isToolDispatcher(name)).toBe(false)
    }
  })
})
