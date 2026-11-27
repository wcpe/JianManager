import { useCallback, useRef, useState } from 'react'

/**
 * 防重复提交（同步拦截 + 渲染可读的在途态）。
 *
 * 【为什么不用 `const [busy, setBusy] = useState(false)` 自己写】
 * setState 是异步生效的：在 React 完成这次渲染之前，闭包里读到的 `busy` 仍是旧值。
 * 只要两次调用落在同一个渲染周期内（同一批事件、或宏任务里连续调用），
 * 两次都会读到 false，于是两次请求都发出去。
 * 对「安装运行时 / 下发资产 / 创建快照」这类**异步任务**后果很实在——
 * 不是多一次幂等查询，而是真的起两个下载任务、两份磁盘占用。
 *
 * 【一个测量上的诚实说明】这条推理在真实浏览器里成立，但**在 jsdom/RTL 里很难复现**：
 * 实测连 fireEvent 同步连发两次 click，React 也会在两次之间刷新状态，
 * 所以组件级测试对「ref vs setState」没有鉴别力（我做过对照实验，两者都只触发一次）。
 * 因此本钩子的区分性由 use-submit-guard.dom.test.tsx 在**钩子层**直接断言：
 * 那里可以不 await 地连调两次 run()，那才是两个调用真正同 tick 的场景。
 *
 * 本钩子用 ref 做**同步**闸门（第一次点击立刻关上，第二次进来读到的是关的），
 * 同时把同一个状态镜像进 state，让 UI 能拿到在途态去禁用按钮、转圈、改文案。
 *
 * 【返回值设计】`run` 会**返回底层函数的返回值**；被拦截时返回 `undefined`。
 * 调用方靠这个区分「这次真的执行了吗」，不必再自己维护一套标志。
 *
 * 【为什么不顺手做 timeout 兜底】把「多久算超时」写死在通用钩子里是错的：
 * 一次本机 rm 和一次 20 分钟的跨网下载没有共同的时间尺度。在途态由底层
 * mutation 的 resolve/reject 释放；真要超时，交还给发起方（它知道这个动作有多重）。
 */
export function useSubmitGuard<Args extends unknown[], R>(
  fn: (...args: Args) => Promise<R>,
): {
  /** 是否有提交在途（渲染可读，用于 disabled / Spinner / 文案切换）。 */
  pending: boolean
  /**
   * 受保护的调用。已有提交在途时**同步**丢弃本次调用并返回 undefined。
   */
  run: (...args: Args) => Promise<R | undefined>
  /** 重置在途态（极少用：仅在需要强制解锁时，如外部取消了整个流程）。 */
  reset: () => void
} {
  // ref 是同步闸门：它在本轮事件处理内立刻生效，不依赖下一次渲染。
  const inFlight = useRef(false)
  // state 是同一事实的渲染镜像：用于把按钮置灰、显示 Spinner。
  const [pending, setPending] = useState(false)

  const run = useCallback(
    async (...args: Args): Promise<R | undefined> => {
      // 同步检查 + 同步置位：中间没有 await，所以同 tick 的第二次点击一定看到 true。
      if (inFlight.current) return undefined
      inFlight.current = true
      setPending(true)
      try {
        return await fn(...args)
      } finally {
        // 无论成功/失败/抛错都释放：卡死在「永远 pending」比漏一次防重更难排查。
        inFlight.current = false
        setPending(false)
      }
    },
    [fn],
  )

  const reset = useCallback(() => {
    inFlight.current = false
    setPending(false)
  }, [])

  return { pending, run, reset }
}
