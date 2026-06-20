import { Routes, Route, useLocation } from 'react-router'
import { Suspense, lazy, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { useConsoleStore } from '@/stores/console'
import WorkspacePane from './WorkspacePane'
import WorkspaceEmpty from './WorkspaceEmpty'

const OverviewPage = lazy(() => import('@/pages/OverviewPage'))
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
const AlertsPage = lazy(() => import('@/pages/AlertsPage'))
const SettingsPage = lazy(() => import('@/pages/SettingsPage'))
const LogsPage = lazy(() => import('@/pages/LogsPage'))

/**
 * 运维控制台右侧工作区（ADR-009 / FR-037 / FR-039）。
 * 打开实例时渲染单个 WorkspacePane（终端 | Bot 分段）；否则按路由渲染对应页面，既有页面不变。
 * 同一时刻仅一个实例面板，切换实例即换 instanceId。
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
    return <WorkspacePane instanceId={openInstanceId} />
  }

  return (
    <Suspense fallback={<div className="p-6 text-muted-foreground">{t('common.loading')}</div>}>
      <div className="h-full overflow-auto p-6">
        <Routes>
          <Route index element={<OverviewPage />} />
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
          <Route path="schedules" element={<SchedulesPage />} />
          <Route path="backups" element={<BackupsPage />} />
          <Route path="backup-storages" element={<BackupStoragesPage />} />
          <Route path="audit" element={<AuditPage />} />
          <Route path="logs" element={<LogsPage />} />
          <Route path="settings" element={<SettingsPage />} />
          <Route path="*" element={<WorkspaceEmpty />} />
        </Routes>
      </div>
    </Suspense>
  )
}
