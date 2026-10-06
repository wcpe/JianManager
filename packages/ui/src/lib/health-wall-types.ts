/**
 * 集群健康墙的类型（FR-461）。
 *
 * 从应用侧 `api/metrics.ts` 抽出：那里混着取数 hook，而健康墙的单格快照是纯数据，
 * 纯逻辑与视图侧要复用。应用侧原文件原样转出，调用点零改动。
 */

export type HealthLevel = 'offline' | 'stale' | 'degraded' | 'healthy'

/** FR-461 健康墙单格：一台节点的当前只读快照。 */
export interface HealthWallNode {
  nodeId: number
  nodeUuid: string
  name: string
  zone?: string
  freshness: 'fresh' | 'stale' | 'offline'
  cpuPct: number | null
  memPct: number | null
  diskPct: number | null
  running: number
  crashed: number
  stopped: number
  /**
   * FR-459/FR-461：活着但已不可服务的实例数（状态仍 RUNNING，巡检已写入 status_reason，
   * 如假死 / 崩溃熔断）。假死进程不会退出、状态也不转 CRASHED，只按 status 分级会漏掉，
   * 故服务端单列计数并据此把节点降级。老 CP 不返回本字段。
   */
  degraded?: number
  activeAlerts: number
  botActive: number | null
  botConnecting: number | null
  level: HealthLevel
  /** 一键定位下钻地址：/monitoring?node=<uuid>。 */
  href: string
}

/** FR-461 健康墙读模型。 */
export interface HealthWallResponse {
  nodes: HealthWallNode[]
  /** 节点数超过服务端上限、响应已按 severity 截断时为 true。 */
  truncated?: boolean
}

/** 健康墙服务端排序键。 */
export type HealthWallSort = 'level' | 'cpu' | 'mem' | 'disk' | 'instances'
