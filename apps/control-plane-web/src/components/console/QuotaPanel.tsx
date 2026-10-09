import { QuotaPanel as QuotaPanelView } from '@/components/views/console/QuotaPanel'
import { useInstanceQuota } from '@/api/quota'

/**
 * 实例「配额」区的应用接线层（ADR-097）。
 *
 * 视图本体已迁入组件库并受控；本层按实例 ID 取配额快照并把四态（加载 / 失败 / 无数据 / 有数据）
 * 与重试注入进去，保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function QuotaPanel({ instanceId }: { instanceId: number }) {
  const { data: quota, isLoading, isError, refetch } = useInstanceQuota(instanceId)

  return (
    <QuotaPanelView
      quota={quota}
      isLoading={isLoading}
      isError={isError}
      onRetry={() => void refetch()}
    />
  )
}
