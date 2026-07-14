import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'
import { INSTANCE_QUERY_GC_TIME_MS } from '@/api/instances'

/** 实例崩溃快照（FR-313）：进程非正常退出的现场留存，后端每实例滚动保留最近 5 条。 */
export interface CrashSnapshot {
  id: number
  instanceId: number
  /** 崩溃发生时刻（Worker 侧时钟，RFC3339）。 */
  occurredAt: string
  /** 进程退出码；无法获知时为 -1。 */
  exitCode: number
  /** 终止信号名（Unix，如 killed）；Windows / 非信号退出为空。 */
  signal: string
  /** 本次运行时长（毫秒）。 */
  durationMs: number
  /** 崩溃前终端尾部输出（≤200 行 / 64KB，Worker 侧截取）。 */
  tailOutput: string
  createdAt: string
}

/**
 * 查询实例崩溃快照列表（FR-313）：后端按发生时间倒序返回（最新在前，至多 5 条）。
 * 快照只在崩溃时新增，无需轮询；随控制台页数据一次拉取，失败横幅出现（FR-312 互补）
 * 或手动刷新时由查询失效自然重拉。
 */
export function useCrashSnapshots(instanceId: number, enabled = true) {
  return useQuery({
    queryKey: ['crash-snapshots', instanceId],
    queryFn: () => api.get<CrashSnapshot[]>(`/instances/${instanceId}/crash-snapshots`).then((r) => r.data),
    enabled: enabled && instanceId > 0,
    gcTime: INSTANCE_QUERY_GC_TIME_MS,
  })
}
