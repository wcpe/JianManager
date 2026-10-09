/**
 * 观测增强组件的共享格式化工具（FR-463/464/465/469）。
 *
 * 抽出的理由：`fmtBytes` 此前在 `CapacityForecastCard.tsx` 与 `InstanceRankingPanel.tsx`
 * 内**逐字重复两份**。
 *
 * 注意与 `@jianmanager/ui` 的 `formatBytes`（packages/ui/src/lib/monitor-metrics.ts:50）的区别：
 * 本实现多一个 `< 1e3 → String(b)` 裸字节分支（排行榜里 0~999 字节要显示精确值而非 `0K`），
 * 故不是同一函数，不能直接复用那一个。
 *
 * ## 与「文件体积」格式化的分工（别混用）
 * 本模块是**图表轴 / 排行榜的紧凑口径**：`K/M/G`、无空格、进位阈值按 1e3。
 * 「文件体积 / 存储占用」（`1.0 KB` / `2.0 GB` 这种带 B 且带空格的写法）已收敛到唯一实现
 * `@/lib/shared/format-file-size`，它同样**不能**用本函数替代。两处互相指路，各有独立语义。
 *
 * ## 紧凑口径同族实现的收敛现状
 * 与 `packages/ui/src/lib/monitor-metrics.ts` 的两份 canonical 之外，本族在应用侧仍散落
 * **6 份**同义实现（其中 4 份与本模块逐字相同，2 份少了裸字节分支）：
 * - 含裸字节分支：`views/statistics/StatisticsPageView`、`views/client-dist/InsightCards`、
 *   `views/client-dist/machine-format`、`views/client-dist/OpsShared`；
 * - 少裸字节分支：`views/instances/MetricsSegment`、`views/overview/OverviewPageView`
 *   （0~999 字节会显示 `0K`，与本模块口径不同）。
 * 它们属跨文件收敛，**另开 FR**（本次只做了「文件体积」域的收敛，未动这一族）。
 */

/** 字节 → G/M/K（< 1KB 显示原始字节数）。非有限值或非正值一律 '0'。 */
export function fmtBytes(b: number): string {
  if (!Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1e9) return `${(b / 1024 / 1024 / 1024).toFixed(1)}G`
  if (b >= 1e6) return `${(b / 1024 / 1024).toFixed(0)}M`
  if (b >= 1e3) return `${(b / 1024).toFixed(0)}K`
  return String(b)
}