import { useCapacityForecast } from '@/api/metrics'
import { CapacityForecastCard as CapacityForecastCardView } from '@/components/views/instances/CapacityForecastCard'
import type { MetricRange } from '@jianmanager/ui'

/**
 * 容量预测卡的应用接线层（ADR-097 a 范式）。
 *
 * 卡片本体是受控视图（见 components/views）；本层按 scope/targetId/range 取预测。
 * 保留同路径的导出与同一套 props，调用点无需改动。
 */
export function CapacityForecastCard({
  scope,
  targetId,
  range,
  metrics,
}: {
  /** 目标维度：node（节点资源）或 instance（实例堆内存）。 */
  scope: 'node' | 'instance'
  /** 目标 UUID：node 维度为节点 UUID，instance 维度为实例 UUID。 */
  targetId: string
  /** 统计窗口（同时决定样本档位；7d 为推荐值）。 */
  range: MetricRange
  /** 待预测的「已用」指标键；缺省由后端按 scope 取默认集合。 */
  metrics?: string[]
}) {
  const { data, isError, isLoading } = useCapacityForecast({ scope, targetId, range, metrics })

  return (
    <CapacityForecastCardView
      forecasts={data?.forecasts ?? []}
      loading={isLoading}
      error={isError}
    />
  )
}
