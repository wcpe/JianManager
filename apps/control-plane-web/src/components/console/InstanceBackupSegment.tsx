import { useMemo, useState } from 'react'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Archive, CalendarClock } from 'lucide-react'

import { Button } from '@jianmanager/ui/components/button'
import { EmptyState } from '@jianmanager/ui/components/empty-state'
import { Panel } from '@jianmanager/ui/components/panel'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import DangerConfirm from '@/components/DangerConfirm'
import { useInstance } from '@/api/instances'
import { useBackups, useCreateBackup, useDeleteBackup, useRestoreBackup, type BackupInfo } from '@/api/backups'
import { useBackupStorages } from '@/api/backupStorages'
import { useDeleteSchedule, useSchedules, useUpdateSchedule, type ScheduleInfo } from '@/api/schedules'
import { describeCron } from '@/lib/cron'
import { ConfigSwitch } from '@/pages/config-row'
import {
  BACKUP_COMPLETED,
  BACKUP_MODE_INCREMENTAL,
  backupStatusKey,
  backupStatusLevel,
  countDependents,
  formatSizeMb,
  hasActiveBackup,
  isIncrementalChild,
} from '@/pages/backups-view'

/** 进行中备份时的轮询间隔（毫秒）：刷新进度直至完成（FR-151，复用 BackupsPage 模式）。 */
const ACTIVE_POLL_MS = 3000

/** 实例备份·定时分区 props（FR-339）。 */
interface InstanceBackupSegmentProps {
  /** 实例 DB ID：定时任务与备份均为实例原生作用域。 */
  instanceId: number
}

/**
 * 实例控制台「备份 · 定时」分区（FR-339）：本实例作用域的备份与定时任务。
 * - 定时任务：列表 + 启停（只发 `{enabled}`）+ 删除；创建/编辑表单较重，引导去独立页（spec §2.2 拍板）；
 * - 备份：列表（进行中 3s 轮询）+ 全量/增量创建（存储只读下拉，缺省本地）+ 恢复（运行态守卫）+ 删除（增量依赖警告）。
 * 备份仓库全局配置属独立页（FR-057/FR-338），不进本分区。
 */
export default function InstanceBackupSegment({ instanceId }: InstanceBackupSegmentProps) {
  return (
    <div className="space-y-3">
      <SchedulesPanel instanceId={instanceId} />
      <BackupsPanel instanceId={instanceId} />
    </div>
  )
}

/** 表格行加载骨架（共享 Skeleton 原语拼装）。 */
function RowsSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div className="space-y-2">
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} className="h-8 w-full" />
      ))}
    </div>
  )
}

/** 本实例定时任务：列表 + 启停 + 删除（交互复用 SchedulesPage 的 handler 模式）。 */
function SchedulesPanel({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()
  const { data: schedules, isLoading } = useSchedules(instanceId)
  const updateSchedule = useUpdateSchedule()
  const deleteSchedule = useDeleteSchedule()
  const [deleteTarget, setDeleteTarget] = useState<ScheduleInfo | null>(null)

  // cron 人类可读文案（FR-153）：可识别则译，否则退回原表达式。
  const cronReadable = (expr: string): string => {
    const desc = describeCron(expr)
    return desc ? t(desc.key, desc.params) : expr
  }

  const handleToggleEnabled = (s: ScheduleInfo) => {
    // 启停只发 { enabled }，不夹带其它字段（后端 PUT 局部更新语义）。
    updateSchedule.mutate(
      { id: s.id, body: { enabled: !s.enabled } },
      {
        onSuccess: () =>
          toast.success(s.enabled ? t('schedules.disabledToast') : t('schedules.enabledToast')),
        onError: (e: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(e?.response?.data?.message || t('common.error')),
      },
    )
  }

  const confirmDelete = () => {
    if (!deleteTarget) return
    const target = deleteTarget
    setDeleteTarget(null)
    deleteSchedule.mutate(target.id, {
      onSuccess: () => toast.success(t('schedules.deletedToast')),
      onError: (e: Error & { response?: { data?: { message?: string } } }) =>
        toast.error(e?.response?.data?.message || t('common.error')),
    })
  }

  const list = schedules ?? []
  const goCreateLink = (
    // 创建/编辑表单较重（cron 预设/校验/预览），v1 引导去独立页（spec §2.2）。
    <Button asChild size="sm" variant="outline">
      <Link to="/schedules">{t('serverConsole.goCreateSchedule')}</Link>
    </Button>
  )

  return (
    <Panel icon={<CalendarClock className="size-3.5" />} title={t('schedules.title')} actions={goCreateLink}>
      {isLoading ? (
        <RowsSkeleton />
      ) : list.length === 0 ? (
        <EmptyState icon={<CalendarClock />} title={t('schedules.empty')} />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('schedules.name')}</TableHead>
              <TableHead>{t('schedules.cron')}</TableHead>
              <TableHead>{t('schedules.action')}</TableHead>
              <TableHead>{t('schedules.lastRun')}</TableHead>
              <TableHead>{t('schedules.enabled')}</TableHead>
              <TableHead className="text-right">{t('common.actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.map((s) => (
              <TableRow key={s.id}>
                <TableCell className="font-medium">{s.name}</TableCell>
                <TableCell className="font-mono text-xs">
                  <div>{s.cronExpr}</div>
                  <div className="font-sans text-muted-foreground">{cronReadable(s.cronExpr)}</div>
                </TableCell>
                <TableCell>{t(`schedules.action_${s.action}`, { defaultValue: s.action })}</TableCell>
                <TableCell className="text-muted-foreground">
                  {s.lastRun ? new Date(s.lastRun).toLocaleString() : t('schedules.neverRun')}
                </TableCell>
                <TableCell>
                  <ConfigSwitch
                    checked={s.enabled}
                    onChange={() => handleToggleEnabled(s)}
                    label={t('schedules.enabled')}
                    onLabel={t('schedules.enable')}
                    offLabel={t('schedules.disable')}
                  />
                </TableCell>
                <TableCell className="text-right">
                  <Button
                    variant="ghost"
                    size="xs"
                    className="text-status-danger hover:text-status-danger"
                    onClick={() => setDeleteTarget(s)}
                  >
                    {t('common.delete')}
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <DangerConfirm
        open={deleteTarget !== null}
        title={t('schedules.deleteTitle', { name: deleteTarget?.name ?? '' })}
        description={t('schedules.deleteDesc')}
        confirmLabel={t('common.delete')}
        scope="group"
        pending={deleteSchedule.isPending}
        onConfirm={confirmDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </Panel>
  )
}

/** 本实例备份：列表（进行中轮询）+ 全量/增量创建 + 恢复（运行态守卫）+ 删除（依赖警告）。 */
function BackupsPanel({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()
  const { data: instance } = useInstance(instanceId)
  const { data: storages } = useBackupStorages()
  const [storageId, setStorageId] = useState<number | undefined>()
  const [restoreTarget, setRestoreTarget] = useState<number | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<BackupInfo | null>(null)

  // 先无轮询取一次判定是否有进行中备份，再据此决定轮询间隔（FR-151，复用 BackupsPage 模式）。
  const probe = useBackups(instanceId)
  const active = hasActiveBackup(probe.data ?? [])
  const { data: backups, isLoading } = useBackups(instanceId, {
    refetchInterval: active ? ACTIVE_POLL_MS : false,
  })

  const createBackup = useCreateBackup(instanceId)
  const deleteBackup = useDeleteBackup()
  const restoreBackup = useRestoreBackup()

  // 实例进程可能存活（STARTING/RUNNING/STOPPING）时禁止恢复，与后端恢复守卫一致：
  // 运行中的服务器下次自动存档会覆盖掉刚恢复的文件，恢复会静默失效。
  const instanceLive = !!instance && ['STARTING', 'RUNNING', 'STOPPING'].includes(instance.status)
  const restoreDisabledTitle = instanceLive ? t('backups.restoreNeedStopped') : undefined

  const list = useMemo(() => backups ?? [], [backups])
  const backupById = useMemo(() => new Map(list.map((b) => [b.id, b])), [list])

  const storageName = (id?: number) =>
    id ? ((storages ?? []).find((s) => s.id === id)?.name ?? `#${id}`) : t('backups.localStorage')
  const parentName = (b: BackupInfo) =>
    b.parentId !== undefined ? (backupById.get(b.parentId)?.name ?? `#${b.parentId}`) : undefined

  const handleCreate = async (incremental: boolean) => {
    try {
      await createBackup.mutateAsync({
        name: `${incremental ? 'inc' : 'full'}-${new Date().toISOString().slice(0, 19)}`,
        incremental,
        storageId,
      })
      toast.success(t('backups.creating'))
    } catch (e: unknown) {
      // 增量缺少基准时后端回 422 BUSINESS_ERROR，透传定向提示。
      const msg = (e as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(msg || t('backups.createFailed'))
    }
  }

  const handleRestore = async (backupId: number) => {
    try {
      await restoreBackup.mutateAsync(backupId)
      toast.success(t('backups.restoring'))
    } catch (e: unknown) {
      // 实例未停止时后端回 409 INSTANCE_NOT_STOPPED，透传定向提示。
      const msg = (e as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(msg || t('backups.restoreFailed'))
    }
    setRestoreTarget(null)
  }

  const handleDelete = (backup: BackupInfo) => {
    deleteBackup.mutate(backup.id, {
      onSuccess: () => toast.success(t('common.deleted')),
      onError: (e: unknown) => {
        const msg = (e as { response?: { data?: { message?: string } } })?.response?.data?.message
        toast.error(msg || t('backups.deleteFailed'))
      },
    })
  }

  // 删除前算直接依赖此备份的增量数，用于二次确认警告（FR-151）。
  const dependents = deleteTarget ? countDependents(list, deleteTarget.id) : 0

  const modeBadge = (b: BackupInfo) =>
    b.mode === BACKUP_MODE_INCREMENTAL ? (
      <StatusBadge level="info" label={t('backups.incremental')} dot={false} />
    ) : (
      <StatusBadge level="neutral" label={t('backups.full')} dot={false} />
    )

  const statusBadge = (b: BackupInfo) => (
    <StatusBadge
      level={backupStatusLevel(b.status)}
      label={t(`backups.${backupStatusKey(b.status)}`)}
      pulse={b.status !== BACKUP_COMPLETED && b.status !== 3}
    />
  )

  const createActions = (
    <>
      {/* 存储选择保留只读下拉（列表消费，缺省本地）；仓库增删改属独立页（FR-338）。 */}
      <select
        className="rounded border bg-background p-1.5 text-xs"
        value={storageId ?? ''}
        onChange={(e) => setStorageId(e.target.value ? Number(e.target.value) : undefined)}
        title={t('backups.selectStorage')}
        aria-label={t('backups.selectStorage')}
      >
        <option value="">{t('backups.localStorage')}</option>
        {(storages ?? []).map((s) => (
          <option key={s.id} value={s.id}>
            {s.name}
          </option>
        ))}
      </select>
      <Button size="sm" onClick={() => handleCreate(false)} disabled={createBackup.isPending}>
        {t('backups.createFull')}
      </Button>
      <Button size="sm" variant="outline" onClick={() => handleCreate(true)} disabled={createBackup.isPending}>
        {t('backups.createIncremental')}
      </Button>
    </>
  )

  return (
    <Panel icon={<Archive className="size-3.5" />} title={t('backups.title')} actions={createActions}>
      {active && (
        <div className="mb-2 inline-flex items-center gap-1.5 text-xs text-status-info">
          <span className="size-1.5 animate-pulse rounded-full bg-status-info" />
          {t('backups.autoRefreshing')}
        </div>
      )}
      {isLoading && list.length === 0 ? (
        <RowsSkeleton />
      ) : list.length === 0 ? (
        <EmptyState icon={<Archive />} title={t('backups.empty')} />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('backups.name')}</TableHead>
              <TableHead>{t('backups.mode')}</TableHead>
              <TableHead>{t('backups.size')}</TableHead>
              <TableHead>{t('backups.storageLocation')}</TableHead>
              <TableHead>{t('backups.status')}</TableHead>
              <TableHead>{t('backups.time')}</TableHead>
              <TableHead className="text-right">{t('common.actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.map((b) => (
              <TableRow key={b.id}>
                <TableCell className="font-medium">{b.name}</TableCell>
                <TableCell>
                  <div className="flex flex-col gap-1">
                    {modeBadge(b)}
                    {isIncrementalChild(b) && (
                      <span className="text-xs text-muted-foreground">
                        {t('backups.basedOn')} {parentName(b)}
                      </span>
                    )}
                  </div>
                </TableCell>
                <TableCell>{formatSizeMb(b.fileSizeMb)}</TableCell>
                <TableCell>{storageName(b.storageId)}</TableCell>
                <TableCell>{statusBadge(b)}</TableCell>
                <TableCell className="text-muted-foreground">{new Date(b.createdAt).toLocaleString()}</TableCell>
                <TableCell className="text-right whitespace-nowrap">
                  <div className="flex justify-end gap-1">
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() => setRestoreTarget(b.id)}
                      disabled={b.status !== BACKUP_COMPLETED || restoreBackup.isPending || instanceLive}
                      title={restoreDisabledTitle}
                    >
                      {t('backups.restore')}
                    </Button>
                    <Button
                      variant="ghost"
                      size="xs"
                      className="text-status-danger hover:text-status-danger"
                      onClick={() => setDeleteTarget(b)}
                      title={countDependents(list, b.id) > 0 ? t('backups.dependentsWarn', { count: countDependents(list, b.id) }) : undefined}
                    >
                      {t('common.delete')}
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <DangerConfirm
        open={restoreTarget !== null}
        title={t('backups.confirmRestore')}
        description={t('backups.restoreWarning')}
        confirmLabel={t('backups.restore')}
        scope="group"
        pending={restoreBackup.isPending}
        onConfirm={() => { if (restoreTarget) handleRestore(restoreTarget) }}
        onCancel={() => setRestoreTarget(null)}
      />

      <DangerConfirm
        open={deleteTarget !== null}
        title={t('backups.deleteConfirm')}
        description={dependents > 0 ? t('backups.dependentsWarn', { count: dependents }) : t('common.irreversible')}
        confirmLabel={t('common.delete')}
        scope="group"
        pending={deleteBackup.isPending}
        onConfirm={() => { if (deleteTarget) handleDelete(deleteTarget); setDeleteTarget(null) }}
        onCancel={() => setDeleteTarget(null)}
      />
    </Panel>
  )
}
