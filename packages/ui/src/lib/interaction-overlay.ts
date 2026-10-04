/**
 * 交互覆盖层常量（FR-496 阶段 6）：悬停 / 按压 / 禁用反馈的统一词汇表。
 *
 * 背景：本库此前每个组件各自发明 hover 与 active 配色（`hover:bg-accent`、`hover:bg-accent/50`、
 * `active:bg-primary/80`…），于是同一个「按住」动作在不同组件上深浅不一，
 * 更常见的是**有 hover 无 active**——触屏与键盘用户完全看不到按压反馈。
 * 这里把「按压/悬停该怎么表现」收敛成一处。
 *
 * 与 Astryx `interactionOverlayStyles` 的差异：Astryx 用 `background-image: linear-gradient(overlay, overlay)`
 * 把半透明涂层叠在控件自身底色之上，因此对任何底色都成立；Tailwind 侧没有等价的「叠图」惯用法
 * （内联 style 与自定义 CSS 都被前端约定禁止），故这里按**色调**给出涂层：
 * 控件在自身色系里加深一档，而不是叠一层外来颜色——`active:bg-accent/70` 落到主色实心按钮上
 * 会整块变浅绿，那是错的表现，不是表现力不足。
 *
 * 所有时长/缓动一律绑 motion token（见 styles/motion.css 的词汇表），不写 `duration-150` 之类定值。
 */

/**
 * 通用交互过渡：颜色/边框/阴影之外的内容变化（渐变、滤镜）不在此列。
 *
 * 取 fast（120ms）而非 normal：指针悬停反馈必须比页面转场更快才像「即时响应」。
 */
export const interactionTransition =
  'transition-[background-color,border-color,color,box-shadow] duration-[var(--motion-duration-fast)] ease-ios'

/**
 * 卡片类表面的悬停抬升过渡：只换阴影、不换颜色。
 *
 * 单独成常量是因为它的时长取 slow（320ms）——阴影是「重量」线索，快切会像闪烁；
 * 且 FR-176 已定死卡片 hover 只换阴影不位移，故此常量只描述过渡，位移类名不该出现在它的使用者里。
 *
 * 注：`Panel` 自己是同源取值的字面量（`transition-[box-shadow] duration-[var(--motion-duration-slow)] ease-ios`），
 * 因为全局动效守护测试把它钉成了锚点文件；CPW 的三张工作台卡片（节点/实例/Bot）在下一批收敛时取本常量。
 */
export const shadowTransition =
  'transition-[box-shadow] duration-[var(--motion-duration-slow)] ease-ios'

/** 中性悬停涂层：浅色主色底（accent）+ 对应前景色。 */
export const hoverOverlay = 'hover:bg-accent hover:text-accent-foreground dark:hover:bg-accent/50'

/**
 * 中性按压涂层：同色系加深一档，与悬停可区分。
 *
 * 不做 `active:bg-accent`（与悬停同值）是有意的：按住时若无任何变化，用户无法判断
 * 「这次点击有没有生效」，尤其在操作延迟明显的运维台。
 */
export const pressOverlay = 'active:bg-accent/70 dark:active:bg-accent/60'

/** 中性悬停 + 按压：ghost/outline 一类无自身底色的控件整套取用。 */
export const interactionOverlay = `${hoverOverlay} ${pressOverlay}`

/**
 * 统一禁用态：屏蔽指针事件 + 降不透明度。
 *
 * 用 `disabled:pointer-events-none` 而非只降透明度：禁用控件仍能接收 hover/active 会
 * 造成「看起来可点但点不动」；同时禁用态必须**同时**有视觉与非视觉两层表达
 * （不透明度是视觉层，`disabled` 属性本身由原生元素提供语义层）。
 */
export const disabledState = 'disabled:pointer-events-none disabled:opacity-50'

/** 按压涂层色调：与按钮变体色系一一对应。 */
export type PressTone = 'primary' | 'destructive' | 'secondary' | 'neutral'

/**
 * 按色调取按压涂层类名。
 *
 * 供 `cva` 变体表使用：按压色必须落在控件自身色系内（见模块头注释），
 * 逐个变体手写容易漏，且漏掉的那个不会报错、只会静默没有按压反馈。
 */
export function pressOverlayClass(tone: PressTone): string {
  switch (tone) {
    case 'primary':
      return 'active:bg-primary/80'
    case 'destructive':
      return 'active:bg-destructive/85 dark:active:bg-destructive/75'
    case 'secondary':
      return 'active:bg-secondary/70'
    case 'neutral':
    default:
      return pressOverlay
  }
}
