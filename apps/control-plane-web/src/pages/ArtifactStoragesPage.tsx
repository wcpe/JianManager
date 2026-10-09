// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做渠道取数、存量迁移轮询与写动作/toast 接线。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useArtifactStorages, useCreateArtifactStorage, useUpdateArtifactStorage, useDeleteArtifactStorage, useActivateArtifactStorage, useTestArtifactStorage, useTestArtifactStorageDraft, useArtifactMigrationStatus, useStartArtifactMigration, useArtifactMigrationFailures } from '@/api/artifactStorages'
import { useCancelTask } from '@/api/tasks'
import { ArtifactStoragesPageView } from '@/components/views/artifacts/ArtifactStoragesPageView'

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案（守卫/409/422 都用它呈现准确原因）。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * 文件存储配置页容器（ADR-097 b 范式）：渠道列表与存量迁移状态取数、创建/更新/删除/测试/
 * 设活跃/发起迁移/强制停止各写动作与 toast 文案都在这里决定，表格、四个对话框与删除二次确认
 * 交共享视图。
 *
 * 会触发重新取数的状态归本层：失败明细的打开状态同时是明细查询的启用键（打开才拉取），故
 * 由本层持有并经 `failuresTaskId` 注入；视图内的对话框开合、表单草稿与待确认渠道是纯 UI 状态。
 * 保留同路径默认导出，路由表与既有 DOM 测试无需改动。
 */
export default function ArtifactStoragesPage() {
  const { t } = useTranslation()
  const { data: channels, isLoading } = useArtifactStorages()
  const create = useCreateArtifactStorage()
  const update = useUpdateArtifactStorage()
  const del = useDeleteArtifactStorage()
  const activate = useActivateArtifactStorage()
  const testSaved = useTestArtifactStorage()
  const testDraft = useTestArtifactStorageDraft()

  // 存量迁移（FR-348）：最近迁移状态轮询 + 发起 + 强停 + 失败明细。
  const migrationStatus = useArtifactMigrationStatus()
  const startMigration = useStartArtifactMigration()
  const cancelTask = useCancelTask()
  const migTask = migrationStatus.data?.task ?? null
  /** 失败明细模态的打开状态；null = 关闭（打开才拉取明细）。 */
  const [failuresTaskId, setFailuresTaskId] = useState<string | null>(null)
  const migrationFailures = useArtifactMigrationFailures(failuresTaskId ?? undefined)

  return (
    <ArtifactStoragesPageView
      channels={channels}
      isLoading={isLoading}
      saving={create.isPending || update.isPending}
      testingDraft={testDraft.isPending}
      // 只有容器知道行内测试在测哪一行；未在测试时为 null，所有行都可点。
      rowTestingId={testSaved.isPending ? (testSaved.variables ?? null) : null}
      activating={activate.isPending}
      migrationTask={migTask}
      migrationInfo={migrationStatus.data?.migration ?? null}
      migrationStarting={startMigration.isPending}
      migrationCanceling={cancelTask.isPending}
      failuresTaskId={failuresTaskId}
      failures={migrationFailures.data}
      onSubmit={async (values, editingId) => {
        try {
          if (editingId !== null) {
            await update.mutateAsync({ id: editingId, ...values })
            toast.success(t('artifactStorages.updated', '已更新'))
          } else {
            await create.mutateAsync(values)
            toast.success(t('artifactStorages.created', '渠道已创建'))
          }
          // 成功：视图据此关窗并清空草稿。
          return true
        } catch (err) {
          toast.error(errMessage(err, editingId !== null
            ? t('artifactStorages.updateFailed', '更新渠道失败')
            : t('artifactStorages.createFailed', '创建渠道失败')))
          return false
        }
      }}
      onTestDraft={async (values, editingId) => {
        try {
          // 编辑态凭证留空：带 id 让后端复用存库凭证探测。
          const result = await testDraft.mutateAsync({ ...values, id: editingId ?? undefined })
          // 结论提示与弹窗内联回显同源，都取后端 message。
          if (result.ok) toast.success(result.message)
          else toast.error(result.message)
          return { ok: result.ok, message: result.message }
        } catch {
          const message = t('artifactStorages.testFailed', '测试连接失败')
          toast.error(message)
          return { ok: false, message }
        }
      }}
      onTest={(id) => {
        testSaved.mutate(id, {
          onSuccess: (result) => {
            if (result.ok) toast.success(result.message)
            else toast.error(result.message)
          },
          onError: () => toast.error(t('artifactStorages.testFailed', '测试连接失败')),
        })
      }}
      onActivate={(id) => {
        activate.mutate(id, {
          onSuccess: () => toast.success(t('artifactStorages.activated', '已设为活跃渠道，后续上传将落此渠道')),
          onError: (err: unknown) => toast.error(errMessage(err, t('artifactStorages.activateFailed', '设活跃失败'))),
        })
      }}
      onDelete={(id) => {
        del.mutate(id, {
          onSuccess: () => toast.success(t('common.deleted', '已删除')),
          // 删除守卫命中（内置/活跃/被制品引用）→ 用后端 message 呈现准确原因。
          onError: (err: unknown) => toast.error(errMessage(err, t('artifactStorages.deleteFailed', '删除失败'))),
        })
      }}
      onStartMigration={(targetChannelId) => {
        startMigration.mutate(targetChannelId, {
          onSuccess: () => toast.success(t('artifactStorages.migrate.started', '迁移任务已发起，进度见本页与任务中心')),
          // 409（已有在途）/ 422（目标探测失败）用后端 message 呈现准确原因。
          onError: (err: unknown) => toast.error(errMessage(err, t('artifactStorages.migrate.startFailed', '发起迁移失败'))),
        })
      }}
      onStopMigration={() => {
        if (!migTask) return
        cancelTask.mutate(migTask.taskId, {
          onSuccess: () => {
            toast.success(t('artifactStorages.migrate.stopRequested', '已请求停止，重新发起可续跑'))
            // 立即重取一次迁移状态，让进度卡尽快落到终态摘要。
            void migrationStatus.refetch()
          },
          onError: () => toast.error(t('artifactStorages.migrate.stopFailed', '停止失败')),
        })
      }}
      onOpenFailures={(taskId) => setFailuresTaskId(taskId)}
      onCloseFailures={() => setFailuresTaskId(null)}
    />
  )
}
