// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做列表取数、进行中轮询、写动作与 toast 接线。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useBackups, useCreateBackup, useDeleteBackup, useRestoreBackup } from '@/api/backups'
import { useBackupStorages } from '@/api/backupStorages'
import { useInstance } from '@/api/instances'
import { InstancePicker } from '@/components/instances/InstancePicker'
import { hasActiveBackup } from '@/lib/backup'
import { BackupsPageView } from '@/components/views/backups/BackupsPageView'

/** 进行中备份时的轮询间隔（毫秒）：刷新进度直至完成（FR-151）。 */
const ACTIVE_POLL_MS = 3000

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案（增量缺基准 422 / 实例未停止 409 都靠它透传）。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * 备份管理页容器（ADR-097 b 范式）：备份列表取数与进行中轮询、实例选择、创建/恢复/删除三个
 * 写动作与 toast 文案都在这里决定，表格、卡片、视图切换与两次二次确认交共享视图。
 *
 * 受控状态归属：`instanceId` 是备份列表与实例详情查询的键（一变即重新取数），故归本层；
 * 视图内的卡片/列表切换、创建目标存储位置与两个确认框开合是纯 UI 状态，留包内。
 * 保留同路径默认导出，路由表（route-chunks 惰性加载）与既有 DOM 测试无需改动。
 */
export default function BackupsPage() {
  const { t } = useTranslation()
  /** 当前选中实例；null = 未选（不发起备份列表与实例详情查询）。 */
  const [instanceId, setInstanceId] = useState<number | null>(null)
  const { data: storages } = useBackupStorages()
  // 当前选中实例详情（恢复守卫需其状态；深链/搜索翻页后仍可解析名称与状态）。
  const { data: selectedInst } = useInstance(instanceId ?? 0)

  // 先无轮询取一次以判定是否有进行中备份，再据此决定轮询间隔（FR-151）。
  const probe = useBackups(instanceId ?? undefined)
  const active = hasActiveBackup(probe.data ?? [])
  const { data: backups, isLoading } = useBackups(instanceId ?? undefined, {
    refetchInterval: active ? ACTIVE_POLL_MS : false,
  })

  const createBackup = useCreateBackup(instanceId ?? 0)
  const deleteBackup = useDeleteBackup()
  const restoreBackup = useRestoreBackup()

  // 实例进程可能存活（STARTING/RUNNING/STOPPING）时禁止恢复，与后端恢复守卫一致：
  // 运行中的服务器下次自动存档会覆盖掉刚恢复的文件，恢复会静默失效。
  const instanceLive = !!selectedInst && ['STARTING', 'RUNNING', 'STOPPING'].includes(selectedInst.status)

  return (
    <BackupsPageView
      instanceId={instanceId}
      onInstanceChange={setInstanceId}
      storages={storages ?? []}
      backups={backups ?? []}
      isLoading={isLoading}
      backupsActive={active}
      instanceLive={instanceLive}
      creating={createBackup.isPending}
      restoring={restoreBackup.isPending}
      // 实例候选走服务端搜索（千级实例不能一次拉全量）：防抖、候选窗口与请求时机都在接线层。
      // valueLabel 保证已选实例不在候选窗口内时触发器仍显示名称，而非退化成裸 id。
      renderInstancePicker={({ value, onChange }) => (
        <InstancePicker
          className="w-52"
          value={value}
          onChange={onChange}
          valueLabel={selectedInst?.name}
          placeholder={t('backups.selectInstance', '选择实例')}
        />
      )}
      onCreate={({ incremental, storageId }) => {
        if (!instanceId) return
        createBackup.mutate(
          {
            name: `${incremental ? 'inc' : 'full'}-${new Date().toISOString().slice(0, 19)}`,
            incremental,
            storageId,
          },
          {
            onSuccess: () => toast.success(t('backups.creating', '创建中...')),
            // 增量缺少基准时后端回 422 BUSINESS_ERROR，透传定向提示。
            onError: (err: unknown) => toast.error(errMessage(err, t('backups.createFailed', '创建备份失败'))),
          },
        )
      }}
      onRestore={async (backupId) => {
        try {
          await restoreBackup.mutateAsync(backupId)
          toast.success(t('backups.restoring', '恢复中...'))
        } catch (err: unknown) {
          // 实例未停止时后端回 409 INSTANCE_NOT_STOPPED，透传定向提示。错误在此吞掉：
          // 视图据 Promise 结束关窗，与原页「成败都关窗」一致。
          toast.error(errMessage(err, t('backups.restoreFailed', '恢复备份失败')))
        }
      }}
      onDelete={(backupId) => {
        deleteBackup.mutate(backupId, {
          onSuccess: () => toast.success(t('common.deleted', '已删除')),
          onError: (err: unknown) => toast.error(errMessage(err, t('backups.deleteFailed', '删除备份失败'))),
        })
      }}
    />
  )
}
