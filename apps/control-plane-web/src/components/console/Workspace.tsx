import { Routes, Route, Navigate, useLocation } from 'react-router'
import { Suspense, lazy, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useAuthStore } from '@/stores/auth'
import WorkspaceEmpty from './WorkspaceEmpty'

const OverviewPage = lazy(() => import('@/pages/OverviewPage'))
const MonitoringPage = lazy(() => import('@/pages/MonitoringPage'))
const NodesPage = lazy(() => import('@/pages/NodesPage'))
const InstancesPage = lazy(() => import('@/pages/InstancesPage'))
const InstanceDetailPage = lazy(() => import('@/pages/InstanceDetailPage'))
const InstanceWizardPage = lazy(() => import('@/pages/InstanceWizardPage'))
const NetworksPage = lazy(() => import('@/pages/NetworksPage'))
const PlayersPage = lazy(() => import('@/pages/PlayersPage'))
const UsersPage = lazy(() => import('@/pages/UsersPage'))
const GroupsPage = lazy(() => import('@/pages/GroupsPage'))
const SchedulesPage = lazy(() => import('@/pages/SchedulesPage'))
const BackupsPage = lazy(() => import('@/pages/BackupsPage'))
const BackupStoragesPage = lazy(() => import('@/pages/BackupStoragesPage'))
const ArtifactStoragesPage = lazy(() => import('@/pages/ArtifactStoragesPage'))
const BotsPage = lazy(() => import('@/pages/BotsPage'))
const AuditPage = lazy(() => import('@/pages/AuditPage'))
const TemplatesPage = lazy(() => import('@/pages/TemplatesPage'))
const RuntimeAssetsPage = lazy(() => import('@/pages/RuntimeAssetsPage'))
const AlertsPage = lazy(() => import('@/pages/AlertsPage'))
const SettingsPage = lazy(() => import('@/pages/SettingsPage'))
const StoragePage = lazy(() => import('@/pages/StoragePage'))
const LogsPage = lazy(() => import('@/pages/LogsPage'))
const StatisticsPage = lazy(() => import('@/pages/StatisticsPage'))
const ClientDistMonitoringPage = lazy(() => import('@/pages/ClientDistMonitoringPage'))
const ClientChannelsPage = lazy(() => import('@/pages/ClientChannelsPage'))
const ProtectionCenterPage = lazy(() => import('@/pages/ProtectionCenterPage'))
const ClientPublishPage = lazy(() => import('@/pages/ClientPublishPage'))
const DatabasePage = lazy(() => import('@/pages/DatabasePage'))
const SystemUpdatePage = lazy(() => import('@/pages/SystemUpdatePage'))
const AgentTokensPage = lazy(() => import('@/pages/AgentTokensPage'))
const LicensesPage = lazy(() => import('@/pages/LicensesPage'))
const TasksPage = lazy(() => import('@/pages/TasksPage'))
const NotificationCenterPage = lazy(() => import('@/pages/NotificationCenterPage'))
const SuperWorkbenchPage = lazy(() => import('./SuperWorkbenchPage'))
const DirectorConsolePage = lazy(() => import('./DirectorConsolePage'))

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
export default function Workspace() {
  const { t } = useTranslation()
  const location = useLocation()
  const isInstanceRoute = /^\/instances\/\d+/.test(location.pathname)
  // 路由过渡容器 key 仅按 pathname——同页仅 query 变化（如发布向导 ?step= 步骤切换、列表筛选、
  // tab 切换）不得 remount 路由子树，否则会清空页内本地状态（如发布向导本地暂存的 drafts）。
  // 页面级切换（pathname 变）仍会换 key 重放进场动画。
  // `/instances/:id` 归并为固定 key（FR-296，ADR-067）：实例间切换不 remount 路由子树，
  // 由跨服热缓存宿主（InstanceConsoleCache）在子树内做 LRU 保活与瞬切。
  const routeKey = isInstanceRoute ? 'instances-console' : location.pathname

  // 超级工作台全幅（自带实例库 + 画布），不套统一内边距与滚动壳。
  if (location.pathname === '/super' || location.pathname.startsWith('/super/')) {
    return (
      <Suspense fallback={<div className="p-6 text-muted-foreground">{t('common.loading')}</div>}>
        <SuperWorkbenchPage />
      </Suspense>
    )
  }

  // 导播台全幅（场景舞台 + 缩略图条），不套统一内边距与滚动壳。
  if (location.pathname === '/director' || location.pathname.startsWith('/director/')) {
    return (
      <Suspense fallback={<div className="p-6 text-muted-foreground">{t('common.loading')}</div>}>
        <DirectorConsolePage />
      </Suspense>
    )
  }

  return (
    <Suspense fallback={<div className="p-6 text-muted-foreground">{t('common.loading')}</div>}>
      <div className={isInstanceRoute ? 'jm-workspace-bg h-full w-full overflow-auto p-3 [scrollbar-gutter:stable]' : 'jm-workspace-bg h-full w-full overflow-auto p-3 [scrollbar-gutter:stable] sm:p-5 lg:p-6'}>
        <div key={routeKey} data-slot="workspace-route-transition" className="jm-route-transition min-h-full">
          <Routes>
            <Route index element={<OverviewPage />} />
            <Route path="monitor" element={<MonitoringPage />} />
            <Route path="nodes" element={<NodesPage />} />
            <Route path="instances" element={<InstancesPage />} />
            <Route path="instances/new" element={<InstanceWizardPage />} />
            <Route path="instances/:id" element={<InstanceDetailPage />} />
            <Route path="networks" element={<NetworksPage />} />
            <Route path="networks/topology" element={<NetworksPage />} />
            <Route path="players" element={<PlayersPage />} />
            <Route path="bots" element={<BotsPage />} />
            <Route path="alerts" element={<AlertsPage />} />
            <Route path="users" element={<UsersPage />} />
            <Route path="groups" element={<GroupsPage />} />
            <Route path="templates" element={<TemplatesPage />} />
            <Route path="runtime-assets" element={<RuntimeAssetsPage />} />
            <Route path="schedules" element={<SchedulesPage />} />
            <Route path="backups" element={<BackupsPage />} />
            <Route path="backup-storages" element={<BackupStoragesPage />} />
            {/* 文件存储配置（FR-347）：客户端分发制品外置对象存储渠道。 */}
            <Route path="artifact-storages" element={<ArtifactStoragesPage />} />
            <Route path="audit" element={<AuditPage />} />
            <Route path="tasks" element={<TasksPage />} />
            {/* 通知中心（FR-216）：站内信 + 告警合并的统一通知流页。 */}
            <Route path="notifications" element={<NotificationCenterPage />} />
            <Route path="client-channels" element={<ClientChannelsPage />} />
            <Route path="client-dist-security" element={<ProtectionCenterPage />} />
            <Route path="client-channels/:id/publish" element={<ClientPublishPage />} />
            <Route path="logs" element={<LogsPage />} />
            {/* 观测·统计占位页（FR-215）；实质内容由 FR-220 补齐。 */}
            <Route path="statistics" element={<StatisticsPage />} />
            {/* 观测·客户端分发监控页（FR-218）：消费 FR-217 观测底座出时序趋势 + 分布/榜单，总览 + 频道筛选。
                用平级路径（非 /monitor/* 嵌套）避免侧栏「监控总览」NavLink 前缀匹配误高亮。 */}
            <Route path="client-dist-monitor" element={<ClientDistMonitoringPage />} />
            {/* 观测域同义旧链接重定向兼容（FR-215）：避免外部/手输旧路径 404。 */}
            <Route path="monitoring" element={<Navigate to="/monitor" replace />} />
            <Route path="stats" element={<Navigate to="/statistics" replace />} />
            <Route path="observability" element={<Navigate to="/monitor" replace />} />
            <Route path="storage" element={<StoragePage />} />
            <Route path="settings" element={<SettingsPage />} />
            <Route path="database" element={<RequirePlatformAdmin><DatabasePage /></RequirePlatformAdmin>} />
            <Route path="system-update" element={<RequirePlatformAdmin><SystemUpdatePage /></RequirePlatformAdmin>} />
            <Route path="agent-tokens" element={<RequirePlatformAdmin><AgentTokensPage /></RequirePlatformAdmin>} />
            <Route path="licenses" element={<RequirePlatformAdmin><LicensesPage /></RequirePlatformAdmin>} />
            <Route path="*" element={<WorkspaceEmpty />} />
          </Routes>
        </div>
      </div>
    </Suspense>
  )
}
