import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useInstanceEnv, useUpdateInstance } from '@/api/instances'
import InstanceEnvSegmentView from '@/components/views/instances/InstanceEnvSegment'

/**
 * 环境变量页签的应用接线层（ADR-097 b 范式）。
 *
 * 页签本体已迁入组件库并受控（不取数、不弹 toast）；本层取三份环境数据并承接保存与提示。
 * 保留同路径的默认导出，调用点无需改动。
 */
export default function InstanceEnvSegment({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()
  const { data: env } = useInstanceEnv(instanceId)
  const update = useUpdateInstance()

  return (
    <InstanceEnvSegmentView
      configured={env?.configured}
      runtime={env?.runtime}
      runtimeAvailable={env?.runtimeAvailable}
      note={env?.note}
      saving={update.isPending}
      onSave={async (envVars) => {
        try {
          await update.mutateAsync({ id: instanceId, body: { envVars } })
          toast.success(t('env.saved'))
          return true
        } catch {
          return false
        }
      }}
    />
  )
}
