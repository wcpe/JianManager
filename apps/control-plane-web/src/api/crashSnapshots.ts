import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'
import { INSTANCE_QUERY_GC_TIME_MS } from '@/api/instances'

/** 崩溃根因枚举（FR-470，与后端 crashdiag 对齐）。 */
export const CRASH_ROOT_CAUSES = [
  'oom',
  'port_in_use',
  'class_not_found',
  'jvm_args',
  'permission',
  'segfault',
  'corrupt_data',
  'unknown',
] as const

export type CrashRootCause = (typeof CRASH_ROOT_CAUSES)[number]

/** 崩溃与资源关联证据（FR-470 §2.3，OOM 证据链）。 */
// 崩溃诊断契约已回迁应用侧（受控视图与业务页面共用，ADR-097）；此处原样再导出，调用点无需改动。
// 本地绑定供本文件的查询泛型使用。
import type { CrashSnapshot, CrashTrend } from '@/lib/crash'
export type {
  CrashCauseCount,
  CrashCorrelation,
  CrashSignatureCount,
  CrashSnapshot,
  CrashTrend,
  CrashTrendPoint,
} from '@/lib/crash'

// `CrashSnapshot` / `CrashTrend*` 的定义已随受控视图归包（见上方 re-export）。


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

/** 查询实例崩溃趋势（FR-470）：按天/根因计数 + 同类聚合。 */
export function useCrashTrend(instanceId: number, days = 30, enabled = true) {
  return useQuery({
    queryKey: ['crash-trend', instanceId, days],
    queryFn: () => api.get<CrashTrend>(`/instances/${instanceId}/crash-trend?days=${days}`).then((r) => r.data),
    enabled: enabled && instanceId > 0,
    gcTime: INSTANCE_QUERY_GC_TIME_MS,
  })
}
