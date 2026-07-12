import { useIsFetching, useIsMutating } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router'

/**
 * 页眉顶部加载进度条（FR-243）。
 *
 * 每次路由切换走一次性进度条；React Query 正在请求/变更时切到循环加载态。
 * 本项目用 BrowserRouter，不依赖 data-router navigation state。
 */
export function TopLoadingBar() {
  const { key, pathname, search } = useLocation()
  const fetching = useIsFetching()
  const mutating = useIsMutating()
  const loading = fetching + mutating > 0
  const routeKey = `${key}:${pathname}${search}`
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
