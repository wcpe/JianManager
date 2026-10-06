/**
 * 业务动作（JBIS）的核心契约（FR-125/126/127）。
 *
 * 归包理由：背包/经济等业务视图要读业务结果与能力清单，应用侧 API 层改从包 import，
 * 避免两处各写一份。实现（fetchBusinessManifest / dispatchBusiness）仍留应用侧——
 * 它们要拿 axios 实例与鉴权。
 */

/** 一个业务动作的能力声明（来自探针 Provider 的 manifest）。 */
export interface BusinessAction {
  /** 动作名（如 balance）。 */
  action: string
  /** 入参名列表（如 ["player","currency"]），前端据此渲染表单。 */
  args?: string[]
  /** 是否只读（仅展示，不影响下发）。 */
  readOnly?: boolean
}

/** 业务能力清单：域 → 该域的动作列表。 */
export interface BusinessManifest {
  domains?: Record<string, { actions?: BusinessAction[] }>
}

/** 一次业务调用 / 元查询的结果（与后端 service.BusinessResult 对应）。 */
export interface BusinessResult<T = unknown> {
  instanceId: number
  domain: string
  action: string
  /** 探针在线 + 执行成功。false 时 output 为 null、error 给原因。 */
  available: boolean
  /** 业务结果原始 JSON（探针透传，CP 不解析）；不可得时 null。 */
  output: T | null
  /** 降级 / 失败原因（探针未连 / 域不可用 / 执行失败），成功时空。 */
  error?: string
}

/** 业务写动作的审计选项。 */
export interface BusinessWriteOptions {
  /** 标记为写操作（后端据此要求审计理由）。 */
  write?: boolean
  /** 幂等键：同一次意图重试须复用。 */
  operationId?: string
  reason?: string
}
