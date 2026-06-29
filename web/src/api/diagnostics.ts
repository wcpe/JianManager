import { useMutation } from '@tanstack/react-query'
import api from '@/api/client'

/** 出站 HTTP 可达性测试结果（FR-229）。 */
export interface HTTPReachabilityResult {
  ok: boolean
  status?: number
  latencyMs: number
  error?: string
}

/** 节点存活探测结果（FR-229）。 */
export interface NodePingResult {
  alive: boolean
  latencyMs: number
  version?: string
  os?: string
  arch?: string
  error?: string
}

/**
 * 出站 HTTP 可达性测试（FR-229）：经 CP 当前出站客户端（含已配置代理）GET 目标 URL。
 * 供「代理设置测试」「JDK 下载源测试」复用——先确认能通再下载，避免下载长卡死。
 */
export function useTestHTTP() {
  return useMutation({
    mutationFn: (url: string) =>
      api.post<HTTPReachabilityResult>('/diagnostics/http-test', { url }).then((r) => r.data),
  })
}

/**
 * 节点存活探测（FR-229）：经 gRPC 调用 Worker 轻量 GetVersion 主动探活（不读心跳缓存）。
 * 供 JDK 一键下载前「测试节点是否存活」，避免对离线/卡顿节点发起会卡死的下载。
 */
export function usePingNode(nodeId: number) {
  return useMutation({
    mutationFn: () => api.post<NodePingResult>(`/nodes/${nodeId}/ping`, {}).then((r) => r.data),
  })
}
