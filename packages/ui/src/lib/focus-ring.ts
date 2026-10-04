/**
 * 焦点环常量（FR-496 阶段 6）：键盘焦点环的唯一出处。
 *
 * 背景：Button / Input / Dialog 等交互面各自手写同一串 `focus-visible:*`，改一次要翻遍全库；
 * 更糟的是它们并不同步——有的写 3px + 50% 主色（糊到邻近文字），有的写 2px。此模块把
 * 「焦点环长什么样」收敛成一处，组件只表达「用哪一种环」，不重复拼类名。
 *
 * 与 Astryx `focusOutline` 的对应关系：Astryx 把环放在 `--focus-outline-*` 变量里，
 * 由主题统一改外观；本库的环取语义色 token（ring / destructive），
 * **宽度与不透明度按 FR-176 收敛为细环**——这是既定的可访问性决策，不是随手取值：
 * 3px/50% 的环在密集表格与表单里会晕染到相邻文字，故统一为 2px/40%。
 *
 * 约束：本模块只导出类名常量，不导出组件；调用点用 `cn(...)` 与自身类名合并即可。
 */

/**
 * 标准细焦点环（FR-176 收敛值）：2px 主色环 + 40% 底 + 主色边线。
 *
 * 边线与环同时保留是有意的：只描边在浅底上对比不足，只画环在深底上不够醒目，
 * 两者叠加才能同时满足明暗主题（对比度校验见 lib/color-contrast.ts）。
 */
export const focusRing =
  'focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/40'

/**
 * 内嵌焦点环变体：环画在元素边界内侧。
 *
 * 用于「外环会被父层裁掉」的场合——父级 `overflow-hidden`（表格容器、圆角卡片、
 * 抽屉/模态正文滚动区）会在元素贴边时把 2px 外环裁掉一半，看起来像没焦点态。
 */
export const focusRingInset = `${focusRing} focus-visible:ring-inset`

/**
 * 危险/错误色调焦点环：焦点环取 destructive 色而非主色。
 *
 * 用于销毁性按钮（删除、回滚）：这类控件的边线本来就是红的，若焦点环仍是主色绿，
 * 「我正在按的是危险按钮」这层语义在键盘操作时丢失。
 * 与 {@link focusRing} 保持同样的 2px 收敛宽度，仅换色。
 */
export const focusRingDanger =
  'focus-visible:border-destructive focus-visible:ring-2 focus-visible:ring-destructive/20 dark:focus-visible:ring-destructive/40'

/**
 * `aria-invalid` 状态描红：无效控件的边线 + 环底取 destructive。
 *
 * 单独成常量是因为它作用的属性不是 `:focus-visible` 而是 `[aria-invalid]`，
 * 与焦点环是两条正交的视觉通道（一个无效输入框在失焦时也该是红的）；
 * 但取值必须与 {@link focusRingDanger} 同源，否则「焦点 + 无效」叠起来会出现混色。
 */
export const invalidFieldState =
  'aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40'

/** 焦点环色调：主色（常规）或危险色（销毁性/无效控件）。 */
export type FocusRingTone = 'ring' | 'destructive'

/**
 * 按色调与内外位置取焦点环类名。
 *
 * 供 `cva` 变体表使用：变体众多时逐个拼字面量容易漏，交给一个函数取。
 * 注意不要用「两个常量拼一起」的方式切换色调——`focus-visible:border-*` 会互相覆盖，
 * 结果取决于 tailwind-merge 的先后顺序，而不是调用点的意图。
 */
export function focusRingClass(options: {
  /** 环色，默认主色。 */
  tone?: FocusRingTone
  /** 是否内嵌（父层会裁掉外环时用）。 */
  inset?: boolean
} = {}): string {
  const { tone = 'ring', inset = false } = options
  const ring = tone === 'destructive' ? focusRingDanger : focusRing
  return inset ? `${ring} focus-visible:ring-inset` : ring
}
