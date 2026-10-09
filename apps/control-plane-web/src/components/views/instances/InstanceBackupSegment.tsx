import { useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
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
import DangerConfirm from '@/components/views/DangerConfirm'
import { ConfigSwitch } from '@/components/views/instances/ConfigSwitch'
import { describeCron } from '@/lib/cron'
import {
  BACKUP_COMPLETED,
  BACKUP_MODE_INCREMENTAL,
  backupStatusKey,
  backupStatusLevel,
  countDependents,
  formatSizeMb,
  isIncrementalChild,
  type BackupInfo,
} from '@/lib/backup'
import type { ScheduleInfo } from '@/lib/schedule'

/** 提示通道：视图算好文案交外壳展示（本包不弹 toast）。 */
export type BackupNotice = (kind: 'success' | 'error', message: string) => void

/** 可选的备份仓库（只读下拉用）。 */
export interface BackupStorageOption {
  id: number
  name: string
}

/**
 * 实例控制台「备份 · 定时」分区（FR-339）：本实例作用域的备份与定时任务。
 * - 定时任务：列表 + 启停（只发 `{enabled}`）+ 删除；创建/编辑表单较重，引导去独立页（spec §2.2 拍板）；
 * - 备份：列表（进行中 3s 轮询）+ 全量/增量创建（存储只读下拉，缺省本地）+ 恢复（运行态守卫）+ 删除（增量依赖警告）。
 * 备份仓库全局配置属独立页（FR-057/FR-338），不进本分区。
 *
 * 布局为分栏（FR-423，spec §3.1 备份定时 62:38）：左 62% 放备份记录（7 列宽表，条数只增不减），
 * 右 38% 放定时任务（任务通常个位数，是内容驱动的窄块）。原先两者纵向堆叠，
 * 定时任务把备份表推到折叠线以下，而两张表又都被拉到同一整宽——宽表挤、窄表空。
 *
 * 两卡一律「内容驱动高度 + 栏高封顶 + 卡内滚动」（spec §3.2）：备份多时撑到栏高上限后表体内部滚，
 * 少时贴合内容。不写死 `flex-1`——否则 2 条备份也把卡撑满整栏，正是本批要消灭的
 * 「矮内容被拉平成死区」。
 *
 * 受控视图（ADR-097 b 范式）：不取数、不发请求、不弹 toast——两份列表、存活判定、
 * 五个动作与「去创建定时任务」链接都由外壳注入；轮询由外壳负责（视图只显示自动刷新提示）。
 */
export interface InstanceBackupSegmentProps {
  /** 本实例的备份记录。 */
  backups: BackupInfo[]
  /** 备份列表加载中。 */
  backupsLoading?: boolean
  /** 是否有进行中备份（显示自动刷新提示）。 */
  backupsActive?: boolean
  /** 可用备份仓库。 */
  storages: BackupStorageOption[]
  /** 本实例的定时任务。 */
  schedules: ScheduleInfo[]
  /** 定时任务加载中。 */
  schedulesLoading?: boolean
  /** 实例进程是否存活（存活时禁止恢复，与后端守卫一致）。 */
  instanceLive: boolean
  /** 创建备份。失败请抛错，视图取服务端 message 提示。 */
  onCreateBackup: (payload: { incremental: boolean; storageId?: number }) => Promise<void>
  /** 恢复备份。 */
  onRestoreBackup: (backupId: number) => Promise<void>
  /** 删除备份。 */
  onDeleteBackup: (backupId: number) => Promise<void>
  /** 启停定时任务（只发 enabled）。 */
  onToggleSchedule: (payload: { id: number; enabled: boolean }) => Promise<void>
  /** 删除定时任务。 */
  onDeleteSchedule: (scheduleId: number) => Promise<void>
  /** 「去创建定时任务」链接（路由由外壳给）。 */
  createScheduleLink: ReactNode
  /** 提示通道。 */
  notify: BackupNotice
}

export default function InstanceBackupSegment(props: InstanceBackupSegmentProps) {
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2 xl:flex-row">
      {/* 左栏 62%：备份记录横向要 7 列、纵向要行数，是本页的主列表。 */}
      <div className="flex flex-none flex-col xl:min-h-0 xl:flex-[1.6]">
        <BackupsPanel {...props} />
      </div>

      {/* 右栏 38%：定时任务。xl 以下退成整页纵向堆叠，权重只在 xl 生效——
          窄屏若仍按权重分高度，两栏会互相挤压，页面该滚的时候滚不动。 */}
      <div className="flex flex-none flex-col gap-2 xl:min-h-0 xl:flex-1">
        <SchedulesPanel {...props} />
      </div>
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
function SchedulesPanel({
  schedules,
  schedulesLoading = false,
  createScheduleLink,
  onToggleSchedule,
  onDeleteSchedule,
  notify,
}: InstanceBackupSegmentProps) {
  const { t } = useTranslation()
  const [deleteTarget, setDeleteTarget] = useState<ScheduleInfo | null>(null)
  const [pending, setPending] = useState(false)

  // cron 人类可读文案（FR-153）：可识别则译，否则退回原表达式。
  const cronReadable = (expr: string): string => {
    const desc = describeCron(expr)
    return desc ? t(desc.key, desc.params) : expr
  }

  const handleToggleEnabled = async (s: ScheduleInfo) => {
    try {
      // 启停只发 { enabled }，不夹带其它字段（后端 PUT 局部更新语义）。
      await onToggleSchedule({ id: s.id, enabled: !s.enabled })
      notify('success', s.enabled ? t('schedules.disabledToast') : t('schedules.enabledToast'))
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { message?: string } } })?.response?.data?.message
      notify('error', msg || t('common.error'))
    }
  }

  const confirmDelete = async () => {
    if (!deleteTarget) return
    const target = deleteTarget
    setPending(true)
    try {
      await onDeleteSchedule(target.id)
      notify('success', t('schedules.deletedToast'))
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { message?: string } } })?.response?.data?.message
      notify('error', msg || t('common.error'))
    } finally {
      setPending(false)
      setDeleteTarget(null)
    }
  }

  const list = schedules

  return (
    // 内容驱动高度（spec §3.2）：定时任务通常个位数条，撑高只会在卡内留死区。
    // 6 列在 38% 栏内偏挤，用横向滚动兜住而不压缩列（列宽治理归 FR-424，本批不动）。
    <Panel className="flex-none" icon={<CalendarClock className="size-3.5" />} title={t('schedules.title')} actions={createScheduleLink}>
      {schedulesLoading ? (
        <RowsSkeleton />
      ) : list.length === 0 ? (
        <EmptyState icon={<CalendarClock />} title={t('schedules.empty')} />
      ) : (
        <div className="overflow-x-auto">
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
                      onChange={() => void handleToggleEnabled(s)}
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
        </div>
      )}

      <DangerConfirm
        open={deleteTarget !== null}
        title={t('schedules.deleteTitle', { name: deleteTarget?.name ?? '' })}
        description={t('schedules.deleteDesc')}
        confirmLabel={t('common.delete')}
        scope="group"
        pending={pending}
        onConfirm={() => void confirmDelete()}
        onCancel={() => setDeleteTarget(null)}
      />
    </Panel>
  )
}

/** 本实例备份：列表（进行中轮询）+ 全量/增量创建 + 恢复（运行态守卫）+ 删除（依赖警告）。 */
function BackupsPanel({
  backups,
  backupsLoading = false,
  backupsActive = false,
  storages,
  instanceLive,
  onCreateBackup,
  onRestoreBackup,
  onDeleteBackup,
  notify,
}: InstanceBackupSegmentProps) {
  const { t } = useTranslation()
  const [storageId, setStorageId] = useState<number | undefined>()
  const [restoreTarget, setRestoreTarget] = useState<number | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<BackupInfo | null>(null)
  const [creating, setCreating] = useState(false)
  const [restoring, setRestoring] = useState(false)
  const [deleting, setDeleting] = useState(false)

  const restoreDisabledTitle = instanceLive ? t('backups.restoreNeedStopped') : undefined

  const list = useMemo(() => backups, [backups])
  const backupById = useMemo(() => new Map(list.map((b) => [b.id, b])), [list])

  const storageName = (id?: number) =>
    id ? (storages.find((s) => s.id === id)?.name ?? `#${id}`) : t('backups.localStorage')
  const parentName = (b: BackupInfo) =>
    b.parentId !== undefined ? (backupById.get(b.parentId)?.name ?? `#${b.parentId}`) : undefined

  const handleCreate = async (incremental: boolean) => {
    setCreating(true)
    try {
      await onCreateBackup({ incremental, storageId })
      notify('success', t('backups.creating'))
    } catch (e: unknown) {
      // 增量缺少基准时后端回 422 BUSINESS_ERROR，透传定向提示。
      const msg = (e as { response?: { data?: { message?: string } } })?.response?.data?.message
      notify('error', msg || t('backups.createFailed'))
    } finally {
      setCreating(false)
    }
  }

  const handleRestore = async (backupId: number) => {
    setRestoring(true)
    try {
      await onRestoreBackup(backupId)
      notify('success', t('backups.restoring'))
    } catch (e: unknown) {
      // 实例未停止时后端回 409 INSTANCE_NOT_STOPPED，透传定向提示。
      const msg = (e as { response?: { data?: { message?: string } } })?.response?.data?.message
      notify('error', msg || t('backups.restoreFailed'))
    } finally {
      setRestoring(false)
      setRestoreTarget(null)
    }
  }

  const handleDelete = async (backup: BackupInfo) => {
    setDeleting(true)
    try {
      await onDeleteBackup(backup.id)
      notify('success', t('common.deleted'))
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { message?: string } } })?.response?.data?.message
      notify('error', msg || t('backups.deleteFailed'))
    } finally {
      setDeleting(false)
      setDeleteTarget(null)
    }
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
        {storages.map((s) => (
          <option key={s.id} value={s.id}>
            {s.name}
          </option>
        ))}
      </select>
      <Button size="sm" onClick={() => void handleCreate(false)} disabled={creating}>
        {t('backups.createFull')}
      </Button>
      <Button size="sm" variant="outline" onClick={() => void handleCreate(true)} disabled={creating}>
        {t('backups.createIncremental')}
      </Button>
    </>
  )

  return (
    // 内容驱动 + 栏高封顶（FR-423）：备份条数只增不减，撑到栏高上限后表体内部滚（表头随之留在视野内），
    // 少时贴合内容。body 去 padding 交给内部分块，滚动容器才能贴着卡边。
    <Panel
      className="max-h-full min-h-0 flex-none"
      bodyClassName="flex min-h-0 flex-col overflow-hidden p-0"
      icon={<Archive className="size-3.5" />}
      title={t('backups.title')}
      actions={createActions}
    >
      {backupsActive && (
        <div className="flex-none px-3 pt-2">
          <span className="inline-flex items-center gap-1.5 text-xs text-status-info">
            <span className="size-1.5 animate-pulse rounded-full bg-status-info" />
            {t('backups.autoRefreshing')}
          </span>
        </div>
      )}
      {backupsLoading && list.length === 0 ? (
        <div className="p-3">
          <RowsSkeleton />
        </div>
      ) : list.length === 0 ? (
        <EmptyState icon={<Archive />} title={t('backups.empty')} />
      ) : (
        <div className="min-h-0 overflow-auto">
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
                        disabled={b.status !== BACKUP_COMPLETED || restoring || instanceLive}
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
        </div>
      )}

      <DangerConfirm
        open={restoreTarget !== null}
        title={t('backups.confirmRestore')}
        description={t('backups.restoreWarning')}
        confirmLabel={t('backups.restore')}
        scope="group"
        pending={restoring}
        onConfirm={() => { if (restoreTarget) void handleRestore(restoreTarget) }}
        onCancel={() => setRestoreTarget(null)}
      />

      <DangerConfirm
        open={deleteTarget !== null}
        title={t('backups.deleteConfirm')}
        description={dependents > 0 ? t('backups.dependentsWarn', { count: dependents }) : t('common.irreversible')}
        confirmLabel={t('common.delete')}
        scope="group"
        pending={deleting}
        onConfirm={() => { if (deleteTarget) void handleDelete(deleteTarget) }}
        onCancel={() => setDeleteTarget(null)}
      />
    </Panel>
  )
}
