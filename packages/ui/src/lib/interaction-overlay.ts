/**
 * 交互覆盖层常量（FR-502 阶段 6）：悬停 / 按压 / 禁用反馈的统一词汇表。
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

/**
 * 卡片类表面的基础样式：圆角 + 描边 + 卡片底 + 柔和阴影。
 *
 * **为什么要有这个常量**：`Card`、`Panel`、`StatCard` 三个组件此前各自手写
 * `rounded-lg border bg-card/95 shadow-soft`——三处取值巧合一致，但没有任何机制保证
 * 它们今后仍然一致。改圆角要改三处、漏一处就静默漂移；这正是「整体样式不统一」的来源。
 * 现在收敛为一处：三者都引用本常量，改一次全站跟随。
 *
 * 关于 `bg-card/95` 与 `backdrop-blur`：底色本身 95% 不透明，模糊只作用于透出的 5%，
 * 视觉收益极小，代价是每个卡片成为一个独立合成层（实测禁用后侧栏开合过渡
 * 51.6fps→58.8fps）。因此默认不带模糊，需要真毛玻璃的场景（下方确有内容滚动）
 * 由调用方显式加回。
 *
 * 【边框为什么降到 55% 不透明度】卡片原本用实色 `border`（`--border`，浅色下 #d7dce3）。
 * 配上已收敛的极淡阴影后，一圈实线反而成了最重的视觉元素——同一屏十几张卡片，
 * 边框连起来像表格线，「卡片」读起来更像「格子」。
 *
 * 现在边框降到 55%：**它仍在那里**（用于界定边界，尤其相邻卡片贴合时），
 * 但不再是视觉主角，让底色与留白承担分块。这与 Primer / Linear 的做法一致——
 * 容器的边界靠「底色差 + 极淡描边」表达，而不是靠实线分割。
 *
 * 需要更强边界感的场景（如可点击卡片 hover）由调用方显式加深，不在此处一刀切。
 */
export const cardSurface =
  'rounded-xl border border-border/55 text-card-foreground shadow-soft'

/**
 * 卡片底色的强弱档位。
 *
 * 【为什么需要收敛】替换前全站有 `bg-card/30`、`/35`、`/40`、`/45`、`/50`、`/60`、`/70`、
 * `/80`、`/90`、`/95`、`/96`、`/98` 共 **12 种**任意透明度。这些数字没有可分辨的语义——
 * 没人能说清 `/45` 与 `/50` 该用哪个，实际上它们来自不同时期的不同人各自微调。
 * 视觉上它们几乎不可区分（5% 的差在浅色底上肉眼难辨），却让「统一」无从谈起。
 *
 * 现在按**承载关系**分三档，每档有明确含义：
 * - `solid`  — 页面级主容器（默认）。卡片独立成块，需要最实的底。
 * - `subtle` — 嵌套在另一个容器**内部**的次级卡片。略透，让层次退后一层。
 * - `faint`  — 列表行、内联块、树节点等**成排出现**的轻量容器。最透，避免一片死白。
 *
 * 需要更弱的存在感时不要再写新的透明度——先问「它属于哪一种承载关系」，
 * 三档覆盖不了再加档，并在此处说明理由。
 */
export const cardSurfaceTone = {
  solid: 'bg-card/95',
  subtle: 'bg-card/70',
  faint: 'bg-card/40',
} as const

export type CardSurfaceTone = keyof typeof cardSurfaceTone

/** 中性悬停涂层：浅色主色底（accent）+ 对应前景色。 */
/**
 * 中性悬停涂层：浅色主色底（accent）+ 对应前景色。
 *
 * 取值从「实色 accent」改为「accent/70」：实色 hover 在密集工具栏里会跳出来抢注意力，
 * 一排按钮鼠标掠过时像在闪烁；70% 的浅涂足够表达「指针在这上面」，又不改变该区域的
 * 视觉重量。深色模式再降一档（/40），因为深色底上浅色涂层的观感差异被放大。
 */
export const hoverOverlay = 'hover:bg-accent/70 hover:text-accent-foreground dark:hover:bg-accent/40'

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
 *
 * 【按压梯度必须与 hover 拉开距离】原取值为 primary/80，而实心按钮的 hover 是
 * primary/90——两者只差 10% 不透明度，在浅色底上几乎不可分辨：鼠标悬停时按钮已经
 * 变了一次色，按下时几乎没有进一步变化，用户会怀疑「点没点中」。运维台操作延迟本就
 * 明显，这一点尤其致命。
 *
 * 现在的梯度：常态 100% → hover 90% → active 75%（相差 15%，肉眼可辨且不突兀）。
 * destructive 与 secondary 同理拉开。neutral（outline/ghost）走 accent 涂层，
 * 其 hover/active 已由 `hoverOverlay` 与 `pressOverlay` 分别定义，梯度同样成立。
 */
export function pressOverlayClass(tone: PressTone): string {
  switch (tone) {
    case 'primary':
      return 'active:bg-primary/75'
    case 'destructive':
      return 'active:bg-destructive/75 dark:active:bg-destructive/70'
    case 'secondary':
      return 'active:bg-secondary/65'
    case 'neutral':
    default:
      return pressOverlay
  }
}
