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
  updateTotal: number
  updateSuccess: number
  updateFailStatic: number
  updateRolledBack: number
  updateError: number
  successRate: number
  failStaticRate: number
  rollbackRate: number
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

/** FR-428：前一等长窗口的同环比基数（缺省=后端未升级，前端不渲染同环比）。 */
export interface ObservabilityCompare {
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

/** 客户端分发观测视图（FR-217，见 ADR-049）。channelId 省略=跨频道总。 */
export interface ClientDistObservability {
  channelId: string
  from: string
  to: string
  series: ObservabilitySeriesPoint[]
  summary: ObservabilitySummary
  /** FR-428：前一等长窗口同环比基数（缺省=后端未升级，前端不渲染同环比）。 */
  compare?: ObservabilityCompare
  versionDist: ObservabilityVersionDist[]
  platformDist: ObservabilityPlatformDist[]
  lagDist: ObservabilityLagDist[]
}

/** FR-425：观测窗口 = 预设档 或 任意起止（RFC3339，后端 parseObsRange 已支持）。 */
export type ObservabilityWindow = { range: ObservabilityRange } | { from: string; to: string }

/**
 * 客户端分发观测时序 + 分布 + 汇总（FR-217）。
 * 频道工作台统计 Tab（FR-219）传 channelId 取单频道；observability 监控页（FR-218）省略取总。
 * 与 FR-095 `/client-dist/stats`（按日看板）并存：本端点提供小时级时序 + 平台/滞后维度。
 */
export function useClientDistObservability(channelId: string | null, window: ObservabilityWindow | ObservabilityRange = { range: '7d' }) {
  // 兼容旧调用形态（裸 '7d' 字符串）与新对象形态 { range } | { from, to }。
  const w: ObservabilityWindow = typeof window === 'string' ? { range: window } : window
  const from = 'from' in w ? w.from : undefined
  const to = 'to' in w ? w.to : undefined
  const range = 'range' in w ? w.range : undefined
  return useQuery({
    queryKey: ['client-dist-observability', channelId, range ?? '', from ?? '', to ?? ''],
    queryFn: async () => {
      const { data } = await api.get<ClientDistObservability>('/client-dist/observability', {
        params: {
          ...(channelId ? { channelId } : {}),
          ...(from && to ? { from, to } : { range: range ?? '7d' }),
        },
      })
      return data
    },
    enabled: !!channelId,
  })
}
