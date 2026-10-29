import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/** 
 * 业务插件域状态（见 docs/specs/business-plugin-panel/spec.md）。
 *
 * **只渲染不解释**：本模块只按 OpenAPI 契约（`BusinessStatus` / `DomainStatus`）取回探针上报的
 * 原始状态，不做任何业务语义归类、不解析 manifest 内容。`manifestDigest` 只是内容指纹。
 */

/** 单个业务域的运行状态，字段对齐 OpenAPI `DomainStatus`。 */
export interface DomainStatus {
  /** 域名（插件自注册时的标识，平台不解释其含义）。 */
  domain: string
  /** 该域当前是否可用。 */
  available: boolean
  /** 不可用原因（如 `PLUGIN_DISABLED` / `INIT_FAILED`），原样来自探针。 */
  degradedReason?: string
  /** manifest 内容指纹；先用它判断是否变化，再决定是否拉全量。 */
  manifestDigest?: string
  /** 最近一次 dispatch 时间（epoch 秒），0 表示从未。 */
  lastDispatchAt: number
  /** 自探针启动以来的累计调用数（探针重启归零）。 */
  dispatchCount: number
  /** 累计失败数。 */
  failureCount: number
}

/** 实例的业务域注册状态，字段对齐 OpenAPI `BusinessStatus`。 */
export interface BusinessStatus {
  instanceId: number
  instanceName: string
  /** 探针当前是否连入。 */
  probeConnected: boolean
  /** 这份数据的采集时刻（RFC3339）；`stale` 时是上次成功的时刻。 */
  fetchedAt?: string
  /** `true` 表示探针不可达，返回的是最后一次已知状态。 */
  stale: boolean
  /** 已注册域清单（后端按域名排序，保证渲染稳定）。 */
  domains: DomainStatus[]
  /** 降级原因（节点未连 / 查询失败）——`stale` 时给出，便于面板提示。 */
  error?: string
}

/** 取某实例的业务域状态（GET /instances/:id/business/status）。 */
export async function fetchBusinessStatus(instanceId: number): Promise<BusinessStatus> {
  const { data } = await api.get<BusinessStatus>(`/instances/${instanceId}/business/status`)
  return data
}

/** 
 * 业务域状态的查询 hook。
 *
 * 缓存 TTL 与后端一致取 30 秒（spec §6）：后端在这段时间内直接命中内存缓存、不打探针，
 * 前端再短只会把同样的旧数据多要几遍；再长则失去「探针断连立即失效」的时效。
 * `retry: false`：接口即便探针不可达也回 200 + `stale=true`（面板还有东西可渲染），
 * 走到错误分支说明是权限/实例不存在这类不该重试的失败。
 */
export function useBusinessStatus(instanceId: number, enabled = true) {
  return useQuery({
    queryKey: ['business-status', instanceId],
    queryFn: () => fetchBusinessStatus(instanceId),
    enabled: enabled && instanceId > 0,
    staleTime: 30_000,
    retry: false,
  })
}

/** 批量域状态查询的单次上限（后端 spec§5.2）。 */
export const BUSINESS_STATUS_BATCH_MAX = 50

/** 批量结果里单个实例的条目。 */
export interface BusinessStatusBatchItem {
  instanceId: number
  /** 实例不存在或无权访问（存在性隐藏：两者刻意不区分）。 */
  notFound: boolean
  status?: BusinessStatus
}

/** 
 * 批量取多个实例的业务域状态（POST /instances/business/status/batch）。
 *
 * 逐实例独立成败：某台 `notFound=true` 不影响其他台。用于实例列表的
 * 「业务插件」汇总列——每行各发一次请求会打爆探针，故走批量。
 *
 * 超过 50 台会返回 400 `BATCH_TOO_LARGE`，调用方应分批。
 */
export async function fetchBusinessStatusBatch(
  instanceIds: number[],
): Promise<BusinessStatusBatchItem[]> {
  if (instanceIds.length === 0) return []
  const { data } = await api.post<{ results: BusinessStatusBatchItem[] }>(
    '/instances/business/status/batch',
    { instanceIds },
  )
  return data.results ?? []
}

/** 
 * 批量域状态的查询 hook。
 *
 * `staleTime` 与单实例保持一致（30s）；按 id 集合排序后作为 queryKey 的一部分，
 * 避免列表顺序变化导致缓存击穿。
 */
export function useBusinessStatusBatch(instanceIds: number[], enabled = true) {
  // 去重 + 升序：同一集合的不同排列应命中同一份缓存。
  const ids = Array.from(new Set(instanceIds)).sort((a, b) => a - b)
  return useQuery({
    queryKey: ['business-status-batch', ids],
    queryFn: () => fetchBusinessStatusBatch(ids),
    enabled: enabled && ids.length > 0,
    staleTime: 30_000,
    retry: false,
  })
}
