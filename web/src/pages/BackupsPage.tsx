import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useBackups, useCreateBackup, useDeleteBackup, useRestoreBackup, type BackupInfo } from '@/api/backups'
import { useBackupStorages } from '@/api/backupStorages'
import { useInstances } from '@/api/instances'
import { Badge } from '@/components/ui/badge'
import DangerConfirm from '@/components/DangerConfirm'

/** 备份状态码 → i18n key（与后端 model.BackupStatus 对齐：0 待处理 / 1 进行中 / 2 完成 / 3 失败）。 */
const STATUS_KEY: Record<number, string> = { 0: 'pending', 1: 'inProgress', 2: 'completed', 3: 'failed' }
const STATUS_VARIANT: Record<number, 'default' | 'secondary' | 'destructive' | 'outline'> = {
  0: 'outline', 1: 'secondary', 2: 'default', 3: 'destructive',
}

/** 备份管理页：支持全量/增量（FR-056）与远程存储位置（FR-057）。 */
export default function BackupsPage() {
  const { t } = useTranslation()
  const [selectedInstance, setSelectedInstance] = useState<number | undefined>()
  const [storageId, setStorageId] = useState<number | undefined>()
  const [restoreTarget, setRestoreTarget] = useState<number | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<BackupInfo | null>(null)
  const { data: instances } = useInstances()
  const { data: storages } = useBackupStorages()
  const { data: backups, isLoading } = useBackups(selectedInstance)
  const createBackup = useCreateBackup(selectedInstance ?? 0)
  const deleteBackup = useDeleteBackup()
  const restoreBackup = useRestoreBackup()

  const storageName = (id?: number) =>
    id ? (storages ?? []).find((s) => s.id === id)?.name ?? `#${id}` : t('backups.localStorage', '本地')

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

  const handleDelete = (backupId: number) => {
    deleteBackup.mutate(backupId, {
      onSuccess: () => toast.success(t('common.deleted', '已删除')),
      onError: (e: unknown) => {
        const msg = (e as { response?: { data?: { message?: string } } })?.response?.data?.message
        toast.error(msg || t('backups.deleteFailed', '删除备份失败'))
      },
    })
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <h1 className="text-2xl font-bold">{t('backups.title', '备份管理')}</h1>
        <div className="flex gap-2 flex-wrap items-center">
          <select
            className="p-2 border rounded bg-background"
            value={selectedInstance ?? ''}
            onChange={(e) => setSelectedInstance(e.target.value ? Number(e.target.value) : undefined)}
          >
            <option value="">{t('backups.selectInstance', '选择实例')}</option>
            {(instances ?? []).map((inst) => <option key={inst.id} value={inst.id}>{inst.name}</option>)}
          </select>
          <select
            className="p-2 border rounded bg-background"
            value={storageId ?? ''}
            onChange={(e) => setStorageId(e.target.value ? Number(e.target.value) : undefined)}
            title={t('backups.selectStorage', '存储位置')}
          >
            <option value="">{t('backups.localStorage', '本地')}</option>
            {(storages ?? []).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
          </select>
          <button
            className="px-4 py-2 bg-primary text-primary-foreground rounded-md hover:bg-primary/90 disabled:opacity-50"
            onClick={() => handleCreate(false)}
            disabled={!selectedInstance || createBackup.isPending}
          >
            {t('backups.createFull', '全量备份')}
          </button>
          <button
            className="px-4 py-2 border rounded-md hover:bg-accent disabled:opacity-50"
            onClick={() => handleCreate(true)}
            disabled={!selectedInstance || createBackup.isPending}
          >
            {t('backups.createIncremental', '增量备份')}
          </button>
        </div>
      </div>

      {!selectedInstance && <p className="text-muted-foreground">{t('backups.hint', '请先选择一个实例查看备份列表')}</p>}

      {selectedInstance && (
        <div className="border rounded-lg overflow-hidden">
          <table className="w-full">
            <thead className="bg-muted"><tr>
              <th className="p-3 text-left">{t('backups.name', '名称')}</th>
              <th className="p-3 text-left">{t('backups.mode', '模式')}</th>
              <th className="p-3 text-left">{t('backups.size', '大小')}</th>
              <th className="p-3 text-left">{t('backups.storageLocation', '存储位置')}</th>
              <th className="p-3 text-left">{t('backups.status', '状态')}</th>
              <th className="p-3 text-left">{t('backups.time', '时间')}</th>
              <th className="p-3 text-left">{t('common.actions', '操作')}</th>
            </tr></thead>
            <tbody>
              {(backups ?? []).map((b: BackupInfo) => (
                <tr key={b.id} className="border-t">
                  <td className="p-3">{b.name}</td>
                  <td className="p-3">
                    {b.mode === 1
                      ? <Badge variant="secondary">{t('backups.incremental', '增量')}{b.parentId ? ` · ${t('backups.chain', '备份链')}` : ''}</Badge>
                      : <Badge variant="outline">{t('backups.full', '全量')}</Badge>}
                  </td>
                  <td className="p-3">{b.fileSizeMb.toFixed(1)} MB</td>
                  <td className="p-3">{storageName(b.storageId)}</td>
                  <td className="p-3">
                    <Badge variant={STATUS_VARIANT[b.status] ?? 'outline'}>
                      {t(`backups.${STATUS_KEY[b.status] ?? 'pending'}`)}
                    </Badge>
                  </td>
                  <td className="p-3">{new Date(b.createdAt).toLocaleString()}</td>
                  <td className="p-3 flex gap-2">
                    <button
                      className="text-primary hover:underline disabled:opacity-50"
                      onClick={() => setRestoreTarget(b.id)}
                      disabled={b.status !== 2 || restoreBackup.isPending}
                    >
                      {t('backups.restore', '恢复')}
                    </button>
                    <button className="text-destructive hover:underline" onClick={() => handleDelete(b.id)}>
                      {t('common.delete', '删除')}
                    </button>
                  </td>
                </tr>
              ))}
              {(!backups || backups.length === 0) && !isLoading && (
                <tr><td colSpan={7} className="p-3 text-center text-muted-foreground">{t('backups.empty', '暂无备份')}</td></tr>
              )}
            </tbody>
          </table>
        </div>
      )}

      <DangerConfirm
        open={restoreTarget !== null}
        title={t('backups.confirmRestore', '确认恢复此备份？')}
        description={t('backups.restoreConfirm', '当前文件将被覆盖，此操作不可撤销。')}
        confirmLabel={t('backups.restore', '恢复')}
        scope="group"
        onConfirm={() => { if (restoreTarget) handleRestore(restoreTarget) }}
        onCancel={() => setRestoreTarget(null)}
      />

      <DangerConfirm
        open={deleteTarget !== null}
        title={t('backups.deleteConfirm', '确定删除此备份？')}
        description={t('common.irreversible')}
        confirmLabel={t('common.delete', '删除')}
        scope="group"
        onConfirm={() => { if (deleteTarget) deleteBackup.mutate(deleteTarget.id); setDeleteTarget(null) }}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}
