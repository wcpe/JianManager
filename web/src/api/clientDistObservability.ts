import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/** 时序区间枚举（无 from/to 时回退；与后端 range 枚举一致，FR-217 spec §5）。 */
export type ObservabilityRange = '24h' | '7d' | '30d' | '90d' | '180d'

/** 小时桶时序点（series[]，按 ts 升序；跨频道时同小时合并；缺数小时无点）。 */
export interface ObservabilitySeriesPoint {
  ts: string
  manifestPulls: number
  artifactPulls: number
  downloadBytes: number
  casHit: number
  casMiss: number
  activeMachines: number
  updateTotal: number
  updateSuccess: number
  updateFailStatic: number
  updateRolledBack: number
  updateError: number
}

/** 区间汇总标量 + 派生率（分母为 0 时率为 0）。 */
export interface ObservabilitySummary {
  manifestPulls: number
  artifactPulls: number
  downloadBytes: number
  casHit: number
  casMiss: number
  updateTotal: number
  updateSuccess: number
  updateFailStatic: number
  updateRolledBack: number
  updateError: number
  successRate: number
  failStaticRate: number
  rollbackRate: number
  casHitRate: number
  /**
   * 活跃客户端（machineId 去重，不可信仅近似，ADR-023）。
   * activeMachinesExact=true：区间在明细保留窗(14d)内，精确区间级独立数；
   * =false：区间超窗，各桶人次求和的近似上界（ADR-049 §4）。
   */
  activeMachines: number
  activeMachinesExact: boolean
}

/** 版本分布项（区间内跨桶合并，按 count 降序）。 */
export interface ObservabilityVersionDist {
  version: number
  count: number
}

/** 平台分布项（来源遥测 os，按 count 降序）。 */
export interface ObservabilityPlatformDist {
  os: string
  count: number
}

/** 版本滞后分布项（current_version - toVersion，按 lag 升序；0=已最新）。 */
export interface ObservabilityLagDist {
  lag: number
  count: number
}

/** 客户端分发观测视图（FR-217，见 ADR-049）。channelId 省略=跨频道总。 */
export interface ClientDistObservability {
  channelId: string
  from: string
  to: string
  series: ObservabilitySeriesPoint[]
  summary: ObservabilitySummary
  versionDist: ObservabilityVersionDist[]
  platformDist: ObservabilityPlatformDist[]
  lagDist: ObservabilityLagDist[]
}

/**
 * 客户端分发观测时序 + 分布 + 汇总（FR-217）。
 * 频道工作台统计 Tab（FR-219）传 channelId 取单频道；observability 监控页（FR-218）省略取总。
 * 与 FR-095 `/client-dist/stats`（按日看板）并存：本端点提供小时级时序 + 平台/滞后维度。
 */
export function useClientDistObservability(channelId: string | null, range: ObservabilityRange) {
  return useQuery({
    queryKey: ['client-dist-observability', channelId, range],
    queryFn: async () => {
      const { data } = await api.get<ClientDistObservability>('/client-dist/observability', {
        params: { channelId, range },
      })
      return data
    },
    enabled: !!channelId,
  })
}
