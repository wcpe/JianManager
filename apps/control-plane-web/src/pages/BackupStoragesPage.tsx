// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做列表取数、写动作与危险操作门禁接线。
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useBackupStorages, useCreateBackupStorage, useUpdateBackupStorage, useDeleteBackupStorage, useTestBackupStorage, useTestBackupStorageDraft } from '@/api/backupStorages'
import { BackupStoragesPageView } from '@/components/views/backups/BackupStoragesPageView'

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * 备份存储后端页容器（ADR-097 b 范式）：列表取数、创建/更新/删除/测试四个写动作、
 * 平台级删除门禁与 toast 文案都在这里决定，表格、弹窗与二次确认交共享视图。
 * 保留同路径默认导出，路由表无需改动。
 */
export default function BackupStoragesPage() {
  const { t } = useTranslation()
  const { data: storages, isLoading } = useBackupStorages()
  const create = useCreateBackupStorage()
  const update = useUpdateBackupStorage()
  const del = useDeleteBackupStorage()
  const testStorage = useTestBackupStorage()
  const testDraft = useTestBackupStorageDraft()
  // 删除是平台级破坏操作：角色门禁在应用侧判定后注入（包内不持鉴权状态）。

  return (
    <BackupStoragesPageView
      storages={storages}
      isLoading={isLoading}
      saving={create.isPending || update.isPending}
      testing={testDraft.isPending}
      // 只有容器知道行内测试在测哪一行；未在测试时为 null，所有行都可点。
      rowTestingId={testStorage.isPending ? (testStorage.variables ?? null) : null}
      onSubmit={async (values, editingId) => {
        try {
          if (editingId !== null) {
            await update.mutateAsync({ id: editingId, ...values })
            toast.success(t('backupStorages.updated', '已更新'))
          } else {
            await create.mutateAsync(values)
            toast.success(t('backupStorages.create', '创建'))
          }
          // 成功：视图据此关窗并清空草稿。
          return true
        } catch (err) {
          toast.error(errMessage(err, editingId !== null
            ? t('backupStorages.updateFailed', '更新存储后端失败')
            : t('backupStorages.createFailed', '创建存储后端失败')))
          return false
        }
      }}
      onTestDraft={async (values) => {
        try {
          const result = await testDraft.mutateAsync(values)
          // 结论提示与弹窗内联回显同源，都取后端 message。
          if (result.ok) toast.success(result.message)
          else toast.error(result.message)
          return result
        } catch {
          const message = t('backupStorages.testFailed', '测试连接失败')
          toast.error(message)
          return { ok: false, message }
        }
      }}
      onTest={(id) => {
        testStorage.mutate(id, {
          onSuccess: (result) => {
            if (result.ok) toast.success(result.message)
            else toast.error(result.message)
          },
          onError: () => toast.error(t('backupStorages.testFailed', '测试连接失败')),
        })
      }}
      onDelete={(id) => {
        del.mutate(id, {
          onSuccess: () => toast.success(t('common.deleted', '已删除')),
          onError: (err: unknown) => toast.error(errMessage(err, t('backupStorages.deleteFailed', '删除失败'))),
        })
      }}
    />
  )
}
