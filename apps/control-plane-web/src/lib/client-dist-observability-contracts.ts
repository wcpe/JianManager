/**
 * 客户端分发可观测性契约（FR-217 / FR-428，ADR-097）。
 * 与后端 model 对齐的纯数据类型，供 `@jianmanager/ui` 内视图逻辑与应用侧 api 共用。
 */

/** 观测汇总标量（区间内跨频道/单频道合并；率为 0~1 小数）。 */
export interface ClientDistObservabilitySummary {
  manifestPulls: number
  artifactPulls: number
  downloadBytes: number
  updateTotal: number
  updateSuccess: number
  updateFailStatic: number
  updateRolledBack: number
  updateError: number
  successRate: number
  failStaticRate: number
  rollbackRate: number
  activeMachines: number
  /** 区间在明细保留窗(14d)内=精确去重独立数 true；窗外=各桶人次求和近似 false（ADR-049）。 */
  activeMachinesExact: boolean
}

/** FR-428：前一等长窗口的同环比基数（不含率值与 exact 标记；率由消费方现算）。 */
export interface ClientDistObservabilityCompare {
  manifestPulls: number
  artifactPulls: number
  downloadBytes: number
  updateTotal: number
  updateSuccess: number
  updateFailStatic: number
  updateRolledBack: number
  updateError: number
  activeMachines: number
}

/** 小时桶时序点（series[]，按 ts 升序；跨频道时同小时合并；缺数小时无点）。 */
export interface ObservabilitySeriesPoint {
  ts: string
  manifestPulls: number
  artifactPulls: number
  downloadBytes: number
  activeMachines: number
  updateTotal: number
  updateSuccess: number
  updateFailStatic: number
  updateRolledBack: number
  updateError: number
}
