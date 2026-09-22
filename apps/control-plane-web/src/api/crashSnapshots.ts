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
export interface CrashCorrelation {
  /** 崩前 RSS 是否逼近实例内存上限。 */
  nearOom: boolean
  /** 崩溃窗口内进程树 RSS 峰值（字节）。 */
  rssAtCrash: number
  /** 实例内存上限（MiB）；0=未设限。 */
  memLimitMb: number
  /** 崩溃窗口内 JVM 堆已用峰值（字节）。 */
  heapUsedMax: number
  /** GC 关联提示（FR-465 落地前为「待依赖」）。 */
  gcNote: string
  note?: string
}

/** 实例崩溃快照（FR-313，增强 FR-470）：进程非正常退出的现场留存，后端每实例滚动保留最近 5 条。 */
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
  /** 根因归类（FR-470）；旧快照（FR-470 之前入库）为空。 */
  rootCause?: string
  /** 归一化同类指纹，用于同类聚合。 */
  signature?: string
  /** 命中规则的原文行（JSON 数组文本；前端优先用 evidenceLines）。 */
  evidence?: string
  /** 解析后的证据行（后端已解析，直接可渲染）。 */
  evidenceLines?: string[]
  /** 归类置信度 0~1。 */
  confidence?: number
  /** 崩溃与资源关联证据。 */
  correlation?: CrashCorrelation
  createdAt: string
}

/** 趋势序列上的一个点（天 × 根因）。 */
export interface CrashTrendPoint {
  day: string
  rootCause: string
  count: number
}

export interface CrashCauseCount {
  rootCause: string
  count: number
}

export interface CrashSignatureCount {
  signature: string
  rootCause: string
  count: number
}

/**
 * 实例崩溃趋势（FR-470）：来自独立汇总表，**不受 K=5 快照裁剪影响**
 * ——连崩超过 5 次后列表只剩 5 条，但趋势计数完整。
 */
export interface CrashTrend {
  instanceId: number
  days: number
  total: number
  points: CrashTrendPoint[]
  byRootCause: CrashCauseCount[]
  topSignatures: CrashSignatureCount[]
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

/** 查询实例崩溃趋势（FR-470）：按天/根因计数 + 同类聚合。 */
export function useCrashTrend(instanceId: number, days = 30, enabled = true) {
  return useQuery({
    queryKey: ['crash-trend', instanceId, days],
    queryFn: () => api.get<CrashTrend>(`/instances/${instanceId}/crash-trend?days=${days}`).then((r) => r.data),
    enabled: enabled && instanceId > 0,
    gcTime: INSTANCE_QUERY_GC_TIME_MS,
  })
}
