import { useCallback, useMemo, useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router'
import type { WorkspaceSidebarProps } from '@/components/views/console/WorkspaceSidebar'
import { WorkspaceSidebar as WorkspaceSidebarView } from '@/components/views/console/WorkspaceSidebar'
import { SidebarResourcePanel } from '@/components/views/console/SidebarResourcePanel'
import { useInstanceAggregate, useInstanceSearch } from '@/api/instances'
import { useInstanceGroups } from '@/api/instanceGroups'
import { useNodes } from '@/api/nodes'
import { useTopology } from '@/api/topology'
import { useConsoleStore } from '@/stores/console'
import { usePermissionsStore } from '@/stores/permissions'
import { useThemeStore } from '@/stores/theme'
import { useDebounced } from '@/lib/hooks/use-debounced'
import { recordRecentServer, type StorableInstance } from '@/lib/console/server-selection'
import { buildKeyMap } from '@/lib/instances/instance-grouping'
import { useWorkspaceNavigation } from './use-workspace-navigation'
import i18n, { changeLanguage } from '@/i18n'

/** 
 * 资源面板的实例窗口上限。
 * 侧栏是**有界窗口**（服务端 `q` 搜索 + 截断提示），不是千级全量列举；
 * 千级浏览在 `/instances` 页（虚拟化 + 无限分页）里做。
 */
const RESOURCE_PANEL_INSTANCE_LIMIT = 100

/** 资源搜索防抖（键入落定后才下发服务端 q）。 */
const RESOURCE_SEARCH_DEBOUNCE_MS = 300

/** 
 * 工作区侧栏的应用接线层（ADR-103）。
 *
 * 视图本体是受控视图（见 components/views）；本层注入：折叠态与权限快照（store）、工作区解析与当前路径
 * （路由）、资源面板数据（节点 / 聚合计数 / 实例搜索窗口 / 拓扑成员映射）与资源面板组件本身。
 * 保留同路径的默认导出与零 props，调用点无需改动。
 */
export default function WorkspaceSidebar() {
  const { activeWorkspace } = useWorkspaceNavigation()
  const collapsed = useConsoleStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useConsoleStore((s) => s.toggleSidebar)
  const { pathname } = useLocation()
  const navigate = useNavigate()

  const permNodes = usePermissionsStore((s) => s.nodes)
  const permAdmin = usePermissionsStore((s) => s.isPlatformAdmin)
  const permLoaded = usePermissionsStore((s) => s.loaded)
  const canReadInstances = !permLoaded || permAdmin || permNodes.has('instance.read')

  // 底部偏好块的主题切换：与 ThemeSwitcher 接线层同一数据源。
  const colorTheme = useThemeStore((s) => s.colorTheme)
  const setColorTheme = useThemeStore((s) => s.setColorTheme)
  const themeMode = useThemeStore((s) => s.theme)
  const setThemeMode = useThemeStore((s) => s.setTheme)

  // 资源面板的搜索：输入态即时反馈，落定值经防抖后才进服务端 q（护栏：千级走服务端搜索）。
  const [resourceQuery, setResourceQuery] = useState('')
  const debouncedQuery = useDebounced(resourceQuery, RESOURCE_SEARCH_DEBOUNCE_MS)

  const { data: aggregate } = useInstanceAggregate()
  const { data: nodes } = useNodes()
  const { data: topology } = useTopology()

  /** 
   * 侧栏实例窗口：**服务端搜索**（有界前 N 台 + 截断提示），不做本地全量列举。
   *
   * 分组维度不再走 `groupId` 参数：侧栏的「分组方式」是在**已加载窗口内**按维度分段展示
   * （与列表分组同口径），而不是把窗口替换成某个分组的子集。
   */
  const { data: search } = useInstanceSearch(
    {
      page: 1,
      pageSize: RESOURCE_PANEL_INSTANCE_LIMIT,
      sort: 'name',
      order: 'asc',
      ...(debouncedQuery ? { q: debouncedQuery } : {}),
    },
    canReadInstances,
  )

  // 按群组的方式：实例 id → 群组名（拓扑成员映射；与实例列表页「按群组」同一数据源）。
  const networkKeyOf = useMemo(() => {
    const groups = (topology?.networks ?? []).map((n) => ({ key: n.name, instanceIds: n.memberInstanceIds }))
    return buildKeyMap(groups)
  }, [topology])

  // 按分组的方式：后端组织分组树（`GET /instance-groups`，一次请求、无 per-group N+1）。
  const { data: groupTree } = useInstanceGroups({ enabled: canReadInstances })

  // 指标取数不在这一层：面板自己按**虚拟可见行**取（见 `SidebarResourcePanel` 的
  // `visibleInstances`）。此前在这里按整个窗口取前 50 台，导致第 51 台以后永远没指标。

  const openFromPanel = useCallback(
    (inst: StorableInstance) => {
      // 从侧栏进入同样计入「最近打开」（与选择器/直接路由同一入口）。
      recordRecentServer(inst)
      navigate(`/instances/${inst.id}`)
    },
    [navigate],
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
  const renderResourcePanel = useCallback(
    () => (
      <SidebarResourcePanel
        instances={canReadInstances ? (search?.items ?? []) : []}
        nodes={nodes ?? []}
        query={resourceQuery}
        onQueryChange={setResourceQuery}
        loadedCount={search?.items?.length ?? 0}
        totalCount={search?.total}
        onOpen={openFromPanel}
        networkKeyOf={networkKeyOf}
        groupTree={groupTree ?? []}
      />
    ),
    [canReadInstances, search, nodes, resourceQuery, openFromPanel, networkKeyOf, groupTree],
  )

  return (
    <WorkspaceSidebarView
      collapsed={collapsed}
      activeWorkspace={activeWorkspace}
      data={{
        instanceTotal: aggregate?.total,
        nodes,
      }}
      permissions={{ nodes: permNodes, isPlatformAdmin: permAdmin, loaded: permLoaded }}
      pathname={pathname}
      renderLink={renderLink}
      onNavigate={onNavigate}
      renderResourcePanel={renderResourcePanel}
      appVersion={__APP_VERSION__}
      themeSwitcher={{
        colorTheme,
        theme: themeMode,
        onColorThemeChange: setColorTheme,
        onThemeChange: setThemeMode,
      }}
      // 语言切换：与 `/settings` 的「外观」共用同一个 `changeLanguage`（写 localStorage +
      // 改 `<html lang>` + 切 i18n），不另造一份状态。
      language={{ current: i18n.language, onChange: (lng) => changeLanguage(lng as 'zh' | 'en') }}
      // 底部折叠/展开开关：顶栏那个在折叠态会卸载，这里必须是常驻出口。
      onToggleSidebar={toggleSidebar}
    />
  )
}
