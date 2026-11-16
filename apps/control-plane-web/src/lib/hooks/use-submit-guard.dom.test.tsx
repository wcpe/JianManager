import { describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useSubmitGuard } from './use-submit-guard'

/**
 * 防重钩子的核心契约：
 *  ① 同 tick 连点两次只执行一次（这是 setState 版防重做不到的，也是本钩子存在的理由）；
 *  ② 在途态对渲染可见，结束后归位；
 *  ③ 失败也释放闸门（不能卡在永远 pending）。
 */
describe('useSubmitGuard', () => {
  it('同一 tick 内连续调用两次，底层只执行一次', async () => {
    let resolveFn: (v: string) => void = () => {}
    const fn = vi.fn(
      () =>
        new Promise<string>((res) => {
          resolveFn = res
        }),
    )
    const { result } = renderHook(() => useSubmitGuard(fn))

    let p1: Promise<string | undefined>
    let p2: Promise<string | undefined>
    act(() => {
      // 关键：不 await 第一次，紧接着发第二次——模拟真实连点。
      p1 = result.current.run()
      p2 = result.current.run()
    })

    expect(fn).toHaveBeenCalledTimes(1)

    await act(async () => {
      resolveFn('ok')
      await Promise.all([p1!, p2!])
    })

    // 被拦截的那次返回 undefined，调用方可据此区分「真的执行了吗」。
    await expect(p1!).resolves.toBe('ok')
    await expect(p2!).resolves.toBeUndefined()
  })

  it('在途态对渲染可见，完成后归位', async () => {
    let resolveFn: () => void = () => {}
    const fn = vi.fn(() => new Promise<void>((res) => (resolveFn = res)))
    const { result } = renderHook(() => useSubmitGuard(fn))

    expect(result.current.pending).toBe(false)

    let p: Promise<void | undefined>
    act(() => {
      p = result.current.run()
    })
    expect(result.current.pending).toBe(true)

    await act(async () => {
      resolveFn()
      await p!
    })
    expect(result.current.pending).toBe(false)
  })

  it('底层抛错时闸门也要释放（不能卡死在 pending）', async () => {
    const fn = vi.fn(() => Promise.reject(new Error('boom')))
    const { result } = renderHook(() => useSubmitGuard(fn))

    await act(async () => {
      await expect(result.current.run()).rejects.toThrow('boom')
    })
    expect(result.current.pending).toBe(false)

    // 释放后允许再次提交（重试路径）。
    await act(async () => {
      await expect(result.current.run()).rejects.toThrow('boom')
    })
    expect(fn).toHaveBeenCalledTimes(2)
  })

  it('完成之后可以再次提交（不是一次性锁）', async () => {
    const fn = vi.fn(() => Promise.resolve(1))
    const { result } = renderHook(() => useSubmitGuard(fn))

    await act(async () => {
      await result.current.run()
    })
    await act(async () => {
      await result.current.run()
    })
    expect(fn).toHaveBeenCalledTimes(2)
  })
})
