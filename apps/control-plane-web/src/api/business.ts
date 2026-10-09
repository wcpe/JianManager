import api from '@/api/client'

/**
 * JBIS 业务对接：业务能力清单 + 业务命令下发（FR-116/FR-119，见 ADR-026/027）。
 * CP 插件无关，仅转发信封；具体业务语义由探针侧 per-plugin Provider 解释。
 */

// 业务动作契约已回迁应用侧（受控视图与业务页面共用，ADR-097）；此处原样再导出，调用点无需改动。
// 本地绑定供本文件的函数签名使用。
import type {
  BusinessManifest,
  BusinessResult,
  BusinessWriteOptions,
} from '@/lib/business'
export type {
  BusinessAction,
  BusinessManifest,
  BusinessResult,
  BusinessWriteOptions,
} from '@/lib/business'

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
// `BusinessWriteOptions` 的定义已随契约归包（见上方 re-export）。

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
