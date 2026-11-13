/**
 * 图表的可访问名（替代文本）推导。
 *
 * 【为什么必须有】图表在 DOM 里是一堆 `<path>` / `<text>`：读屏把坐标轴刻度、图例文字逐条念出来，
 * 却拼不出「这是一张什么图、画的是哪几条线」，对读屏用户约等于一片噪声。标准解法是把整个绘图区
 * 声明成 `role="img"` 并给出替代文本（与 Sparkline 的既有做法一致）。
 *
 * 【为什么不写死文案】本函数位于组件库，不持有语言包、也不知道调用方在图什么。默认取序列名拼接——
 * 生产代码里 `series[].name` 已是本地化后的指标名（如「CPU 使用率」「TPS」），拼出来就是有意义的
 * 描述；调用方有更贴切的说法时用显式 `ariaLabel` 覆盖。
 */
export function chartAccessibleName(
  series: readonly { name?: string }[],
  explicit: string | undefined,
  hint: string | undefined,
): string | undefined {
  if (explicit) return explicit
  // 去重后再拼：同一张图里重名序列没有意义（多世界/多实例叠加时 name 可能重复），重复播报只是噪声。
  const names = [...new Set(series.map((s) => s.name?.trim()).filter((n): n is string => !!n))]
  if (names.length > 0) return names.join(', ')
  // 兜底：完全推不出来时用调用方给的本地化提示（一般与空态文案同源）；都没有就不挂 aria-label，
  // 空字符串的无名图像反而会被读屏念成「图片」而更含糊。
  return hint && hint.trim() ? hint : undefined
}
