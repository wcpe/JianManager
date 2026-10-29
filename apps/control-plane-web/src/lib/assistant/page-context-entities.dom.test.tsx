import { describe, expect, it, vi } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useContextChips } from '@/lib/assistant/page-context'

/** 
 * 芯片 → 实体引用的传递（§5.2 与芯片联动的接缝）。
 *
 * 为什么单独守这一处：面板层的测试用显式 props 传入 `entity`，
 * 因此**测不到** `useContextChips` 是否真的把实体带出来。
 * 曾经把它去掉后，面板测试仍全绿——但真实界面上芯片会失去指标与跳转入口。
 * 这条用例补的就是这个接缝。
 */

describe('useContextChips · 实体引用传递', () => {
  it('实体芯片必须带 entity，否则指标与详情入口会静默消失', () => {
    const onChange = vi.fn()
    const { result } = renderHook(() =>
      useContextChips(
        { pageKey: 'instances', entities: [{ type: 'instance', id: 7 }] },
        onChange,
      ),
    )

    const chip = result.current.find((c) => c.key === 'ent:instance:7')
    expect(chip).toBeDefined()
    expect(chip!.entity).toEqual({ type: 'instance', id: 7 })
  })

  it('非实体芯片不带 entity（不该让页面/页签也被当成实例去取指标）', () => {
    const onChange = vi.fn()
    const { result } = renderHook(() =>
      useContextChips({ pageKey: 'logs', tab: 'container' }, onChange),
    )

    expect(result.current.length).toBeGreaterThan(0)
    for (const chip of result.current) {
      expect(chip.entity).toBeUndefined()
    }
  })

  it('移除实体后该芯片消失，且回写里也不含它', () => {
    const onChange = vi.fn()
    const { result, rerender } = renderHook(
      ({ entities }) => useContextChips({ pageKey: 'instances', entities }, onChange),
      { initialProps: { entities: [{ type: 'instance' as const, id: 7 }] } },
    )

    const chip = result.current.find((c) => c.key === 'ent:instance:7')
    expect(chip).toBeDefined()
    chip!.onRemove()

    // 回写内容不含被移除的实体（外发快照据此生成）。
    expect(onChange).toHaveBeenCalled()
    const next = onChange.mock.calls[0][0]
    expect(next.entities ?? []).toHaveLength(0)

    // 重新渲染同一份输入（模拟父层尚未回写）时，被移除项仍应被过滤掉。
    rerender({ entities: [{ type: 'instance' as const, id: 7 }] })
    expect(result.current.find((c) => c.key === 'ent:instance:7')).toBeUndefined()
  })
})
