import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useBackups, useCreateBackup, useDeleteBackup, useRestoreBackup, type BackupInfo } from '@/api/backups'
import { useInstances } from '@/api/instances'
import ConfirmDialog from '@/components/ConfirmDialog'

export default function BackupsPage() {
  const { t } = useTranslation()
  const [selectedInstance, setSelectedInstance] = useState<number | undefined>()
  const [restoreTarget, setRestoreTarget] = useState<number | null>(null)
  const { data: instances } = useInstances()
  const { data: backups, isLoading } = useBackups(selectedInstance)
  const createBackup = useCreateBackup()
  const deleteBackup = useDeleteBackup()
  const restoreBackup = useRestoreBackup()

  const handleCreate = async () => {
    if (!selectedInstance) return
    await createBackup.mutateAsync({ instanceId: selectedInstance, name: `backup-${new Date().toISOString().slice(0, 19)}` })
  }

  const handleRestore = async (backupId: number) => {
    await restoreBackup.mutateAsync(backupId)
    setRestoreTarget(null)
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold">{t('backups.title', '备份管理')}</h1>
        <div className="flex gap-2">
          <select className="p-2 border rounded" value={selectedInstance ?? ''} onChange={e => setSelectedInstance(e.target.value ? Number(e.target.value) : undefined)}>
            <option value="">{t('backups.selectInstance', '选择实例')}</option>
            {(instances ?? []).map((inst) => <option key={inst.id} value={inst.id}>{inst.name}</option>)}
          </select>
          <button className="px-4 py-2 bg-primary text-primary-foreground rounded-md hover:bg-primary/90 disabled:opacity-50" onClick={handleCreate} disabled={!selectedInstance || createBackup.isPending}>
            {t('backups.create', '创建备份')}
          </button>
        </div>
      </div>

      {!selectedInstance && <p className="text-muted-foreground">{t('backups.hint', '请先选择一个实例查看备份列表')}</p>}

      {selectedInstance && (
        <div className="border rounded-lg overflow-hidden">
          <table className="w-full">
            <thead className="bg-muted"><tr>
              <th className="p-3 text-left">{t('backups.name', '名称')}</th>
              <th className="p-3 text-left">{t('backups.size', '大小')}</th>
              <th className="p-3 text-left">{t('backups.type', '类型')}</th>
              <th className="p-3 text-left">{t('backups.status', '状态')}</th>
              <th className="p-3 text-left">{t('backups.time', '时间')}</th>
              <th className="p-3 text-left">{t('common.actions', '操作')}</th>
            </tr></thead>
            <tbody>
              {(backups ?? []).map((b: BackupInfo) => (
                <tr key={b.id} className="border-t">
                  <td className="p-3">{b.name}</td>
                  <td className="p-3">{b.fileSizeMb.toFixed(1)} MB</td>
                  <td className="p-3">{b.type === 0 ? t('backups.manual', '手动') : t('backups.auto', '自动')}</td>
                  <td className="p-3">{b.status === 0 ? t('backups.ready', '就绪') : b.status === 1 ? t('backups.creating', '创建中') : t('backups.failed', '失败')}</td>
                  <td className="p-3">{new Date(b.createdAt).toLocaleString()}</td>
                  <td className="p-3 flex gap-2">
                    <button className="text-primary hover:underline disabled:opacity-50" onClick={() => setRestoreTarget(b.id)} disabled={b.status !== 0 || restoreBackup.isPending}>{t('backups.restore', '恢复')}</button>
                    <button className="text-destructive hover:underline" onClick={() => deleteBackup.mutate(b.id)}>{t('common.delete', '删除')}</button>
                  </td>
                </tr>
              ))}
              {(!backups || backups.length === 0) && !isLoading && <tr><td colSpan={6} className="p-3 text-center text-muted-foreground">{t('backups.empty', '暂无备份')}</td></tr>}
            </tbody>
          </table>
        </div>
      )}

      <ConfirmDialog
        open={restoreTarget !== null}
        title={t('backups.confirmRestore', '确认恢复此备份？')}
        description="当前文件将被覆盖，此操作不可撤销。"
        confirmLabel={t('backups.restore', '恢复')}
        onConfirm={() => { if (restoreTarget) handleRestore(restoreTarget) }}
        onCancel={() => setRestoreTarget(null)}
      />
    </div>
  )
}
