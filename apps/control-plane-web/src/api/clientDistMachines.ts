import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 机器级更新清单与钻取（FR-426 mock 契约，真栈按 spec `client-dist-machine-drilldown` 落地）。
 * 「用户」= 机器（machineId 去重，ADR-023 不可信仅近似）；明细 14 天窗内精确、超窗小时桶近似。
 */

/**
 * 机器清单契约（FR-426）已归包，双侧共用（ADR-097）；对外保留原导出名，调用点无需改动。
 */
export type {
  ClientMachineSummary,
  ClientMachineEvent,
  ClientMachineListResponse,
  ClientMachineEventsResponse,
  MachineSortField,
} from '@jianmanager/ui/lib/client-dist-machines-contracts'
import type {
  ClientMachineListResponse,
  ClientMachineEventsResponse,
  MachineSortField,
} from '@jianmanager/ui/lib/client-dist-machines-contracts'

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
