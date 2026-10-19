import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { queryClient } from '@/lib/query-client'

vi.mock('@/stores/permissions', () => ({
  usePermissionsStore: { getState: () => ({ loadFromAuthMe: vi.fn(), reset: vi.fn() }) },
}))
vi.mock('@/lib/terminal-session-manager', () => ({
  terminalSessionManager: { disposeAll: vi.fn() },
}))

import { useAuthStore } from './auth'

function tokenFor(userId: number, username: string): string {
  const payload = btoa(JSON.stringify({ userId, username, role: 0, exp: Math.floor(Date.now() / 1000) + 900 }))
  return `mock.${payload}.sig`
}

describe('auth store 查询缓存隔离', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  beforeEach(() => {
    const values = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => values.set(key, value),
      removeItem: (key: string) => values.delete(key),
      clear: () => values.clear(),
    })
    localStorage.clear()
    queryClient.clear()
    useAuthStore.setState({
      accessToken: null,
      refreshToken: null,
      isAuthenticated: false,
      role: null,
      username: null,
    })
  })

  it('登录切换主体时清空旧主体缓存', () => {
    useAuthStore.getState().login(tokenFor(1, 'alice'), 'refresh-a')
    queryClient.setQueryData(['private', 'alice'], { value: 'alice-only' })
    useAuthStore.getState().login(tokenFor(2, 'bob'), 'refresh-b')
    expect(queryClient.getQueryData(['private', 'alice'])).toBeUndefined()
  })

  it('登出时清空当前主体缓存', () => {
    useAuthStore.getState().login(tokenFor(1, 'alice'), 'refresh-a')
    queryClient.setQueryData(['private', 'alice'], { value: 'alice-only' })
    useAuthStore.getState().logout()
    expect(queryClient.getQueryData(['private', 'alice'])).toBeUndefined()
  })

  /**
   * 回归（2026-10-19，评审 M-5）：flushAuthDynamicImports 必须等**续体**，而不只是 import。
   *
   * 这是确定性判据（不是时序竞态）：续体返回一个受控的 pending Promise，
   * 在它 resolve 之前 flush **不得**返回。
   * - 旧实现（只登记 import 本身）：flush 立即返回 ⇒ 断言失败；
   * - 新实现（续体并进同一登记项）：flush 一直等到 gate resolve ⇒ 断言通过。
   *
   * 为什么不再用「延时置标志」：flush 只能等 Promise、等不到宏任务；而用微任务跳数做区分
   * 又依赖调度细节，实测在旧实现下也会通过（假护栏）。受控 gate 没有这类不确定性。
   */
  it('续体未完成时 flush 不得返回（只等 import 的实现会失败）', async () => {
    const { flushAuthDynamicImports, trackDynamicImport } = await import('./auth')

    let releaseGate: () => void = () => {}
    const gate = new Promise<void>((resolve) => {
      releaseGate = resolve
    })
    let continuationCalled = false

    // 模拟 `import().then(续体)`：续体返回一个 pending Promise。
    void trackDynamicImport(Promise.resolve({ ok: true }), () => {
      continuationCalled = true
      return gate.then(() => undefined)
    })

    let flushed = false
    const flushedPromise = flushAuthDynamicImports().then(() => {
      flushed = true
    })

    // 用一个宏任务给「只等 import」的旧实现充分的完成机会：它会在这一轮里把 flushed 置 true。
    // 新实现则被 gate 卡住，宏任务结束时仍未返回。用宏任务做「给足时间」而不是做标记，
    // 是因为 flush 本身只能等 Promise、等不到任何宏任务——标记必须放在 Promise 上。
    await new Promise<void>((resolve) => setTimeout(resolve, 0))
    expect(continuationCalled).toBe(true) // 续体已被调用
    expect(flushed).toBe(false) // 但 flush 必须仍在等它

    releaseGate()
    await flushedPromise
    expect(flushed).toBe(true)
  })

  it('flushAuthDynamicImports 重复调用幂等，无在途时立即返回', async () => {
    const { flushAuthDynamicImports } = await import('./auth')
    await flushAuthDynamicImports()
    await flushAuthDynamicImports()
  })

  /**
   * 回归（2026-10-19 评审）：**续体自身抛错**不得漏成 unhandled rejection。
   *
   * 原实现只吞了 `import()` 的 rejection（`.then(onFulfilled, () => undefined)`），
   * 但续体在 onFulfilled 里执行——它一旦抛错/返回 rejected，`tracked` 就 reject。而
   * `tracked` 被 `void tracked.finally(...)` 以不等待的方式消费，`.finally` 链上新生成的
   * rejected promise 无人接管 ⇒ unhandled rejection。CI 上的表现正是「全部用例通过但
   * 退出码非零」，很难归因。这里直接监听 process 的 unhandledRejection 来断言。
   */
  it('续体抛错不漏成 unhandled rejection', async () => {
    const { flushAuthDynamicImports, trackDynamicImport } = await import('./auth')

    const seen: unknown[] = []
    const onUnhandled = (reason: unknown) => seen.push(reason)
    process.on('unhandledRejection', onUnhandled)

    try {
      // 同步抛错的续体。
      void trackDynamicImport(Promise.resolve({ ok: true }), () => {
        throw new Error('续体同步抛错')
      })
      // 返回 rejected Promise 的续体。
      void trackDynamicImport(Promise.resolve({ ok: true }), async () => {
        throw new Error('续体异步抛错')
      })

      await flushAuthDynamicImports()
      // 让 finally 链与 unhandledRejection 的派发都跑完（它们在微任务之后）。
      await new Promise<void>((resolve) => setTimeout(resolve, 0))
    } finally {
      process.off('unhandledRejection', onUnhandled)
    }

    expect(seen, `不应出现 unhandled rejection，实际收到：${JSON.stringify(seen.map(String))}`).toEqual([])
  })
})
