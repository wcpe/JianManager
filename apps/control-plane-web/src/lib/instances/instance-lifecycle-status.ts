import type { StatusLevel } from '@jianmanager/ui'

export type InstanceLifecycleAction = 'start' | 'stop' | 'restart'
export type InstanceLifecycleKind = 'running' | 'starting' | 'stopping' | 'stopped' | 'awaiting_confirmation' | 'failed' | 'provisioning' | 'notice' | 'submitting'
export interface LifecycleInstanceLike { status: string; statusReason?: string }
export interface InstanceLifecyclePresentation {
  kind: InstanceLifecycleKind
  tone: StatusLevel
  detail: string
  /** 默认阶段说明需要翻译；后端原文没有此键。 */
  detailKey?: string
  labelKey: string
  labelFallback: string
  transitioning: boolean
}

/** 只有 CP 明确标记的待确认前缀才有此语义，不能用任意“超时/失败/待确认”子串推断。 */
const AWAITING_CONFIRMATION_PREFIX = /^操作结果待确认(?:[：:,，]|\s|$)/
const OPERATION_FAILURE_PREFIX = /^操作失败，状态待心跳收敛:/
const START_FAILURE_PREFIX = /^启动失败[：:]/

export function instanceLifecyclePresentation(
  inst: LifecycleInstanceLike,
  options: { provisioning?: boolean; pendingAction?: InstanceLifecycleAction } = {},
): InstanceLifecyclePresentation {
  const detail = inst.statusReason?.trim() ?? ''
  const base = { labelKey: `instances.${inst.status.toLowerCase()}`, labelFallback: inst.status }
  if (options.pendingAction) {
    const action = options.pendingAction
    const labelFallback = action === 'start' ? '启动请求提交中' : action === 'stop' ? '停止请求提交中' : '重启请求提交中'
    return {
      kind: 'submitting', tone: 'warning', transitioning: true,
      labelKey: `instances.lifecycle.submitting_${action}`, labelFallback,
      detail: '正在提交请求，等待控制面受理', detailKey: 'instances.lifecycle.submittingDetail',
    }
  }
  if (AWAITING_CONFIRMATION_PREFIX.test(detail)) {
    return {
      kind: 'awaiting_confirmation', tone: 'warning', detail, transitioning: true,
      labelKey: 'instances.lifecycle.awaitingConfirmation', labelFallback: '操作结果待确认',
    }
  }
  // 后端已明确拒绝操作时，保留的 STARTING/STOPPING 只代表运行态仍待观测，不能盖掉操作失败。
  if (OPERATION_FAILURE_PREFIX.test(detail) || START_FAILURE_PREFIX.test(detail)) {
    const operationFailure = OPERATION_FAILURE_PREFIX.test(detail)
    return {
      ...base,
      kind: 'failed', tone: 'danger', detail, transitioning: false,
      labelKey: operationFailure ? 'instances.lifecycle.operationFailed' : 'instances.lifecycle.startFailed',
      labelFallback: operationFailure ? '操作失败（运行状态待确认）' : '启动失败',
    }
  }
  if (options.provisioning || (inst.status === 'DAMAGED' && detail.startsWith('重建中'))) {
    return {
      kind: 'provisioning', tone: 'warning', detail, transitioning: true,
      labelKey: 'instances.lifecycle.provisioning', labelFallback: '搭建/重建进行中',
    }
  }
  if (inst.status === 'STARTING' || inst.status === 'STOPPING') {
    const starting = inst.status === 'STARTING'
    return {
      ...base, kind: starting ? 'starting' : 'stopping', tone: 'warning', transitioning: true,
      detail: detail || (starting ? '正在启动，等待节点状态确认' : '正在停止，等待节点状态确认'),
      detailKey: detail ? undefined : starting ? 'instances.lifecycle.startingDetail' : 'instances.lifecycle.stoppingDetail',
    }
  }
  // CRASHED/DAMAGED 是确定故障；STOPPED 的状态原因还可能是配额、回滚、巡检等信息，不能统称启动失败。
  if (inst.status === 'CRASHED' || inst.status === 'DAMAGED') {
    return { ...base, kind: 'failed', tone: 'danger', detail, transitioning: false }
  }
  if (detail) return { ...base, kind: 'notice', tone: 'warning', detail, transitioning: false }
  if (inst.status === 'RUNNING') return { ...base, kind: 'running', tone: 'success', detail: '', transitioning: false }
  return { ...base, kind: 'stopped', tone: 'neutral', detail: '', transitioning: false }
}
