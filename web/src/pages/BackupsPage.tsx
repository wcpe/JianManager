import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Archive } from 'lucide-react'
import { useBackups, useCreateBackup, useDeleteBackup, useRestoreBackup, type BackupInfo } from '@/api/backups'
import { useBackupStorages } from '@/api/backupStorages'
import { useInstances } from '@/api/instances'
import { Panel } from '@/components/ui/panel'
import { StatusBadge } from '@/components/ui/status-badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import DangerConfirm from '@/components/DangerConfirm'
import {
  ConfigRow,
  ConfigViewToggle,
  ConfigSummaryChips,
  type ConfigView,
} from '@/pages/config-row'
import {
  backupStatusKey,
  backupStatusLevel,
  hasActiveBackup,
  summarizeBackups,
  countDependents,
  isIncrementalChild,
  formatSizeMb,
  BACKUP_COMPLETED,
  BACKUP_MODE_INCREMENTAL,
} from '@/pages/backups-view'

/** 进行中备份时的轮询间隔（毫秒）：刷新进度直至完成（FR-151）。 */
const ACTIVE_POLL_MS = 3000

/** 备份管理页：全量/增量（FR-056）+ 远程存储（FR-057）+ 进度轮询/汇总/链路依赖（FR-151）。 */
export default function BackupsPage() {
  const { t } = useTranslation()
  const [selectedInstance, setSelectedInstance] = useState<number | undefined>()
  const [storageId, setStorageId] = useState<number | undefined>()
  const [restoreTarget, setRestoreTarget] = useState<number | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<BackupInfo | null>(null)
  const [view, setView] = useState<ConfigView>('list')
  const { data: instances } = useInstances()
  const { data: storages } = useBackupStorages()

  // 先无轮询取一次以判定是否有进行中备份，再据此决定轮询间隔（FR-151）。
  const probe = useBackups(selectedInstance)
  const active = hasActiveBackup(probe.data ?? [])
  const { data: backups, isLoading } = useBackups(selectedInstance, {
    refetchInterval: active ? ACTIVE_POLL_MS : false,
  })

  const createBackup = useCreateBackup(selectedInstance ?? 0)
  const deleteBackup = useDeleteBackup()
  const restoreBackup = useRestoreBackup()

  const list = useMemo(() => backups ?? [], [backups])
  const summary = useMemo(() => summarizeBackups(list), [list])
  const backupById = useMemo(() => new Map(list.map((b) => [b.id, b])), [list])

  const storageName = (id?: number) =>
    id ? (storages ?? []).find((s) => s.id === id)?.name ?? `#${id}` : t('backups.localStorage', '本地')
  const parentName = (b: BackupInfo) =>
    b.parentId !== undefined ? backupById.get(b.parentId)?.name ?? `#${b.parentId}` : undefined

  const handleCreate = async (incremental: boolean) => {
    if (!selectedInstance) return
    try {
      await createBackup.mutateAsync({
        name: `${incremental ? 'inc' : 'full'}-${new Date().toISOString().slice(0, 19)}`,
        incremental,
        storageId,
      })
      toast.success(t('backups.creating', '创建中...'))
    } catch (e: unknown) {
      // 增量缺少基准时后端回 422 BUSINESS_ERROR。
      const msg = (e as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(msg || t('backups.createFailed', '创建备份失败'))
    }
  }

  const handleRestore = async (backupId: number) => {
    try {
      await restoreBackup.mutateAsync(backupId)
      toast.success(t('backups.restoring', '恢复中...'))
    } catch {
      toast.error(t('backups.restoreFailed', '恢复备份失败'))
    }
    setRestoreTarget(null)
  }

  const handleDelete = (backup: BackupInfo) => {
    deleteBackup.mutate(backup.id, {
      onSuccess: () => toast.success(t('common.deleted', '已删除')),
      onError: (e: unknown) => {
        const msg = (e as { response?: { data?: { message?: string } } })?.response?.data?.message
        toast.error(msg || t('backups.deleteFailed', '删除备份失败'))
      },
    })
  }

  // 删除前算直接依赖此备份的增量数，用于二次确认警告（FR-151）。
  const dependents = deleteTarget ? countDependents(list, deleteTarget.id) : 0

  const modeBadge = (b: BackupInfo) =>
    b.mode === BACKUP_MODE_INCREMENTAL ? (
      <StatusBadge level="info" label={t('backups.incremental', '增量')} dot={false} />
    ) : (
      <StatusBadge level="neutral" label={t('backups.full', '全量')} dot={false} />
    )

  const statusBadge = (b: BackupInfo) => (
    <StatusBadge
      level={backupStatusLevel(b.status)}
      label={t(`backups.${backupStatusKey(b.status)}`)}
      pulse={b.status !== BACKUP_COMPLETED && b.status !== 3}
    />
  )

  // 增量行的副信息：存储位置 + 父备份关系（链路可视，FR-151）。
  const rowSubtitle = (b: BackupInfo) => {
    const parts = [storageName(b.storageId)]
    if (isIncrementalChild(b)) parts.push(`${t('backups.basedOn', '基于')} ${parentName(b)}`)
    return parts.join(' · ')
  }

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <h1 className="text-2xl font-bold">{t('backups.title', '备份管理')}</h1>
        <div className="flex gap-2 flex-wrap items-center">
          <select
            className="p-2 border rounded bg-background text-sm"
            value={selectedInstance ?? ''}
            onChange={(e) => setSelectedInstance(e.target.value ? Number(e.target.value) : undefined)}
          >
            <option value="">{t('backups.selectInstance', '选择实例')}</option>
            {(instances ?? []).map((inst) => <option key={inst.id} value={inst.id}>{inst.name}</option>)}
          </select>
          <select
            className="p-2 border rounded bg-background text-sm"
            value={storageId ?? ''}
            onChange={(e) => setStorageId(e.target.value ? Number(e.target.value) : undefined)}
            title={t('backups.selectStorage', '存储位置')}
          >
            <option value="">{t('backups.localStorage', '本地')}</option>
            {(storages ?? []).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
          </select>
          {selectedInstance && (
            <ConfigViewToggle view={view} onChange={setView} cardLabel={t('common.cardView')} listLabel={t('common.listView')} />
          )}
          <Button onClick={() => handleCreate(false)} disabled={!selectedInstance || createBackup.isPending}>
            {t('backups.createFull', '全量备份')}
          </Button>
          <Button variant="outline" onClick={() => handleCreate(true)} disabled={!selectedInstance || createBackup.isPending}>
            {t('backups.createIncremental', '增量备份')}
          </Button>
        </div>
      </div>

      {!selectedInstance && <p className="text-muted-foreground">{t('backups.hint', '请先选择一个实例查看备份列表')}</p>}

      {selectedInstance && (
        <>
          {/* 汇总条（FR-151）：总占用 / 份数 / 最近成功；进行中时显轮询提示。 */}
          <div className="flex flex-wrap items-center gap-3">
            <ConfigSummaryChips
              chips={[
                { label: t('backups.summaryTotalSize'), value: formatSizeMb(summary.totalSizeMb) },
                { label: t('backups.summaryCount'), value: summary.count },
                {
                  label: t('backups.summaryLastSuccess'),
                  value: summary.lastSuccessAt ? new Date(summary.lastSuccessAt).toLocaleString() : t('backups.neverSuccess'),
                },
              ]}
            />
            {active && (
              <span className="inline-flex items-center gap-1.5 text-xs text-status-info">
                <span className="size-1.5 animate-pulse rounded-full bg-status-info" />
                {t('backups.autoRefreshing')}
              </span>
            )}
          </div>

          {isLoading && list.length === 0 ? (
            <p className="text-muted-foreground">{t('common.loading')}</p>
          ) : list.length === 0 ? (
            <Panel>
              <p className="py-6 text-center text-sm text-muted-foreground">{t('backups.empty', '暂无备份')}</p>
            </Panel>
          ) : view === 'card' ? (
            <div className="flex flex-col gap-2.5">
              {list.map((b) => {
                const dep = countDependents(list, b.id)
                return (
                  <ConfigRow
                    key={b.id}
                    icon={<Archive className="size-[18px]" />}
                    tone={backupStatusLevel(b.status) === 'neutral' ? 'primary' : backupStatusLevel(b.status)}
                    title={b.name}
                    subtitle={rowSubtitle(b)}
                    meta={
                      <>
                        <div>{formatSizeMb(b.fileSizeMb)}</div>
                        <div>{new Date(b.createdAt).toLocaleString()}</div>
                      </>
                    }
                    trailing={
                      <>
                        {modeBadge(b)}
                        {statusBadge(b)}
                        <Button
                          variant="ghost"
                          size="xs"
                          onClick={() => setRestoreTarget(b.id)}
                          disabled={b.status !== BACKUP_COMPLETED || restoreBackup.isPending}
                        >
                          {t('backups.restore', '恢复')}
                        </Button>
                        <Button
                          variant="ghost"
                          size="xs"
                          className="text-status-danger hover:text-status-danger"
                          onClick={() => setDeleteTarget(b)}
                          title={dep > 0 ? t('backups.dependentsWarn', { count: dep }) : undefined}
                        >
                          {t('common.delete', '删除')}
                        </Button>
                      </>
                    }
                  />
                )
              })}
            </div>
          ) : (
            <Panel bodyClassName="p-0">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('backups.name', '名称')}</TableHead>
                    <TableHead>{t('backups.mode', '模式')}</TableHead>
                    <TableHead>{t('backups.size', '大小')}</TableHead>
                    <TableHead>{t('backups.storageLocation', '存储位置')}</TableHead>
                    <TableHead>{t('backups.status', '状态')}</TableHead>
                    <TableHead>{t('backups.time', '时间')}</TableHead>
                    <TableHead className="text-right">{t('common.actions', '操作')}</TableHead>
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
                              {t('backups.basedOn', '基于')} {parentName(b)}
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
                            disabled={b.status !== BACKUP_COMPLETED || restoreBackup.isPending}
                          >
                            {t('backups.restore', '恢复')}
                          </Button>
                          <Button
                            variant="ghost"
                            size="xs"
                            className="text-status-danger hover:text-status-danger"
                            onClick={() => setDeleteTarget(b)}
                            title={countDependents(list, b.id) > 0 ? t('backups.dependentsWarn', { count: countDependents(list, b.id) }) : undefined}
                          >
                            {t('common.delete', '删除')}
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </Panel>
          )}
        </>
      )}

      <DangerConfirm
        open={restoreTarget !== null}
        title={t('backups.confirmRestore', '确认恢复此备份？')}
        description={t('backups.restoreWarning', '当前文件将被覆盖，此操作不可撤销。')}
        confirmLabel={t('backups.restore', '恢复')}
        scope="group"
        onConfirm={() => { if (restoreTarget) handleRestore(restoreTarget) }}
        onCancel={() => setRestoreTarget(null)}
      />

      <DangerConfirm
        open={deleteTarget !== null}
        title={t('backups.deleteConfirm', '确定删除此备份？')}
        description={dependents > 0 ? t('backups.dependentsWarn', { count: dependents }) : t('common.irreversible')}
        confirmLabel={t('common.delete', '删除')}
        scope="group"
        onConfirm={() => { if (deleteTarget) handleDelete(deleteTarget); setDeleteTarget(null) }}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}
