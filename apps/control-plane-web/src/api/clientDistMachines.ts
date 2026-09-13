import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 机器级更新清单与钻取（FR-426 mock 契约，真栈按 spec `client-dist-machine-drilldown` 落地）。
 * 「用户」= 机器（machineId 去重，ADR-023 不可信仅近似）；明细 14 天窗内精确、超窗小时桶近似。
 */

/** 机器更新聚合项（machineId/ip/installId 展示层已脱敏——对齐 FR-265 先例，由后端返回脱敏值）。 */
export interface ClientMachineSummary {
  machineId: string
  channelId: string
  /** 窗口内更新总次数。 */
  updates: number
  /** 最近一次成功/尝试更新时间（ISO）。 */
  lastUpdateAt: string
  latestVersion: number
  currentVersion: number
  /** latest - current；0=已最新。 */
  versionLag: number
  downloadBytes: number
  /**
   * 玩家名（FR-426 增补）：遥测 player_name / 启动参数 --username / 运行态画像解析，
   * 未知为空串（老版本客户端未上报）。仅供人读，不参与统计口径。
   */
  playerName: string
  /** 安装 ID（脱敏短形式）；来自遥测 install_id，未知为空串。 */
  installId: string
  /** 最近来源 IP（脱敏）。 */
  ip: string
}

/** 单机器更新事件（时间线钻取项）。 */
export interface ClientMachineEvent {
  ts: string
  version: number
  result: 'success' | 'fail-static' | 'rollback' | 'error'
  bytes: number
  durationMs: number
}

export interface ClientMachineListResponse {
  channelId: string
  from: string
  to: string
  /** true=窗口在明细保留窗(14d)内，聚合精确；false=超窗近似。 */
  exact: boolean
  total: number
  page: number
  pageSize: number
  items: ClientMachineSummary[]
}

export interface ClientMachineEventsResponse {
  machineId: string
  channelId: string
  from: string
  to: string
  items: ClientMachineEvent[]
}

export type MachineSortField = 'updates' | 'lastUpdateAt' | 'versionLag'

export function useClientDistMachines(params: {
  channelId?: string
  from: string
  to: string
  sort?: MachineSortField
  order?: 'asc' | 'desc'
  page?: number
  pageSize?: number
  enabled?: boolean
}) {
  const { channelId, from, to, sort = 'updates', order = 'desc', page = 1, pageSize = 20, enabled = true } = params
  return useQuery({
    queryKey: ['client-dist-machines', channelId ?? 'all', from, to, sort, order, page, pageSize],
    queryFn: async () => {
      const { data } = await api.get<ClientMachineListResponse>('/client-dist/machines', {
        params: {
          ...(channelId ? { channelId } : {}),
          from,
          to,
          sort,
          order,
          page,
          pageSize,
        },
      })
      return data
    },
    enabled: enabled && !!from && !!to,
  })
}

export function useClientMachineEvents(params: {
  machineId: string | null
  channelId?: string
  from: string
  to: string
  enabled?: boolean
}) {
  const { machineId, channelId, from, to, enabled = true } = params
  return useQuery({
    queryKey: ['client-machine-events', machineId, channelId ?? 'all', from, to],
    queryFn: async () => {
      const { data } = await api.get<ClientMachineEventsResponse>(
        `/client-dist/machines/${encodeURIComponent(machineId ?? '')}/events`,
        { params: { ...(channelId ? { channelId } : {}), from, to } },
      )
      return data
    },
    enabled: enabled && !!machineId && !!from && !!to,
  })
}
