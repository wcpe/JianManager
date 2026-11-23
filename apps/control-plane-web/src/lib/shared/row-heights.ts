/**
 * 变高虚拟列表的「实测行高回填」纯逻辑（任务中心等列表共用）。
 *
 * 【为什么必须有实测回填】估算行高哪怕只差零点几像素，误差也会**按行数线性累积**
 * （500 行 × 0.5px = 250px），表现为滚动条位置与真实内容错位、拖到底也到不了最后一行。
 * 故已渲染过的行一律以 DOM 实测值覆盖估算值；未渲染到的行继续用估算——它们还没进 DOM，无从量起。
 *
 * 【为什么抽成纯函数】DOM 实测在 jsdom 里恒为 0（无布局），组件级用例覆盖不到合并规则本身；
 * 把规则独立出来才能对它做回归（阈值、去重、无变化不触发重渲染）。
 */

/** 一行的 DOM 实测高度。 */
export interface MeasuredRowHeight {
  /** 行标识（taskId 等稳定键）。 */
  id: string
  /** 实测高度（px）；≤0 表示当前环境无布局（jsdom / 隐藏容器），调用方应忽略。 */
  px: number
}

/**
 * 相邻两次实测差异小于该值即视为未变。
 *
 * 取 0.5px 的理由：子像素布局下同一行在不同帧可能量出 56.7 / 56.71 这类抖动，
 * 若按精确不等更新，会形成「实测 → 重渲染 → 再实测」的无限循环；而 0.5px 的偏差
 * 在 500 行量级也只有 250px 的总高误差——这个量级正是本函数要消灭的，故阈值取在同一数量级之下。
 */
export const ROW_HEIGHT_EPSILON_PX = 0.5

/**
 * 把实测行高并入已知表（不修改入参）。
 *
 * @param base 已知行高（宽度变化时调用方应传空表——旧实测值是按旧宽度量的，留着只会整体错位）
 * @param measured 本轮从 DOM 量到的行高
 * @param skipId 由别的通道记账的行（如展开行自己有实测高度），不在此重复记录
 */
export function mergeMeasuredRowHeights(
  base: ReadonlyMap<string, number>,
  measured: readonly MeasuredRowHeight[],
  skipId?: string | null,
): { sizes: Map<string, number>; changed: boolean } {
  const sizes = new Map(base)
  let changed = false
  for (const { id, px } of measured) {
    if (!id || id === skipId) continue
    // 非正高度 = 当前环境量不出布局，保留估算。
    if (!(px > 0)) continue
    if (Math.abs((base.get(id) ?? 0) - px) < ROW_HEIGHT_EPSILON_PX) continue
    sizes.set(id, px)
    changed = true
  }
  return { sizes, changed }
}
