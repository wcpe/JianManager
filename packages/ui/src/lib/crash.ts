/**
 * 崩溃诊断的核心契约（FR-313 / FR-470）。
 *
 * 归包理由：崩溃诊断视图要按快照与趋势渲染根因/证据/关联资源，应用侧 API 层改从包 import，
 * 避免两处各写一份。
 */

/** 崩溃与资源关联证据（FR-470）。 */
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
