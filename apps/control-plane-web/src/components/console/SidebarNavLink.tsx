import { NavLink } from 'react-router'
import { SidebarNavLink as SidebarNavLinkView } from '@/components/views/console/SidebarNavLink'
import type { NavEntry } from '@/lib/shared/nav-config'
import { useRouteIntentPrefetch } from '@/lib/shared/route-prefetch'

// 导航项类型已收敛到包内 nav-config（单一来源）；此处转出以兼容既有 import 点。
export type { NavEntry }

/**
 * 侧栏导航链接的应用接线层（ADR-097）。
 *
 * 视图本体见 components/views（包内不依赖 react-router）；本层把 react-router 的 NavLink
 * 与悬停预取处理器作为渲染函数注入，保留同路径默认导出与同一套 props。
 */
export default function SidebarNavLink({ to, labelKey, icon, nested = false }: NavEntry & { nested?: boolean }) {
  // FR-496 阶段 6 补丁：悬停/聚焦即预取目标页 chunk，把下载提前到点击之前（幂等，见 route-prefetch）。
  const prefetchHandlers = useRouteIntentPrefetch(to)

  return (
    <SidebarNavLinkView
      to={to}
      labelKey={labelKey}
      icon={icon}
      nested={nested}
      renderLink={({ to: linkTo, end, className, children }) => (
        <NavLink
          to={linkTo}
          end={end}
          {...prefetchHandlers}
          className={typeof className === 'function' ? ({ isActive }) => className(isActive) : className}
        >
          {children}
        </NavLink>
      )}
    />
  )
}
