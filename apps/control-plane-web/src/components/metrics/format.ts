/**
 * 观测增强组件的共享格式化工具（FR-463/464/465/469）。
 *
 * 抽出的理由：`fmtBytes` 此前在 `CapacityForecastCard.tsx` 与 `InstanceRankingPanel.tsx`
 * 内**逐字重复两份**（另有 `MetricsSegment` / `StatisticsPage` / `OverviewPage` 等 7 份同义实现，
 * 属跨文件收敛，另开 FR）。
 *
 * 注意与 `@jianmanager/ui` 的 `formatBytes`（packages/ui/src/lib/monitor-metrics.ts:50）的区别：
 * 本实现多一个 `< 1e3 → String(b)` 裸字节分支（排行榜里 0~999 字节要显示精确值而非 `0K`），
 * 故不是同一函数，不能直接复用那一个。
 */

/** 字节 → G/M/K（< 1KB 显示原始字节数）。非有限值或非正值一律 '0'。 */
export function fmtBytes(b: number): string {
  if (!Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1e9) return `${(b / 1024 / 1024 / 1024).toFixed(1)}G`
  if (b >= 1e6) return `${(b / 1024 / 1024).toFixed(0)}M`
  if (b >= 1e3) return `${(b / 1024).toFixed(0)}K`
  return String(b)
}
