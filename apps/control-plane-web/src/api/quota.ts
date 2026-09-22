import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'
import { INSTANCE_QUERY_GC_TIME_MS } from '@/api/instances'

/**
 * 实例运行期配额（FR-467）：限额来源（实例级 > 组派生 > 不限）+ 实时用量 + 强制状态。
 * 限额的写入沿用既有路径（实例级走 PUT /instances/:id，组级走组配额端点）。
 */
export interface InstanceQuotaStatus {
  instanceId: number
  /** 实例 CPU 上限（核）；0=不限。 */
  cpuCores: number
  /** 实例内存上限（MiB）；0=不限。 */
  memLimitMb: number
  /** 实例磁盘上限（MiB）；0=不限。 */
  diskLimitMb: number
  /** 超限处置档位：alert|throttle|stop。 */
  enforceMode: string
  /** 限额来源：instance=实例级字段；group=组配额派生；none=不限。 */
  memSource: string
  diskSource: string
  cpuSource: string
  /** 派生组配额时所属组 ID；0=无组。 */
  groupId: number
  /** 实时用量（采样失败时为 0）。 */
  cpuPercent: number
  rssBytes: number
  diskBytes: number
  /** 无实时数据时的说明（如「实例未运行，无实时用量」）。 */
  sampleNote?: string
  /**
   * 当前是否处于已触发的强制状态（按维度）。
   *
   * **本进程内语义**（后端 s-2）：连续超限计数是 CP 内存态，进程重启后重新累积，
   * 故这些标志只反映「本进程观察到并已触发」；已达阈值但重启过的实例会显示 false
   * 直到重新累积。历史事实查审计 `instance.quota_*`。请结合 `enforceStateScope` 理解。
   */
  enforcedCpu: boolean
  enforcedMem: boolean
  enforcedDisk: boolean
  /**
   * 强制状态的作用域（后端固定回 `in_process`）。
   * 显式下发的目的是使调用方**不必猜测** `enforced*` 是否持久。
   */
  enforceStateScope: string
  /**
   * throttle 档登记的**待收紧限额**（M-1；0 = 无）。
   * 运行期收紧 docker cgroup 限额只能下次启动生效，故此处展示「已登记、下次启动生效」意图；
   * 恢复（超限解除）时后端按维度清零（R7），故有值即表示确有未生效的收紧待应用。
   */
  throttleCpuLimit: number
  throttleMemLimitMb: number
  /** 该实例是否支持内核级限流（仅 docker 模式）。 */
  supportedThrottle: boolean
}

export function useInstanceQuota(instanceId: number, enabled = true) {
  return useQuery({
    queryKey: ['instance-quota', instanceId],
    queryFn: () => api.get<InstanceQuotaStatus>(`/instances/${instanceId}/quota`).then((r) => r.data),
    enabled: enabled && instanceId > 0,
    gcTime: INSTANCE_QUERY_GC_TIME_MS,
  })
}
