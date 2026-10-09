import { useCallback } from 'react'
import { Link, useLocation, useNavigate } from 'react-router'
import type { WorkspaceSidebarProps } from '@/components/views/console/WorkspaceSidebar'
import { WorkspaceSidebar as WorkspaceSidebarView } from '@/components/views/console/WorkspaceSidebar'
import { useInstanceAggregate, useInstanceSearch } from '@/api/instances'
import { useNetworks } from '@/api/networks'
import { useNodes } from '@/api/nodes'
import { useConsoleStore } from '@/stores/console'
import { usePermissionsStore } from '@/stores/permissions'
import { useThemeStore } from '@/stores/theme'
import { useWorkspaceNavigation } from './use-workspace-navigation'
import ServerSelector from './ServerSelector'
import SidebarServerList from './SidebarServerList'

/**
 * 侧栏资源树随首批实例的加载条数上限。
 * 侧栏只用于定位、每节点默认只列 7 行（`ResourceTree` 的 limit），因此不必拉全集：
 * 节点计数取聚合的权威总数，超出这部分条数的实例由「查看其余 N 个实例」兜底。
 */
const RESOURCE_TREE_INSTANCE_LIMIT = 100

/**
 * 工作区侧栏的应用接线层（ADR-097）。
 *
 * 视图本体是受控视图（见 components/views）；本层注入：折叠态与权限快照（store）、工作区解析与当前路径
 * （路由）、资源区四路取数（节点 / 聚合计数 / 首屏实例 / 群组），以及两个自带取数的
 * 子组件（收藏选择器与常驻服务器列）。保留同路径的默认导出与零 props，调用点无需改动。
 */
export default function WorkspaceSidebar() {
  const { activeWorkspace } = useWorkspaceNavigation()
  const collapsed = useConsoleStore((s) => s.sidebarCollapsed)
  const { pathname } = useLocation()
  const navigate = useNavigate()

  const permNodes = usePermissionsStore((s) => s.nodes)
  const permAdmin = usePermissionsStore((s) => s.isPlatformAdmin)
  const permLoaded = usePermissionsStore((s) => s.loaded)
  const canReadInstances = !permLoaded || permAdmin || permNodes.has('instance.read')

  // 底部偏好块的主题切换（FR-164）：与 ThemeSwitcher 接线层同一数据源。
  const colorTheme = useThemeStore((s) => s.colorTheme)
  const setColorTheme = useThemeStore((s) => s.setColorTheme)
  const themeMode = useThemeStore((s) => s.theme)
  const setThemeMode = useThemeStore((s) => s.setTheme)

  const { data: aggregate } = useInstanceAggregate()
  const { data: nodes } = useNodes()
  const { data: networks } = useNetworks()
  // 无 instance.read 时（理论上已被段可见性挡住）不发实例请求，避免注定 403 的调用。
  const { data: search } = useInstanceSearch(
    { page: 1, pageSize: RESOURCE_TREE_INSTANCE_LIMIT, sort: 'name', order: 'asc' },
    canReadInstances,
  )

  // 注入面保持引用稳定，避免视图每次渲染都重建元素树。
  // 链接激活态：`end` 为真时精确匹配（根路径等），否则前缀匹配——与视图的 end 判定一致。
  const isActive = (to: string, end?: boolean) =>
    end ? pathname === to : pathname === to || pathname.startsWith(to + '/')
  const renderLink = useCallback<WorkspaceSidebarProps['renderLink']>(
    ({ to, end, className, ariaCurrent, ariaLabel, title, children }) => (
      <Link
        to={to}
        className={typeof className === 'function' ? className(isActive(to, end)) : className}
        aria-current={ariaCurrent}
        aria-label={ariaLabel}
        title={title}
      >
        {children}
      </Link>
    ),
    // isActive 依赖 pathname：路径变化时重建，保证回调式 class 拿到最新激活态。
    [pathname],
  )
  const onNavigate = useCallback((to: string) => navigate(to), [navigate])
  const renderServerSelector = useCallback(() => <ServerSelector />, [])
  const renderSidebarServerList = useCallback(() => <SidebarServerList />, [])

  return (
    <WorkspaceSidebarView
      collapsed={collapsed}
      activeWorkspace={activeWorkspace}
      data={{
        instanceTotal: aggregate?.total,
        nodes,
        byNode: aggregate?.byNode,
        instances: canReadInstances ? search?.items : undefined,
        networks,
      }}
      permissions={{ nodes: permNodes, isPlatformAdmin: permAdmin, loaded: permLoaded }}
      pathname={pathname}
      renderLink={renderLink}
      onNavigate={onNavigate}
      renderServerSelector={renderServerSelector}
      renderSidebarServerList={renderSidebarServerList}
      appVersion={__APP_VERSION__}
      themeSwitcher={{
        colorTheme,
        theme: themeMode,
        onColorThemeChange: setColorTheme,
        onThemeChange: setThemeMode,
      }}
    />
  )
}
