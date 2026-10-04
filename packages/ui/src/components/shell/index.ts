/**
 * 导航骨架外壳（FR-496 阶段 4）：把「顶栏 + 资源侧栏 + 页面区」三件套收敛成唯一出处。
 *
 * 依据《资源优先工作区》原型（`.tmp/设计/`）实测：全局只有一条顶栏、一条资源侧栏，
 * 页面只管自己那一格的内容。阶段 3 的布局层负责「一页内部怎么摆」，
 * 本层负责「页与页之间共用的那副骨架」——两者边界不可混用。
 *
 * 标准用法：
 * ```tsx
 * <AppShell
 *   topbar={<TopNav brand={<Brand />} workspaces={workspaces} onWorkspaceChange={…} actions={…} onSearch={…} />}
 *   sidebar={
 *     <SideNav collapsed={collapsed}>
 *       <SideNavHead icon={<ServerIcon />} pill="资源视图">服务器运维</SideNavHead>
 *       <SideNavShortcuts>
 *         <SideNavRow icon={<LayoutDashboardIcon />} active>集群总览</SideNavRow>
 *       </SideNavShortcuts>
 *       <SideNavResource>
 *         <ResourceTree nodes={nodes} query={q} onQueryChange={setQ} />
 *       </SideNavResource>
 *       <SideNavBottom>
 *         <SideNavRow icon={<NetworkIcon />}>群组与拓扑</SideNavRow>
 *       </SideNavBottom>
 *     </SideNav>
 *   }
 * >
 *   <PageShell variant="fixed">…</PageShell>
 * </AppShell>
 * ```
 */
export { AppShell } from './AppShell'
export { TopNav, type TopNavWorkspace } from './TopNav'
export {
  SideNav,
  SideNavBottom,
  SideNavHead,
  SideNavResource,
  SideNavRow,
  SideNavShortcuts,
} from './SideNav'
export {
  ResourceTree,
  type ResourceInstance,
  type ResourceNode,
  type ResourceStatus,
} from './ResourceTree'
export { CommandPalette, type CommandPaletteItem } from './CommandPalette'
export {
  ObjectPageHeader,
  type ObjectBreadcrumb,
  type ObjectMetric,
  type ObjectTool,
  type ObjectTone,
} from './ObjectPageHeader'
