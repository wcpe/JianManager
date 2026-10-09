import { useSLO } from '@/api/metrics'
import { SLOSection as SLOSectionView } from '@/components/views/instances/SLOSection'
import type { MetricRange } from '@jianmanager/ui'

// 纯逻辑格式化函数与配色判定原样再导出（其他页面与测试直接引用）。
export { availabilityLevel, budgetBurnRatio, fmtAvailability, fmtDuration } from '@/components/views/instances/SLOSection'

/**
 * 可用性区块的应用接线层（ADR-097 a 范式）。
 *
 * 区块本体是受控视图（见 components/views）；本层按 scope/targetId/range 取 SLO 结果。
 * 保留同路径的导出与同一套 props，调用点无需改动。
 */
export function SLOSection({
  range,
  scope = 'platform',
  targetId,
}: {
  /** 统计窗口（同时决定是否走降采样桶近似）。 */
  range: MetricRange
  /** 统计维度；缺省平台级汇总。 */
  scope?: 'platform' | 'node' | 'instance'
  /** node/instance 维度的目标 UUID（节点 UUID 或实例 UUID）；平台维度必须缺省。 */
  targetId?: string
}) {
  const { data, isError, isLoading } = useSLO({ scope, targetId, range })

  return <SLOSectionView data={data} loading={isLoading} error={isError} />
}
