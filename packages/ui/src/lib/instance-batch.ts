/**
 * 实例批量操作契约（FR-058 / FR-139）。
 *
 * 归包理由：受控视图要按结果计数与逐项错误渲染提示与失败明细，这套结构是双侧共用的，
 * 应用侧 API 层改从包 import，避免两处各写一份。
 */

/** 批量操作动作。 */
export type InstanceBatchAction = 'command' | 'start' | 'stop' | 'restart' | 'kill'

/** 批量操作结果计数与逐项错误。 */
export interface InstanceBatchResult {
  action: string
  requested: number
  succeeded: number
  failed: number
  skipped: number
  errors: { instanceId: number; error: string }[]
}
