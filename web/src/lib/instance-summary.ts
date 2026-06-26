import type { InstanceInfo } from '@/api/instances'

/**
 * 实例列表汇总计数（FR-136）。
 * 抽成无 React 依赖的纯函数以便单测，供顶部汇总头「运行 N / 停止 N / 崩溃 M / 总数」着色与点击筛选。
 */

/** 状态分桶计数：running 含过渡态（启动/停止中，均视作「活动中」）。 */
export interface InstanceStatusCounts {
  total: number
  running: number
  stopped: number
  crashed: number
}

/**
 * 按状态把实例聚合为汇总计数。
 * - running：RUNNING + STARTING + STOPPING（过渡态归活动桶，避免汇总头漏算正在拉起/收尾的实例）。
 * - stopped：STOPPED。
 * - crashed：CRASHED。
 * 未知状态只计入 total。
 */
export function summarizeInstances(instances: InstanceInfo[]): InstanceStatusCounts {
  const counts: InstanceStatusCounts = { total: 0, running: 0, stopped: 0, crashed: 0 }
  for (const inst of instances) {
    counts.total++
    switch (inst.status) {
      case 'RUNNING':
      case 'STARTING':
      case 'STOPPING':
        counts.running++
        break
      case 'STOPPED':
        counts.stopped++
        break
      case 'CRASHED':
        counts.crashed++
        break
    }
  }
  return counts
}

/** 汇总头可点的筛选维度键，映射到列表 status 筛选值。 */
export type SummaryFilterKey = 'all' | 'running' | 'stopped' | 'crashed'

/**
 * 汇总头某一项点击后对应的列表状态筛选值。
 * running 桶含多个状态，后端 status 单值过滤无法表达「运行或过渡」，故以 RUNNING 为代表过滤；
 * stopped/crashed 一一对应；all 返回 undefined（清空筛选）。
 */
export function summaryFilterStatus(key: SummaryFilterKey): string | undefined {
  switch (key) {
    case 'running':
      return 'RUNNING'
    case 'stopped':
      return 'STOPPED'
    case 'crashed':
      return 'CRASHED'
    default:
      return undefined
  }
}
