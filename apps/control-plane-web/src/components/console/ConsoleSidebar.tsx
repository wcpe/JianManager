import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useLocation } from 'react-router'
import { ConsoleSidebar as ConsoleSidebarView } from '@/components/views/console/ConsoleSidebar'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import { useConsoleStore } from '@/stores/console'
import { useThemeStore } from '@/stores/theme'
import { changeLanguage } from '@/i18n'
import { navGroupsForPermissions, navGroupsForRole } from '@/lib/nav-config'
import ServerSelector from './ServerSelector'
import SidebarServerList from './SidebarServerList'

/**
 * 控制台侧栏的应用接线层（ADR-097）。
 *
 * 视图本体是受控视图（见 components/views）；本层注入：导航分组的权限/角色裁剪、折叠态与分组折叠
 * （store）、当前路径（路由）、主题与语言切换（store / i18n），以及两个自带取数的
 * 子组件（选择器与常驻服务器列）。保留同路径的默认导出与零 props，调用点无需改动。
 */
export default function ConsoleSidebar() {
  const role = useAuthStore((s) => s.role)
  const permNodes = usePermissionsStore((s) => s.nodes)
  const permAdmin = usePermissionsStore((s) => s.isPlatformAdmin)
  const permLoaded = usePermissionsStore((s) => s.loaded)
  // 权限未加载时按角色兜底裁剪，避免首帧闪出无权入口。
  const groups = useMemo(() => {
    if (permLoaded) return navGroupsForPermissions(permNodes, permAdmin)
    return navGroupsForRole(role)
  }, [permLoaded, permNodes, permAdmin, role])

  const collapsed = useConsoleStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useConsoleStore((s) => s.toggleSidebar)
  const collapsedGroups = useConsoleStore((s) => s.collapsedGroups)
  const toggleGroup = useConsoleStore((s) => s.toggleGroup)

  const { pathname } = useLocation()
  const { i18n } = useTranslation()
  // 链接激活态：`end` 为真时精确匹配（根路径等），否则前缀匹配——与视图的 end 判定一致。
  const isActive = (to: string, end?: boolean) =>
    end ? pathname === to : pathname === to || pathname.startsWith(to + '/')
  const colorTheme = useThemeStore((s) => s.colorTheme)
  const setColorTheme = useThemeStore((s) => s.setColorTheme)
  const themeMode = useThemeStore((s) => s.theme)
  const setThemeMode = useThemeStore((s) => s.setTheme)

  return (
    <ConsoleSidebarView
      groups={groups}
      collapsed={collapsed}
      onToggleSidebar={toggleSidebar}
      collapsedGroups={collapsedGroups}
      onToggleGroup={toggleGroup}
      pathname={pathname}
      renderLink={({ to, end, className, ariaCurrent, ariaLabel, title, dataActive, children }) => (
        <Link
          to={to}
          className={typeof className === 'function' ? className(isActive(to, end)) : className}
          aria-current={ariaCurrent}
          aria-label={ariaLabel}
          title={title}
          data-active={dataActive}
        >
          {children}
        </Link>
      )}
      renderServerSelector={() => <ServerSelector />}
      renderSidebarServerList={() => <SidebarServerList />}
      appVersion={__APP_VERSION__}
      themeSwitcher={{
        colorTheme,
        theme: themeMode,
        onColorThemeChange: setColorTheme,
        onThemeChange: setThemeMode,
      }}
      language={{ current: i18n.language, onChange: (lng) => changeLanguage(lng as 'en' | 'zh') }}
    />
  )
}
