import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 进程日志诊断（不是日志检索）。
 *
 * 设计边界（勿扩大）：只回答「某个进程最近发生了什么」，
 * 不做关键字检索、时间窗查询、分页、多节点聚合。
 * 需要按条件检索请走日志中心（实例日志）；这里只用于排障时看几百行。
 *
 * 为什么存在：CP / Worker 的进程日志此前只写 stdout/stderr，文件由外部重定向
 * 产生（systemd journald 或 `>> file`）。CP 读不到 Worker 的 journald，
 * 节点出问题时只能 SSH。这个入口把它变成面板里的一次点击。
 */
export interface ProcessLogTail {
  /** 尾部原文（已按行/字节上限裁剪）。 */
  content: string
  /** 是否因上限被截断——UI 必须显式标注，否则会让人误以为看到全部。 */
  truncated: boolean
  /** 日志文件总大小，让运维知道「还有多少没看到」。 */
  fileBytes: number
  /** 实际读取的文件路径（供 UI 展示可复制的 grep 命令）。 */
  path: string
  /** 读取失败/未创建时的说明；非空时 content 通常为空。 */
  note?: string
}

/** 平台（CP）自身进程日志尾部。 */
export function usePlatformDiagnosticsLog(enabled: boolean, lines = 200) {
  return useQuery({
    queryKey: ['diagnostics', 'platform-log', lines],
    enabled,
    // 诊断是「这一刻的快照」，不做后台轮询：轮询会让正在读的人被刷走。
    staleTime: 0,
    refetchOnWindowFocus: false,
    queryFn: async () => {
      const { data } = await api.get<ProcessLogTail>('/diagnostics/log', { params: { lines } })
      return data
    },
  })
}

/** 节点（Worker）进程日志尾部，经 CP 反向隧道拉取。 */
export function useNodeDiagnosticsLog(nodeId: number | null, enabled: boolean, lines = 200) {
  return useQuery({
    queryKey: ['diagnostics', 'node-log', nodeId, lines],
    enabled: enabled && nodeId !== null,
    staleTime: 0,
    refetchOnWindowFocus: false,
    queryFn: async () => {
      const { data } = await api.get<ProcessLogTail>(`/nodes/${nodeId}/diagnostics/log`, {
        params: { lines },
      })
      return data
    },
  })
}
