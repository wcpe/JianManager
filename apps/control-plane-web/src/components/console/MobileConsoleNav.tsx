import { NavLink, useLocation } from 'react-router'
import { MobileConsoleNav as MobileConsoleNavView } from '@/components/views/console/MobileConsoleNav'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import { workspacesForPermissions, workspacesForRole } from '@/lib/console/workspace-navigation'

/**
 * 手机端底部导航的应用接线层（ADR-103）。
 *
 * 视图本体是受控视图（见 components/views）；本层注入按权限/角色裁剪的**工作区**列表
 * （FR-507 阶段 6：底部格子一格一个工作区，与顶栏 / 侧栏同一份真源）、当前路径与路由链接，
 * 保留同路径的默认导出与零 props，调用点无需改动。
 */
export default function MobileConsoleNav() {
  const role = useAuthStore((s) => s.role)
  const permNodes = usePermissionsStore((s) => s.nodes)
  const permAdmin = usePermissionsStore((s) => s.isPlatformAdmin)
  const permLoaded = usePermissionsStore((s) => s.loaded)
  const workspaces = permLoaded ? workspacesForPermissions(permNodes, permAdmin) : workspacesForRole(role)
  const { pathname } = useLocation()

  return (
    <MobileConsoleNavView
      workspaces={workspaces}
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
