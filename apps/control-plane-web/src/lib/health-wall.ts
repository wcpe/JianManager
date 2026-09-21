import type { HealthLevel, HealthWallNode, HealthWallSort } from '@/api/metrics'
import type { StatusLevel } from '@/lib/threshold'

/**
 * 集群健康墙（FR-461）纯函数：分级排序、标签、配色与汇总计数。
 * 抽成无 React 依赖以便单测；排序由服务端完成，这里仅提供展示口径与统计。
 */

/** 分级严重度（越大越严重），用于排序与优先级。 */
export function healthLevelRank(level: HealthLevel): number {
  switch (level) {
    case 'offline':
      return 3
    case 'stale':
      return 2
    case 'degraded':
      return 1
    default:
      return 0
  }
}

/** 分级 → 中文标签。 */
export function healthLevelLabel(level: HealthLevel): string {
  switch (level) {
    case 'offline':
      return '离线'
    case 'stale':
      return '陈旧'
    case 'degraded':
      return '降级'
    default:
      return '健康'
  }
}

/** 分级 → 语义状态色（徽章/单元格）。 */
export function healthLevelTone(level: HealthLevel): StatusLevel {
  switch (level) {
    case 'offline':
      return 'danger'
    case 'stale':
      return 'info'
    case 'degraded':
      return 'warning'
    default:
      return 'success'
  }
}

/** 健康墙各分级计数汇总。 */
export interface HealthWallSummary {
  total: number
  offline: number
  stale: number
  degraded: number
  healthy: number
}

/** 汇总健康墙各分级节点数。 */
export function summarizeHealthWall(nodes: HealthWallNode[]): HealthWallSummary {
  const summary: HealthWallSummary = { total: nodes.length, offline: 0, stale: 0, degraded: 0, healthy: 0 }
  for (const node of nodes) {
    switch (node.level) {
      case 'offline':
        summary.offline++
        break
      case 'stale':
        summary.stale++
        break
      case 'degraded':
        summary.degraded++
        break
      default:
        summary.healthy++
    }
  }
  return summary
}

/** 健康墙可用的排序键（与服务端 ?sort 对齐）。 */
export const HEALTH_WALL_SORTS: HealthWallSort[] = ['level', 'cpu', 'mem', 'disk', 'instances']

/** 排序键 → 中文标签。 */
export function healthWallSortLabel(sort: HealthWallSort): string {
  switch (sort) {
    case 'cpu':
      return 'CPU'
    case 'mem':
      return '内存'
    case 'disk':
      return '磁盘'
    case 'instances':
      return '实例数'
    default:
      return '严重度'
  }
}

/** 格式化百分比水位（缺测为 --）。 */
export function formatPct(value: number | null): string {
  return value == null ? '--' : `${value.toFixed(0)}%`
}
