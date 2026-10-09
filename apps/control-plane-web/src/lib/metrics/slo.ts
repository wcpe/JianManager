/**
 * 可用性与 SLO 聚合的契约（FR-463）。
 *
 * 归包理由：可用性区块与它的纯逻辑格式化函数都要读它，应用侧 API 层改从包 import。
 */
export interface SLOResult {
  scope: 'platform' | 'node' | 'instance'
  availability: number
  totalSamples: number
  upSamples: number
  incidents: number
  activeIncidents: number
  mttrSeconds: number | null
  mtbfSeconds: number | null
  budgetAllowedSec: number
  budgetBurnedSec: number
  target: number
  approximatedBuckets: boolean
  /** false=窗口内无可用证据（分母为 0）：可用率与误差预算均不适用，前端显示「不适用」。 */
  applicable: boolean
}
