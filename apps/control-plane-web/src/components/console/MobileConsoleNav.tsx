import { NavLink, useLocation } from 'react-router'
import { MobileConsoleNav as MobileConsoleNavView } from '@/components/views/console/MobileConsoleNav'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import { navGroupsForPermissions, navGroupsForRole } from '@/lib/nav-config'

/**
 * 手机端底部导航的应用接线层（ADR-097）。
 *
 * 视图本体是受控视图（见 components/views）；本层注入按权限/角色裁剪的导航分组、当前路径与路由链接，
 * 保留同路径的默认导出与零 props，调用点无需改动。
 */
export default function MobileConsoleNav() {
  const role = useAuthStore((s) => s.role)
  const permNodes = usePermissionsStore((s) => s.nodes)
  const permAdmin = usePermissionsStore((s) => s.isPlatformAdmin)
  const permLoaded = usePermissionsStore((s) => s.loaded)
  const groups = permLoaded ? navGroupsForPermissions(permNodes, permAdmin) : navGroupsForRole(role)
  const { pathname } = useLocation()

  return (
    <MobileConsoleNavView
      groups={groups}
      pathname={pathname}
      renderLink={({ to, end, className, ariaCurrent, ariaLabel, title, onClick, children }) => (
        <NavLink
          to={to}
          end={end}
          className={typeof className === 'function' ? ({ isActive }) => className(isActive) : className}
          aria-current={ariaCurrent}
          aria-label={ariaLabel}
          title={title}
          onClick={onClick}
        >
          {children}
        </NavLink>
      )}
    />
  )
}
