import { Link } from 'react-router'
import { toast } from 'sonner'
import { Button } from '@jianmanager/ui/components/button'
import { useInstance } from '@/api/instances'
import { useBackups, useCreateBackup, useDeleteBackup, useRestoreBackup } from '@/api/backups'
import { useBackupStorages } from '@/api/backupStorages'
import { useDeleteSchedule, useSchedules, useUpdateSchedule } from '@/api/schedules'
import { hasActiveBackup } from '@/lib/backup'
import InstanceBackupSegmentView from '@/components/views/instances/InstanceBackupSegment'
import { useTranslation } from 'react-i18next'

/** 进行中备份时的轮询间隔（毫秒）：刷新进度直至完成（FR-151，复用 BackupsPage 模式）。 */
const ACTIVE_POLL_MS = 3000

/**
 * 实例「备份 · 定时」分区的应用接线层（ADR-097 b 范式）。
 *
 * 分区本体是受控视图（见 components/views）；本层取两份列表（备份带「进行中轮询」）、存活判定、
 * 五个动作与「去创建定时任务」链接，把提示交给 toast。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function InstanceBackupSegment({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()
  const { data: instance } = useInstance(instanceId)
  const { data: storages } = useBackupStorages()
  const { data: schedules, isLoading: schedulesLoading } = useSchedules(instanceId)

  // 先无轮询取一次判定是否有进行中备份，再据此决定轮询间隔（FR-151，复用 BackupsPage 模式）。
  const probe = useBackups(instanceId)
  const active = hasActiveBackup(probe.data ?? [])
  const { data: backups, isLoading: backupsLoading } = useBackups(instanceId, {
    refetchInterval: active ? ACTIVE_POLL_MS : false,
  })

  const createBackup = useCreateBackup(instanceId)
  const deleteBackup = useDeleteBackup()
  const restoreBackup = useRestoreBackup()
  const updateSchedule = useUpdateSchedule()
  const deleteSchedule = useDeleteSchedule()

  // 实例进程可能存活（STARTING/RUNNING/STOPPING）时禁止恢复，与后端恢复守卫一致：
  // 运行中的服务器下次自动存档会覆盖掉刚恢复的文件，恢复会静默失效。
  const instanceLive = !!instance && ['STARTING', 'RUNNING', 'STOPPING'].includes(instance.status)

  return (
    <InstanceBackupSegmentView
      backups={backups ?? []}
      backupsLoading={backupsLoading}
      backupsActive={active}
      storages={storages ?? []}
      schedules={schedules ?? []}
      schedulesLoading={schedulesLoading}
      instanceLive={instanceLive}
      onCreateBackup={async (payload) => {
        await createBackup.mutateAsync({
          name: `${payload.incremental ? 'inc' : 'full'}-${new Date().toISOString().slice(0, 19)}`,
          incremental: payload.incremental,
          storageId: payload.storageId,
        })
      }}
      onRestoreBackup={async (backupId) => {
        await restoreBackup.mutateAsync(backupId)
      }}
      onDeleteBackup={async (backupId) => {
        await deleteBackup.mutateAsync(backupId)
      }}
      onToggleSchedule={async (payload) => {
        await updateSchedule.mutateAsync({ id: payload.id, body: { enabled: payload.enabled } })
      }}
      onDeleteSchedule={async (scheduleId) => {
        await deleteSchedule.mutateAsync(scheduleId)
      }}
      createScheduleLink={
        // 创建/编辑表单较重（cron 预设/校验/预览），v1 引导去独立页（spec §2.2）。
        <Button asChild size="sm" variant="outline">
          <Link to="/schedules">{t('serverConsole.goCreateSchedule')}</Link>
        </Button>
      }
      notify={(kind, message) => {
        if (kind === 'success') toast.success(message)
        else toast.error(message)
      }}
    />
  )
}
