/**
 * 平台级聚合统计辅助（FR-220）。
 * 抽成无 React 依赖的纯函数以便单测，供观测·统计页做「构成分布」（按角色/进程类型/OS/arch/版本/平台分桶 + 占比）。
 * 状态/节点的现成汇总复用 `instance-summary.summarizeInstances` 与 `node-summary.summarizeNodes`，本处不重复实现。
 */

/** 分布的一桶：键 + 计数 + 占总比例（0~1）。按 count 降序排列。 */
export interface DistBucket {
  key: string
  count: number
  /** 占总数比例（0~1）；总数为 0 时为 0。 */
  pct: number
}

/**
 * 按 keyFn 把列表分桶计数并算占比，按 count 降序返回。
 * - keyFn 返回空串 / undefined / null 的项归入 `emptyLabel`（默认「—」），不丢弃（否则各桶之和 < 总数易误导）。
 * - 占比以列表总长为分母（含 empty 桶），各桶 pct 之和 = 1（除浮点误差）。
 */
export function tallyBy<T>(
  list: T[],
  keyFn: (item: T) => string | undefined | null,
  emptyLabel = '—',
): DistBucket[] {
  const total = list.length
  const counts = new Map<string, number>()
  for (const item of list) {
    const raw = keyFn(item)
    const key = raw == null || raw === '' ? emptyLabel : raw
    counts.set(key, (counts.get(key) ?? 0) + 1)
  }
  return Array.from(counts.entries())
    .map(([key, count]) => ({ key, count, pct: total > 0 ? count / total : 0 }))
    .sort((a, b) => b.count - a.count || a.key.localeCompare(b.key))
}

/**
 * 把后端聚合返回的 `Record<string, number>`（如 byRole / byProcessType）转成分布桶。
 *
 * 与 `tallyBy` 的分工：后者从「明细列表」现算，前者直接消费后端已聚合好的计数。
 * 需要展示实例维度的分布时优先用这个——不必为一张分布图拉全量实例列表
 * （千级约 1MB/轮且带兜底轮询）。聚合对枚举键做了零补，故这里滤掉 0 值避免噪声。
 */
export function bucketsFromCounts(counts: Record<string, number> | undefined): DistBucket[] {
  const entries = Object.entries(counts ?? {})
  const total = entries.reduce((sum, [, n]) => sum + n, 0)
  return entries
    .filter(([, n]) => n > 0)
    .map(([key, count]) => ({ key, count, pct: total > 0 ? count / total : 0 }))
    .sort((a, b) => b.count - a.count || a.key.localeCompare(b.key))
}

/** 探针连通汇总：可达/总数 + 比例（0~1；无后端时为 0）。 */
export interface ProbeReachability {
  available: number
  total: number
  /** 可达比例（0~1）；总数为 0 时为 0。 */
  pct: number
}

/** 从在线玩家结果的 backends 列表汇总探针连通比例（FR-067 优雅降级口径）。 */
export function summarizeProbeReachability(backends: { available: boolean }[]): ProbeReachability {
  const total = backends.length
  const available = backends.reduce((n, b) => n + (b.available ? 1 : 0), 0)
  return { available, total, pct: total > 0 ? available / total : 0 }
}
