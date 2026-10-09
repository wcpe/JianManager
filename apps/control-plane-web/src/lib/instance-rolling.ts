import type { InstanceBatchAction } from './instance-batch'

/**
 * 实例滚动/分批/灰度编排契约（FR-457）。
 *
 * 归包理由：受控视图要按会话状态渲染进度与控制按钮（`RollingOp` / `RollingState`）、
 * 按状态判定「是否仍在推进」（`isRollingActive`），这套结构是双侧共用的；应用侧 API 层
 * 改从包 import，避免两处各写一份。
 */

/** 编排会话状态。 */
export type RollingState = 'pending' | 'running' | 'paused' | 'done' | 'canceled'

/** 单条失败明细。 */
export interface RollingError {
  instanceId: number
  error: string
}

/** 滚动编排会话（含进度）。 */
export interface RollingOp {
  id: number
  action: string
  command?: string
  batchSize: number
  batchIntervalSec: number
  failFast: boolean
  ratio: number
  targets: number[]
  cursor: number
  state: RollingState
  requested: number
  succeeded: number
  failed: number
  skipped: number
  errors: RollingError[]
  createdAt: string
  updatedAt: string
}

/** 编排控制动作（暂停/继续/取消）。 */
export type RollingControlAction = 'pause' | 'resume' | 'cancel'

/** 会话是否仍在推进（据此决定进度视图是否继续轮询、是否显示取消）。 */
export function isRollingActive(state: RollingState | undefined): boolean {
  return state === 'pending' || state === 'running' || state === 'paused'
}

/**
 * 创建编排的载荷（视图产出，外壳透传给 API）。
 *
 * 目标固定走 `filter.instanceIds` 而非 `ids`：后端灰度的 ratio 抽样**仅在 filter 模式生效**，
 * 若发 ids 又带 ratio，会静默退化为全量（安全方向反转）。
 */
export interface RollingCreatePayload {
  action: InstanceBatchAction
  filter: { instanceIds: number[] }
  command?: string
  batchSize: number
  batchIntervalSec: number
  failFast: boolean
  /** 灰度比例：0 表示全量，取值 (0,1) 触发抽样。 */
  ratio: number
}
