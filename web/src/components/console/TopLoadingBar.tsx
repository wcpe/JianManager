import { useLocation } from 'react-router'

/**
 * 页眉顶部加载进度条（FR-243）。
 *
 * 每次路由切换，在内容区顶部走一条一次性进度条（0→100% 后淡出），给「切页」一个统一的加载反馈。
 * 用 `key={pathname}` 让内层条在每次导航时重挂载、重放纯 CSS 动画——无 JS 状态、无副作用，
 * 不触 `react-hooks/set-state-in-effect`。本项目用 `BrowserRouter`，由 `location` 驱动。
 */
export function TopLoadingBar() {
  const { pathname } = useLocation()
  return (
    <div
      aria-hidden="true"
      className="pointer-events-none absolute inset-x-0 top-0 z-50 h-0.5 overflow-hidden"
    >
      <div key={pathname} data-testid="top-loading-bar" className="h-full bg-primary animate-top-progress" />
    </div>
  )
}
