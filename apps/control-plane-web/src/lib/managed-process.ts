/**
 * 受管进程探查的契约（FR-169 进程剖析）。
 *
 * 归包理由：监控页的进程 TOP 面板与详情弹窗都要读它，应用侧 API 层改从包 import，
 * 避免两处各写一份。实现（useProcessTop / useManagedProcessDetail 等）仍留应用侧。
 */

/** 进程 TOP 一行。 */
export interface ProcessTopItem {
  instanceId: number
  instanceUuid: string
  nodeUuid: string
  pid: number
  name: string
  cpuPercent: number
  rssBytes: number
  readBytesPerSec: number
  writeBytesPerSec: number
  user: string
  commandSummary: string
  sampledAt: string
}

/** 进程树中的一个节点。 */
export interface ManagedProcessInfo {
  pid: number
  parentPid: number
  name: string
  isRoot: boolean
  cpuPercent: number
  rssBytes: number
  readBytesPerSec: number
  writeBytesPerSec: number
  user: string
  commandSummary: string
  uptimeSeconds: number
  threadCount: number
  sampledAt: string
  unavailableReason: string
}

/** 一条诊断建议。 */
export interface ManagedProcessDiagnostic {
  code: string
  severity: 'info' | 'warning' | 'danger' | string
  title: string
  evidence: string
  suggestion: string
}

/** 进程详情（目标 + 祖先链 + 后代 + 诊断 + 窗口统计）。 */
export interface ManagedProcessDetail {
  instance: { id: number; uuid: string; name: string; nodeId: number; nodeUuid: string; nodeName: string }
  rootPid: number
  target: ManagedProcessInfo
  ancestors: ManagedProcessInfo[]
  children: ManagedProcessInfo[]
  diagnostics: ManagedProcessDiagnostic[]
  history: {
    windowSeconds: number
    sampleCount: number
    latestSampledAt: string
    rssDeltaBytes: number
    avgCpuPercent: number
    avgWriteBytesPerSec: number
  }
}

/** 处置动作的执行结果。 */
export interface ManagedProcessActionResult {
  success: boolean
  action: 'terminate' | 'kill_tree' | string
  pid: number
  affectedPids: number[]
  message: string
}

/** 可下发的处置动作。 */
export type ManagedProcessAction = 'terminate' | 'kill_tree'
