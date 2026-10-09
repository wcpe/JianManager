import { keepPreviousData, useQuery } from '@tanstack/react-query'
import api from '@/api/client'
import { INSTANCE_QUERY_GC_TIME_MS } from '@/api/instances'

// 服务器状态契约已回迁应用侧（受控视图与业务页面共用，ADR-097）；此处原样再导出，调用点无需改动。
// 本地绑定供本文件的查询泛型使用。
import type { ServerStateResponse } from '@/lib/server-state'
export type { Bounded, ClassloaderSection, JvmSection, ListenersSection, ProbeServerState, SchedulerSection, ServerSection, ServerStateResponse, WorldEntry } from '@/lib/server-state'

/**
 * 按需查询某实例全量服务器状态（FR-076 / FR-077）。
 *
 * **默认不自动轮询**（refetchInterval: false）：全量快照较重，仅在前端开 tab/手动点刷新时拉取（按需），
 * 轻指标历史时序仍走 /metrics。探针未连入/采集超时由后端降级（200 + connected/available=false）。
 * 由调用方控制 `enabled`（开 tab 后才首拉），刷新经返回的 `refetch`。
 */
export function useServerState(
  instanceId: number,
  enabled: boolean,
  /** 自动刷新间隔（ms）；false=仅手动刷新（默认，非侵入）。开 tab 期间可由调用方开启（FR-109）。 */
  refetchMs: number | false = false,
) {
  return useQuery({
    queryKey: ['server-state', instanceId],
    queryFn: () =>
      api.get<ServerStateResponse>(`/instances/${instanceId}/server-state`).then((r) => r.data),
    enabled: enabled && instanceId > 0,
    refetchInterval: refetchMs,
    refetchOnWindowFocus: false,
    // 自动刷新时数据视为即时过期以便按间隔重拉；手动模式下保持快照不被动重拉。
    staleTime: refetchMs ? 0 : Infinity,
    // FR-297：跨服回切先呈现缓存快照后台刷新，不打断运营者巡检节奏。
    gcTime: INSTANCE_QUERY_GC_TIME_MS,
    placeholderData: keepPreviousData,
  })
}
