import api from '@/api/client'

/**
 * JBIS 业务对接：业务能力清单 + 业务命令下发（FR-116/FR-119，见 ADR-026/027）。
 * CP 插件无关，仅转发信封；具体业务语义由探针侧 per-plugin Provider 解释。
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

/**
 * 取某实例的业务能力清单（JBIS 元查询，GET /business/manifest）。
 * 成功时 output 形如 `{ domains: { economy: { actions: [...] } } }`。
 */
export async function fetchBusinessManifest(
  instanceId: number,
): Promise<BusinessResult<BusinessManifest>> {
  const { data } = await api.get<BusinessResult<BusinessManifest>>(
    `/instances/${instanceId}/business/manifest`,
  )
  return data
}

/**
 * 业务写动作的横切硬化参数（FR-121，见 ADR-029）。
 * 仅写动作（manifest `readOnly=false`）需要：CP 据此走高危写权限、注入幂等键/操作者上下文、记审计。
 */
export interface BusinessWriteOptions {
  /** 是否为写动作（前端据 manifest `readOnly` 取反）。 */
  write?: boolean
  /** 幂等标识：对同一逻辑操作的重试必须稳定（CP 用作 payload `taskId`，探针→插件幂等键）。 */
  operationId?: string
  /** 操作原因（可选，透传进插件流水 + JM 审计）。 */
  reason?: string
}

/**
 * 下发一条业务命令（POST /business）。
 * @param payload 结构化业务参数 JSON 字符串（CP 不解析，原样下发）。
 * @param opts 写动作的横切硬化参数（FR-121）；只读动作省略。
 */
export async function dispatchBusiness(
  instanceId: number,
  domain: string,
  action: string,
  payload: string,
  opts?: BusinessWriteOptions,
): Promise<BusinessResult> {
  const { data } = await api.post<BusinessResult>(`/instances/${instanceId}/business`, {
    domain,
    action,
    payload,
    write: opts?.write ?? false,
    operationId: opts?.operationId,
    reason: opts?.reason,
  })
  return data
}
