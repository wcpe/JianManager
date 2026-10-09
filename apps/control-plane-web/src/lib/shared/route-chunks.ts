/**
 * 路由 chunk 表（FR-496 阶段 6 补丁）：**路由表与预取的唯一真源**。
 *
 * 【为什么要有这张表】
 * 切页等待的大头是目标页 JS chunk 的下载（构建产物里 `InstancesPage-*.js` 108 kB、
 * `NodesPage-*.js` 96 kB），而 `React.lazy` 只在首次渲染时才发起 import——用户点下去才开始下载。
 * 想把下载提前到「用户还在犹豫」的那段时间，就必须能在**不动路由表**的前提下按路径找到同一个
 * import。若在 `Workspace.tsx` 与预取模块各写一份 `import('@/pages/X')`，两处一旦漂移
 * （改名、换目录、漏加路由），预取会静默下载另一个 chunk，白花流量且毫无效果。
 * 因此：`Workspace.tsx` 的 `lazy()` 与预取器都从本表取加载器。
 *
 * 【键的形态】
 * 与 `Workspace.tsx` 的 `<Route path>` 同形（动态段写成 `:id`），不含前导 query。
 * 具体路径（`/instances/42`）由 {@link matchRouteKey} 归一到某个键。
 *
 * 【为什么不用 React Router 自带的预取】
 * `<Link prefetch>` 与 `route.lazy` 只对 data router（`createBrowserRouter`）生效；本应用是
 * `BrowserRouter` + `React.lazy` 的路由表（`Workspace.tsx`），迁移路由形态的收益远小于风险。
 * 手写在这里等价的「同 specifier 动态 import」：Vite dev 下同一 URL、构建产物下同一 chunk，
 * 因而与 `lazy()` 命中的是同一个模块，预取过就等于点下去是同步的。
 */

/** 路由表键 → chunk 加载器。值写成箭头函数（模块级只建一次），调用方才不会每次渲染新建 import。 */
export const ROUTE_CHUNKS = {
  '/': () => import('@/pages/OverviewPage'),
  '/monitor': () => import('@/pages/MonitoringPage'),
  '/nodes': () => import('@/pages/NodesPage'),
  '/instances': () => import('@/pages/InstancesPage'),
  '/instances/new': () => import('@/pages/InstanceWizardPage'),
  '/instances/:id': () => import('@/pages/InstanceDetailPage'),
  '/instances/:id/files': () => import('@/pages/InstanceFilesPage'),
  '/config-baselines': () => import('@/pages/ConfigBaselinesPage'),
  '/networks': () => import('@/pages/NetworksPage'),
  // 拓扑与分组管理同属 NetworkPage（同 chunk），但两个入口都要能被预取，故两个键指向同一 import。
  '/networks/topology': () => import('@/pages/NetworksPage'),
  '/players': () => import('@/pages/PlayersPage'),
  '/bots': () => import('@/pages/BotsPage'),
  '/bots/sessions/:id': () => import('@/pages/BotLoadSessionPage'),
  '/alerts': () => import('@/pages/AlertsPage'),
  '/users': () => import('@/pages/UsersPage'),
  '/permissions': () => import('@/pages/PermissionsPage'),
  '/groups': () => import('@/pages/GroupsPage'),
  '/templates': () => import('@/pages/TemplatesPage'),
  '/runtime-assets': () => import('@/pages/RuntimeAssetsPage'),
  '/schedules': () => import('@/pages/SchedulesPage'),
  '/backups': () => import('@/pages/BackupsPage'),
  '/backup-storages': () => import('@/pages/BackupStoragesPage'),
  '/artifact-storages': () => import('@/pages/ArtifactStoragesPage'),
  '/artifact-versions': () => import('@/pages/ArtifactVersionsPage'),
  '/audit': () => import('@/pages/AuditPage'),
  '/tasks': () => import('@/pages/TasksPage'),
  '/notifications': () => import('@/pages/NotificationCenterPage'),
  '/client-channels': () => import('@/pages/ClientChannelsPage'),
  '/client-channels/:id/publish': () => import('@/pages/ClientPublishPage'),
  '/client-dist-ops': () => import('@/pages/ProtectionCenterPage'),
  // 旧链接重定向页：两条旧路由共用一个组件，也就共用一个 chunk。
  '/client-dist-security': () => import('@/pages/ClientDistRedirect'),
  '/client-dist-monitor': () => import('@/pages/ClientDistRedirect'),
  '/logs': () => import('@/pages/LogsPage'),
  '/statistics': () => import('@/pages/StatisticsPage'),
  '/storage': () => import('@/pages/StoragePage'),
  '/settings': () => import('@/pages/SettingsPage'),
  '/database': () => import('@/pages/DatabasePage'),
  '/system-update': () => import('@/pages/SystemUpdatePage'),
  '/agent-tokens': () => import('@/pages/AgentTokensPage'),
  '/mcp-activity': () => import('@/pages/McpActivityPage'),
  '/agent-call-logs': () => import('@/pages/AgentCallLogsPage'),
  '/licenses': () => import('@/pages/LicensesPage'),
  // 全幅工具页（Workspace 里走独立的整屏分支，不在带内边距的工作区壳内）。
  '/super': () => import('@/components/console/SuperWorkbenchPage'),
  '/director': () => import('@/components/console/DirectorConsolePage'),
} satisfies Record<string, () => Promise<unknown>>

/** 宽类型视图：运行期按字符串查表时用（键的精确类型仍由上面保留给 `lazy()`）。 */
export const ROUTE_LOADERS: Record<string, () => Promise<unknown>> = ROUTE_CHUNKS

/** 路由表键集合（导航覆盖度自检用）。 */
export const ROUTE_KEYS: readonly string[] = Object.keys(ROUTE_CHUNKS)

/**
 * 把具体路径归一到路由表键：先精确命中，再逐个动态段换成 `:id` 重试。
 *
 * 逐段替换足以覆盖本项目全部动态路由（`/instances/42`、`/instances/42/files`、
 * `/bots/sessions/abc`、`/client-channels/x/publish`），比引入完整路径匹配器便宜得多。
 * 未命中返回 `null`——预取是尽力而为，未知路径必须静默忽略而不是抛错。
 */
export function matchRouteKey(path: string): string | null {
  if (path in ROUTE_CHUNKS) return path
  const segments = path.split('/').filter(Boolean)
  for (let index = 0; index < segments.length; index += 1) {
    const candidate = `/${segments.map((segment, at) => (at === index ? ':id' : segment)).join('/')}`
    if (candidate in ROUTE_CHUNKS) return candidate
  }
  return null
}
