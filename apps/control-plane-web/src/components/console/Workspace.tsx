import { Routes, Route, Navigate, useLocation } from 'react-router'
import { Suspense, lazy, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { PageSkeleton } from '@jianmanager/ui/components/layout'
import { useAuthStore } from '@/stores/auth'
import { ROUTE_CHUNKS } from '@/lib/shared/route-chunks'
import WorkspaceEmpty from '@/components/views/console/WorkspaceEmpty'

/**
 * 重定向到新路径，但**保留原有查询参数**。
 *
 * 为什么不能直接用 `<Navigate to="/monitor" replace />`：`to` 里没有 query，
 * 重定向会把原 URL 的 `?instance=<uuid>` / `?node=<uuid>` 一起丢掉。
 * 概览页「Top 实例 / Top 受管进程 / 节点归因」的下钻链接正是靠这些参数定位到具体实体——
 * 参数一丢，用户点进去看到的是全平台监控，而不是他点的那台（实测确认过）。
 *
 * 保留 query 也让外部收藏夹/文档里的旧链接继续有效（本组件的存在意义）。
 */
function RedirectKeepQuery({ to }: { to: string }) {
  const location = useLocation()
  return <Navigate to={`${to}${location.search}`} replace />
}

/**
 * 路由表的懒加载声明（FR-502 阶段 6 补丁）：加载器一律从 `@/lib/route-chunks` 取，
 * 不再各写一份 `import('@/pages/X')`——预取器查的就是同一张表，两处同源才能保证
 * 「hover 预取过的 chunk」与「点下去要等的 chunk」是同一个（否则预取会静默失效）。
 */
const OverviewPage = lazy(ROUTE_CHUNKS['/'])
const AssistantHomePage = lazy(ROUTE_CHUNKS['/assistant-home'])
const MonitoringPage = lazy(ROUTE_CHUNKS['/monitor'])
const NodesPage = lazy(ROUTE_CHUNKS['/nodes'])
const InstancesPage = lazy(ROUTE_CHUNKS['/instances'])
const InstanceDetailPage = lazy(ROUTE_CHUNKS['/instances/:id'])
const InstanceFilesPage = lazy(ROUTE_CHUNKS['/instances/:id/files'])
const InstanceWizardPage = lazy(ROUTE_CHUNKS['/instances/new'])
const ConfigBaselinesPage = lazy(ROUTE_CHUNKS['/config-baselines'])
const NetworksPage = lazy(ROUTE_CHUNKS['/networks'])
const PlayersPage = lazy(ROUTE_CHUNKS['/players'])
const UsersPage = lazy(ROUTE_CHUNKS['/users'])
const PermissionsPage = lazy(ROUTE_CHUNKS['/permissions'])
const GroupsPage = lazy(ROUTE_CHUNKS['/groups'])
const SchedulesPage = lazy(ROUTE_CHUNKS['/schedules'])
const BackupsPage = lazy(ROUTE_CHUNKS['/backups'])
const BackupStoragesPage = lazy(ROUTE_CHUNKS['/backup-storages'])
const ArtifactStoragesPage = lazy(ROUTE_CHUNKS['/artifact-storages'])
const ArtifactVersionsPage = lazy(ROUTE_CHUNKS['/artifact-versions'])
const BotsPage = lazy(ROUTE_CHUNKS['/bots'])
const BotLoadSessionPage = lazy(ROUTE_CHUNKS['/bots/sessions/:id'])
const AuditPage = lazy(ROUTE_CHUNKS['/audit'])
const TemplatesPage = lazy(ROUTE_CHUNKS['/templates'])
const RuntimeAssetsPage = lazy(ROUTE_CHUNKS['/runtime-assets'])
const AlertsPage = lazy(ROUTE_CHUNKS['/alerts'])
const SettingsPage = lazy(ROUTE_CHUNKS['/settings'])
const AssistantSettingsPage = lazy(ROUTE_CHUNKS['/settings/ai'])
const StoragePage = lazy(ROUTE_CHUNKS['/storage'])
const LogsPage = lazy(ROUTE_CHUNKS['/logs'])
const StatisticsPage = lazy(ROUTE_CHUNKS['/statistics'])
const ClientChannelsPage = lazy(ROUTE_CHUNKS['/client-channels'])
const ProtectionCenterPage = lazy(ROUTE_CHUNKS['/client-dist-ops'])
const ClientDistRedirect = lazy(ROUTE_CHUNKS['/client-dist-security'])
const ClientPublishPage = lazy(ROUTE_CHUNKS['/client-channels/:id/publish'])
const DatabasePage = lazy(ROUTE_CHUNKS['/database'])
const SystemUpdatePage = lazy(ROUTE_CHUNKS['/system-update'])
const AgentTokensPage = lazy(ROUTE_CHUNKS['/agent-tokens'])
const McpActivityPage = lazy(ROUTE_CHUNKS['/mcp-activity'])
const AgentCallLogsPage = lazy(ROUTE_CHUNKS['/agent-call-logs'])
const LicensesPage = lazy(ROUTE_CHUNKS['/licenses'])
const TasksPage = lazy(ROUTE_CHUNKS['/tasks'])
const NotificationCenterPage = lazy(ROUTE_CHUNKS['/notifications'])
const SuperWorkbenchPage = lazy(ROUTE_CHUNKS['/super'])
const DirectorConsolePage = lazy(ROUTE_CHUNKS['/director'])

/** 平台管理员角色值（与后端 model.RolePlatformAdmin 对齐）。 */
const ROLE_PLATFORM_ADMIN = 10

/**
 * 管理页路由级角色守卫：此前仅靠侧栏按角色隐藏入口，URL 直达仍可进页。
 * 非平台管理员一律重定向首页；页内既有角色兜底（如 DatabasePage/SystemUpdatePage）保留作纵深防御，
 * 后端 RBAC 仍是最终强制。
 */
function RequirePlatformAdmin({ children }: { children: ReactNode }) {
  const role = useAuthStore((s) => s.role)
  if (role !== ROLE_PLATFORM_ADMIN) return <Navigate to="/" replace />
  return <>{children}</>
}
/**
 * 运维控制台右侧工作区（ADR-009 / FR-037 / FR-039 / FR-166 / FR-167 / FR-269）。
 * 打开服务器时默认渲染固定分区的服务器统一控制台；可组合卡片画布保留为高级拼屏能力。
 * 否则按路由渲染对应页面，既有页面不变。同一时刻仅一个打开的服务器上下文。
 * 跨实例超级工作台（FR-167）走 `/super`，全幅渲染（自带左侧实例库 + 画布，无统一内边距）。
 * 工作区导播台（FR-168）走 `/director`，同为全幅（场景舞台 + 缩略图条，多预设预热瞬切）。
 */
/**
 * 首页模式选择器（FR-520）。
 *
 * 只读 URL 上的 `home` 参数，**不持任何本地状态**：这样「切到经典 → 刷新」仍停在经典，
 * 且两种模式可以互相直链。缺省（无参数或其它值）为 AI 首页。
 */
function HomeRoute() {
  const location = useLocation()
  const home = new URLSearchParams(location.search).get('home')
  return home === 'classic' ? <OverviewPage /> : <AssistantHomePage />
}

export default function Workspace() {
  const { t } = useTranslation()
  const location = useLocation()
  const isInstanceRoute = /^\/instances\/\d+/.test(location.pathname)
  // 权限配置页：固定视口高度，仅中栏滚动（与实例控制台同款骨架）
  const isPermissionsRoute = location.pathname === '/permissions' || location.pathname.startsWith('/permissions')
  /**
   * 实例**列表页**也走固定视口骨架。
   *
   * 列表页的滚动发生在虚拟表内部（表头吸顶 + 容器内滚），整页不该再滚一层。
   * 此前只把 `/instances/:id`（详情）纳入固定视口，列表页走的是整页滚动：
   * 外层 `overflow-auto` + `min-h-full` 的路由容器不给子级确定高度，
   * 于是表格的 `flex-1` 拿不到可分配空间、退回按内容撑高（实测 8294px），
   * 被外层再滚一次——两层滚动叠加，表格底部直接被推出视口。
   * `^/instances(/|$)` 只匹配列表本身，详情路由仍由上面的 `isInstanceRoute` 命中，
   * 两者互不干扰（且都已包含在下面的或运算里）。
   */
  const isInstanceListRoute = /^\/instances\/?$/.test(location.pathname)
  const isFixedViewportRoute = isInstanceRoute || isInstanceListRoute || isPermissionsRoute
  // 路由过渡容器 key 仅按 pathname——同页仅 query 变化（如发布向导 ?step= 步骤切换、列表筛选、
  // tab 切换）不得 remount 路由子树，否则会清空页内本地状态（如发布向导本地暂存的 drafts）。
  // 页面级切换（pathname 变）仍会换 key 重放进场动画。
  // `/instances/:id` 归并为固定 key（FR-296，ADR-067）：实例间切换不 remount 路由子树，
  // 由跨服热缓存宿主（InstanceConsoleCache）在子树内做 LRU 保活与瞬切。
  const routeKey = isInstanceRoute ? 'instances-console' : location.pathname

  // 超级工作台全幅（自带实例库 + 画布），不套统一内边距与滚动壳。
  if (location.pathname === '/super' || location.pathname.startsWith('/super/')) {
    // FR-502 阶段 6 补丁：fallback 从「一行加载中文字」换成同壳骨架（工具壳，无外层留白/页头）。
    return (
      <Suspense fallback={<PageSkeleton variant="tool" aria-label={t('common.loading')} />}>
        <SuperWorkbenchPage />
      </Suspense>
    )
  }

  // 导播台全幅（场景舞台 + 缩略图条），不套统一内边距与滚动壳。
  if (location.pathname === '/director' || location.pathname.startsWith('/director/')) {
    return (
      <Suspense fallback={<PageSkeleton variant="tool" aria-label={t('common.loading')} />}>
        <DirectorConsolePage />
      </Suspense>
    )
  }

  return (
    // 实例路由走视口自适应骨架（FR-422）：外层不滚动，滚动收口到页内卡片；
    // 顶栏与 Tab 栏因此常驻可见，底部不再随屏幕变大而留白。其他路由保持整页滚动不变。
    // 两条骨架分支的外边距必须同值（曾为 p-3 / p-3 sm:p-5 lg:p-6，固定视口页因此紧一档）。
    <div
      className={
        isFixedViewportRoute
          ? 'jm-workspace-bg flex h-full w-full flex-col overflow-hidden p-[clamp(6px,0.8vw,12px)]'
          : 'jm-workspace-bg h-full w-full overflow-auto p-[clamp(6px,0.8vw,12px)] [scrollbar-gutter:stable]'
      }
    >
      <div
        key={routeKey}
        data-slot="workspace-route-transition"
        className={isFixedViewportRoute ? 'jm-route-transition flex min-h-0 flex-1 flex-col' : 'jm-route-transition min-h-full'}
      >
        {/* FR-502 阶段 6 补丁：Suspense 边界收进路由子树**内部**。
            此前边界包住整个工作区壳，切页时 fallback 把外壳（背景、内边距、滚动容器、路由进场容器）
            一并替换掉，于是「骨架 → 真实页面」会整页跳动一次；现在外壳常驻，只有路由内容在
            「页面骨架 ↔ 真实页面」之间切换，骨架与页面共用同一套留白与滚动模型。
            骨架壳态跟随当前路由：实例控制台/权限页是固定视口态，其余是整页滚动态。 */}
        <Suspense
          fallback={
            <PageSkeleton
              variant={isFixedViewportRoute ? 'fixed' : 'default'}
              aria-label={t('common.loading')}
            />
          }
        >
          <Routes>
            {/* 首页模式（FR-520 §9.1）：`?home=classic` 走经典仪表盘，否则默认 AI 首页。
                刻意在**路由层**二选一而不是页内条件渲染：后者会让两套查询同时存活，
                与「只挂载当前模式的数据查询」直接冲突。
                深链 `/?home=classic` 刷新仍落经典模式（查询参数由路由保证，不存内存态）。 */}
            <Route index element={<HomeRoute />} />
            <Route path="monitor" element={<MonitoringPage />} />
            <Route path="nodes" element={<NodesPage />} />
            <Route path="instances" element={<InstancesPage />} />
            {/* 配置基线（FR-458）：集群级配置模板下发/漂移检测/一键收敛。 */}
            <Route path="config-baselines" element={<ConfigBaselinesPage />} />
            <Route path="instances/new" element={<InstanceWizardPage />} />
            <Route path="instances/:id" element={<InstanceDetailPage />} />
            {/* FR-376：文件深链（浏览器新标签）；不并入 instances-console 热缓存 key */}
            <Route path="instances/:id/files" element={<InstanceFilesPage />} />
            <Route path="networks" element={<NetworksPage />} />
            <Route path="networks/topology" element={<NetworksPage />} />
            <Route path="players" element={<PlayersPage />} />
            <Route path="bots" element={<BotsPage />} />
            {/* FR-372 压测运行详情（会话级观测） */}
            <Route path="bots/sessions/:id" element={<BotLoadSessionPage />} />
            <Route path="alerts" element={<AlertsPage />} />
            <Route path="users" element={<UsersPage />} />
            {/* FR-432 权限树：路由级 RequirePlatformAdmin + 页内 rbac.read 双闸。 */}
            <Route path="permissions" element={<RequirePlatformAdmin><PermissionsPage /></RequirePlatformAdmin>} />
            <Route path="groups" element={<GroupsPage />} />
            <Route path="templates" element={<TemplatesPage />} />
            <Route path="runtime-assets" element={<RuntimeAssetsPage />} />
            <Route path="schedules" element={<SchedulesPage />} />
            <Route path="backups" element={<BackupsPage />} />
            <Route path="backup-storages" element={<BackupStoragesPage />} />
            {/* 文件存储配置（FR-347）：客户端分发制品外置对象存储渠道。 */}
            <Route path="artifact-storages" element={<ArtifactStoragesPage />} />
            <Route path="artifact-versions" element={<RequirePlatformAdmin><ArtifactVersionsPage /></RequirePlatformAdmin>} />
            <Route path="audit" element={<AuditPage />} />
            <Route path="tasks" element={<TasksPage />} />
            {/* 通知中心（FR-216）：站内信 + 告警合并的统一通知流页。 */}
            <Route path="notifications" element={<NotificationCenterPage />} />
            {/* FR-430：页面 A「客户端分发」补平台管理员守卫（此前仅靠侧栏按角色隐藏）。 */}
            <Route path="client-channels" element={<RequirePlatformAdmin><ClientChannelsPage /></RequirePlatformAdmin>} />
            {/* FR-430：页面 B「客户端分发运维」新路由（7 Tab，观测 + 研判处置合并）+ 守卫。 */}
            <Route path="client-dist-ops" element={<RequirePlatformAdmin><ProtectionCenterPage /></RequirePlatformAdmin>} />
            {/* FR-430：旧安全中心路由 → 页面 B 参数翻译重定向（透传 query，不包守卫，仅 Navigate）。 */}
            <Route path="client-dist-security" element={<ClientDistRedirect source="security" />} />
            <Route path="client-channels/:id/publish" element={<ClientPublishPage />} />
            <Route path="logs" element={<LogsPage />} />
            {/* 观测·统计占位页（FR-215）；实质内容由 FR-220 补齐。 */}
            <Route path="statistics" element={<StatisticsPage />} />
            {/* FR-430：旧监控路由改重定向到页面 B「客户端分发运维」（观测入口已并入）。 */}
            <Route path="client-dist-monitor" element={<ClientDistRedirect source="monitor" />} />
            {/* 观测域同义旧链接重定向兼容（FR-215）：避免外部/手输旧路径 404。
                必须用 RedirectKeepQuery 而不是 Navigate：旧深链常带 ?instance=<uuid>，
                裸 Navigate 会把参数吞掉，用户点进来看到的是全平台而不是他那台。 */}
            <Route path="monitoring" element={<RedirectKeepQuery to="/monitor" />} />
            <Route path="stats" element={<RedirectKeepQuery to="/statistics" />} />
            <Route path="observability" element={<RedirectKeepQuery to="/monitor" />} />
            <Route path="storage" element={<StoragePage />} />
            <Route path="settings" element={<SettingsPage />} />
            {/* AI 助手配置（FR-514）：供应商/模型/默认模型，仅平台管理员。 */}
            <Route
              path="settings/ai"
              element={
                <RequirePlatformAdmin>
                  <AssistantSettingsPage />
                </RequirePlatformAdmin>
              }
            />
            <Route path="database" element={<RequirePlatformAdmin><DatabasePage /></RequirePlatformAdmin>} />
            <Route path="system-update" element={<RequirePlatformAdmin><SystemUpdatePage /></RequirePlatformAdmin>} />
            <Route path="agent-tokens" element={<RequirePlatformAdmin><AgentTokensPage /></RequirePlatformAdmin>} />
            <Route path="mcp-activity" element={<RequirePlatformAdmin><McpActivityPage /></RequirePlatformAdmin>} />
            {/* 端点无状态化（ADR-096）后会话维度视图已无数据源，旧链接重定向到按 Token 聚合的活动视图。 */}
            <Route path="mcp-sessions" element={<Navigate to="/mcp-activity" replace />} />
            <Route path="agent-call-logs" element={<RequirePlatformAdmin><AgentCallLogsPage /></RequirePlatformAdmin>} />
            <Route path="licenses" element={<RequirePlatformAdmin><LicensesPage /></RequirePlatformAdmin>} />
            <Route path="*" element={<WorkspaceEmpty />} />
          </Routes>
        </Suspense>
      </div>
    </div>
  )
}
