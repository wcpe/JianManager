import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useUpdateInstance } from '@/api/instances'
import EditInstanceLimitsDialogView from '@/components/views/instances/EditInstanceLimitsDialog'

/**
 * 实例资源限额编辑器的应用接线层（ADR-097 b 范式）。
 *
 * 编辑器本体已迁入组件库并受控；本层只做持久化与提示。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function EditInstanceLimitsDialog({
  instanceId,
  instanceName,
  processType,
  cpuLimit,
  memLimitMb,
  diskLimitMb,
  onClose,
}: {
  instanceId: number
  instanceName: string
  /** 实例启动方式；仅 docker 模式资源限额生效（FR-079，ADR-019）。 */
  processType: string
  /** 当前 CPU 核数上限（0=不限制）。 */
  cpuLimit: number
  /** 当前内存上限（MiB，0=不限制）。 */
  memLimitMb: number
  /** 当前磁盘上限（MiB，0=不限制；v1 仅持久化展示）。 */
  diskLimitMb: number
  onClose: () => void
}) {
  const { t } = useTranslation()
  const update = useUpdateInstance()

  return (
    <EditInstanceLimitsDialogView
      instanceName={instanceName}
      processType={processType}
      cpuLimit={cpuLimit}
      memLimitMb={memLimitMb}
      diskLimitMb={diskLimitMb}
      saving={update.isPending}
      onClose={onClose}
      onSave={async (limits) => {
        try {
          await update.mutateAsync({ id: instanceId, body: limits })
          toast.success(t('instances.resourceLimitSaved'))
          return true
        } catch {
          return false
        }
      }}
    />
  )
}
