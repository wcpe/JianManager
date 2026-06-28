import { Routes, Route, useLocation } from 'react-router'
import { Suspense, lazy, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { useConsoleStore } from '@/stores/console'
import WorkspaceCanvas from './WorkspaceCanvas'
import WorkspaceEmpty from './WorkspaceEmpty'

const OverviewPage = lazy(() => import('@/pages/OverviewPage'))
const MonitoringPage = lazy(() => import('@/pages/MonitoringPage'))
const NodesPage = lazy(() => import('@/pages/NodesPage'))
const InstancesPage = lazy(() => import('@/pages/InstancesPage'))
const InstanceDetailPage = lazy(() => import('@/pages/InstanceDetailPage'))
const NetworksPage = lazy(() => import('@/pages/NetworksPage'))
const PlayersPage = lazy(() => import('@/pages/PlayersPage'))
const UsersPage = lazy(() => import('@/pages/UsersPage'))
const GroupsPage = lazy(() => import('@/pages/GroupsPage'))
const SchedulesPage = lazy(() => import('@/pages/SchedulesPage'))
const BackupsPage = lazy(() => import('@/pages/BackupsPage'))
const BackupStoragesPage = lazy(() => import('@/pages/BackupStoragesPage'))
const BotsPage = lazy(() => import('@/pages/BotsPage'))
const AuditPage = lazy(() => import('@/pages/AuditPage'))
const TemplatesPage = lazy(() => import('@/pages/TemplatesPage'))
const RuntimeAssetsPage = lazy(() => import('@/pages/RuntimeAssetsPage'))
const AlertsPage = lazy(() => import('@/pages/AlertsPage'))
const SettingsPage = lazy(() => import('@/pages/SettingsPage'))
const StoragePage = lazy(() => import('@/pages/StoragePage'))
const LogsPage = lazy(() => import('@/pages/LogsPage'))
const ClientChannelsPage = lazy(() => import('@/pages/ClientChannelsPage'))
const ClientPublishPage = lazy(() => import('@/pages/ClientPublishPage'))
const DatabasePage = lazy(() => import('@/pages/DatabasePage'))
const SystemUpdatePage = lazy(() => import('@/pages/SystemUpdatePage'))
const LicensesPage = lazy(() => import('@/pages/LicensesPage'))
const TasksPage = lazy(() => import('@/pages/TasksPage'))
const SuperWorkbenchPage = lazy(() => import('./SuperWorkbenchPage'))
const DirectorConsolePage = lazy(() => import('./DirectorConsolePage'))
/**
 * 运维控制台右侧工作区（ADR-009 / FR-037 / FR-039 / FR-166 / FR-167）。
 * 打开实例时渲染可组合卡片画布 {@link WorkspaceCanvas}（卡片=实例×功能，可拖拽/调大小/存预设）；
 * 否则按路由渲染对应页面，既有页面不变。同一时刻仅一个实例画布，切换实例即换 instanceId。
 * 跨实例超级工作台（FR-167）走 `/super`，全幅渲染（自带左侧实例库 + 画布，无统一内边距）。
 * 工作区导播台（FR-168）走 `/director`，同为全幅（场景舞台 + 缩略图条，多预设预热瞬切）。
 */
export default function Workspace() {
  const { t } = useTranslation()
  const openInstanceId = useConsoleStore((s) => s.openInstanceId)
  const closeInstance = useConsoleStore((s) => s.closeInstance)
  const location = useLocation()
  const lastPathRef = useRef(location.pathname)

  // 打开实例工作区不改 URL；但侧栏导航切页时路由会变。此处监听路由变化即关闭工作区，
  // 让导航能正常切到目标页面（此前 openInstanceId 非空会永久覆盖路由内容，无法切页）。
  useEffect(() => {
    if (location.pathname !== lastPathRef.current) {
      lastPathRef.current = location.pathname
      if (openInstanceId !== null) closeInstance()
    }
  }, [location.pathname, openInstanceId, closeInstance])

  if (openInstanceId !== null) {
    return <WorkspaceCanvas instanceId={openInstanceId} />
  }

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
      <div className="h-full overflow-auto p-6">
        <Routes>
          <Route index element={<OverviewPage />} />
          <Route path="monitor" element={<MonitoringPage />} />
          <Route path="nodes" element={<NodesPage />} />
          <Route path="instances" element={<InstancesPage />} />
          <Route path="instances/:id" element={<InstanceDetailPage />} />
          <Route path="networks" element={<NetworksPage />} />
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
          <Route path="audit" element={<AuditPage />} />
          <Route path="tasks" element={<TasksPage />} />
          <Route path="client-channels" element={<ClientChannelsPage />} />
          <Route path="client-channels/:id/publish" element={<ClientPublishPage />} />
          <Route path="logs" element={<LogsPage />} />
          <Route path="storage" element={<StoragePage />} />
          <Route path="settings" element={<SettingsPage />} />
          <Route path="database" element={<DatabasePage />} />
          <Route path="system-update" element={<SystemUpdatePage />} />
          <Route path="licenses" element={<LicensesPage />} />
          <Route path="*" element={<WorkspaceEmpty />} />
        </Routes>
      </div>
    </Suspense>
  )
}
