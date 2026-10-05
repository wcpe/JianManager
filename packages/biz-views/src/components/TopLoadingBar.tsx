import { useEffect, useRef, useState } from 'react'

/**
 * 页眉顶部加载进度条（FR-243）。
 *
 * 每次路由切换走一次性进度条；有在途请求/变更时切成循环加载态。
 *
 * 【受控化】原实现自行调用 `useLocation`（react-router）与 `useIsFetching`/`useIsMutating`
 * （react-query），因而无法脱离主控台的运行时复用。现改为由外壳注入两项：
 * - `routeKey` —— 路由身份，变化即触发一次路由进度；
 * - `pendingCount` —— 在途请求/变更数，>0 即忙碌。
 * 三段状态机（route → busy → done）、两处延迟（路由 720ms 收尾、忙碌 180ms 起显、
 * done 后 240ms 隐去）与全部 `data-*` 钩子均**逐行保留**，行为未变。
 *
 * 为什么延迟参数留在组件内而不是也提为 props：它们是这个进度条自身的动效规格
 * （防闪烁阈值），不是外部数据；外壳关心的是「忙不忙」，不是「忙多久才显示」。
 */
export interface TopLoadingBarProps {
  /**
   * 路由身份。任何变化都触发一次路由切换进度。
   * 外壳拼装建议：`${location.key}:${location.pathname}${location.search}`
   * ——必须包含 key，否则同路径重复点击不会产生变化。
   */
  routeKey: string
  /**
   * 在途的请求 / 变更数（>0 视为忙碌）。
   * 外壳从数据层读取后注入（如 `useIsFetching() + useIsMutating()`）。
   */
  pendingCount: number
}

export function TopLoadingBar({ routeKey, pendingCount }: TopLoadingBarProps) {
  const loading = pendingCount > 0
  const [visible, setVisible] = useState(false)
  const [mode, setMode] = useState<'route' | 'busy' | 'done'>('route')
  const mounted = useRef(false)
  const busyVisible = useRef(false)

  useEffect(() => {
    if (!mounted.current) {
      mounted.current = true
      return
    }

    setMode('route')
    setVisible(true)
    const timer = window.setTimeout(() => {
      if (!busyVisible.current) setVisible(false)
    }, 720)
    return () => window.clearTimeout(timer)
  }, [routeKey])

  useEffect(() => {
    let delayTimer = 0

    if (loading) {
      delayTimer = window.setTimeout(() => {
        busyVisible.current = true
        setMode('busy')
        setVisible(true)
      }, 180)
    } else if (busyVisible.current) {
      busyVisible.current = false
      setMode('done')
    }

    return () => {
      window.clearTimeout(delayTimer)
    }
  }, [loading])

  useEffect(() => {
    if (!visible || mode !== 'done') return

    const timer = window.setTimeout(() => setVisible(false), 240)
    return () => window.clearTimeout(timer)
  }, [mode, visible])

  return (
    <div
      data-slot="top-loading-track"
      data-testid="top-loading-track"
      data-loading={String(loading)}
      data-visible={String(visible)}
      aria-hidden="true"
      className="jm-top-loading-track pointer-events-none fixed inset-x-0 top-0 z-[80] h-[3px] overflow-hidden"
    >
      <div
        key={`${routeKey}:${mode}`}
        data-testid="top-loading-bar"
        data-loading={String(loading)}
        data-visible={String(visible)}
        data-mode={mode}
        className="jm-top-loading-bar h-full"
      />
    </div>
  )
}
