/**
 * 指标归因与容量预测的契约（FR-464 / FR-465）。
 *
 * 归包理由：归因卡与容量预测卡都要读它，应用侧 API 层改从包 import，避免两处各写一份。
 */

/** 一个归因因子。 */
export interface AttributionFactor {
  metricKey: string
  label: string
  correlation: number
  weight: number
  note?: string
}

/** 性能归因结果（FR-465）。status=insufficient 时 factors 为空，不伪造排序。 */
export interface AttributionResult {
  target: string
  window: { from: string; to: string }
  status: 'ok' | 'insufficient'
  tldr: string
  factors: AttributionFactor[]
  samples: number
}

/** 容量预测一行（FR-464）。 */
export interface ForecastResult {
  targetId: string
  metricKey: string
  nowValue: number
  limitValue: number
  slopePerSec: number
  exhaustAt: string | null
  exhaustLowDays: number | null
  exhaustHighDays: number | null
  confidence: 'high' | 'low' | 'insufficient'
  samples: number
  note?: string
}
