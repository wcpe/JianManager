/**
 * 导航原语层（原 `shell/`，FR-502 阶段 4）：页面之间共用的顶栏与侧栏构件。
 *
 * 本目录只放**能在不同页面里重复摆放的导航原语**——顶栏 `TopNav`、资源侧栏
 * `SideNav` 及其分段（`SideNavHead` / `SideNavShortcuts` / `SideNavResource` /
 * `SideNavBottom`）、对象页头 `ObjectPageHeader`、命令面板 `CommandPalette`。
 * 它不是"整体应用外壳"：不提供页面骨架、不接管路由、不规定页面内部怎么摆。
 *
 * 两层边界：阶段 3 的布局层（`components/layout`）负责"一页内部怎么摆"，
 * 本层负责"页与页之间共用的那几条栏"——两者不可混用，页面骨架不进本层。
 *
 * 标准用法：
 * ```tsx
 * <TopNav brand={<Brand />} workspaces={workspaces} onWorkspaceChange={…} actions={…} onSearch={…} />
 *
 * <SideNav collapsed={collapsed}>
 *   <SideNavHead icon={<ServerIcon />} pill="资源视图">服务器运维</SideNavHead>
 *   <SideNavShortcuts>
 *     <SideNavRow icon={<LayoutDashboardIcon />} active>集群总览</SideNavRow>
 *   </SideNavShortcuts>
 *   <SideNavResource>{/* 资源列表等滚动内容 *\/}</SideNavResource>
 *   <SideNavBottom>
 *     <SideNavRow icon={<NetworkIcon />}>群组与拓扑</SideNavRow>
 *   </SideNavBottom>
 * </SideNav>
 * ```
 */
export { TopNav, type TopNavWorkspace } from './TopNav'
export {
  SideNav,
  SideNavBottom,
  SideNavHead,
  SideNavResource,
  SideNavRow,
  SideNavShortcuts,
} from './SideNav'
export { SidebarShell, type SidebarShellProps } from './SidebarShell'
export { SidebarGroup, type SidebarGroupProps } from './SidebarGroup'
export { SidebarFooter, type SidebarFooterProps } from './SidebarFooter'
export { SidebarRail, type SidebarRailProps } from './SidebarRail'
export { SidebarSection, type SidebarSectionProps } from './SidebarSection'
export { CommandPalette, type CommandPaletteItem } from './CommandPalette'
export {
  ObjectPageHeader,
  type ObjectBreadcrumb,
  type ObjectMetric,
  type ObjectTool,
  type ObjectTone,
} from './ObjectPageHeader'
