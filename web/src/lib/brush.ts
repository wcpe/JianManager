/**
 * 监控页 brush 拖拽轴（FR-169）的纯逻辑：把 recharts Brush 的选区（按时间戳升序数组上的
 * startIndex/endIndex）换算成 { from, to } 时间窗，供该图二次过滤。抽成无 React 依赖的模块
 * 以便 vitest 单测（参照 runtime-assets-view.ts 约定）。
 */

/** 一个时间窗（闭区间，ISO 时间戳）。 */
export interface TimeWindow {
  from: string
  to: string
}

/**
 * 把 brush 选区（在已升序排列的时间戳数组上的下标对）换算为时间窗。
 *
 * - 下标越界自动夹到 [0, len-1]；start>end 时自动交换（用户反向拖动手柄）。
 * - 空数组返回 null（无可选区间）。
 * - recharts 的 Brush 可能回传 undefined 下标（拖动中），按各自默认（start→0、end→末尾）兜底。
 */
export function brushSelectionToWindow(
  timestamps: string[],
  startIndex: number | undefined,
  endIndex: number | undefined,
): TimeWindow | null {
  const len = timestamps.length
  if (len === 0) return null

  const clamp = (i: number): number => {
    if (!Number.isFinite(i)) return 0
    if (i < 0) return 0
    if (i > len - 1) return len - 1
    return Math.floor(i)
  }

  let lo = clamp(startIndex ?? 0)
  let hi = clamp(endIndex ?? len - 1)
  if (lo > hi) [lo, hi] = [hi, lo]

  return { from: timestamps[lo], to: timestamps[hi] }
}

/**
 * 判断某时间窗是否覆盖整段数据（首末两端）。覆盖全段视为「未筛选」，
 * 调用方据此决定是否显示「重置 brush」之类的提示。
 */
export function isFullWindow(timestamps: string[], window: TimeWindow | null): boolean {
  if (!window) return true
  const len = timestamps.length
  if (len === 0) return true
  return window.from === timestamps[0] && window.to === timestamps[len - 1]
}

/**
 * 按时间窗过滤升序时间戳行集合（闭区间，含端点）。行需带 ts:string 字段。
 * window 为 null 时原样返回。
 */
export function filterRowsByWindow<T extends { ts: string }>(rows: T[], window: TimeWindow | null): T[] {
  if (!window) return rows
  return rows.filter((r) => r.ts >= window.from && r.ts <= window.to)
}
