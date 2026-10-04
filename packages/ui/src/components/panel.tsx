/**
 * @file Panel：分区面板原语（标题栏 + 内容区 + 底部区三段式），全站卡片基座。
 * @input  lib/utils 的 cn、lib/tone、lib/interaction-overlay 的 shadowTransition
 * @output Panel、PanelProps
 * @sync   改动三段结构或 data-slot 钩子时同步 panel.test.tsx；页面外壳依赖其 bodyClassName 约定
 * @since  FR-496
 */
import * as React from 'react'

import { cn } from '../lib/utils'
import { toneChipClass, type Tone } from '../lib/tone'
import { shadowTransition } from '../lib/interaction-overlay'

/**
 * 分区面板（FR-061/FR-163）：大圆角 + 柔和阴影统一卡片原语，
 * 可选标题栏（图标/标题 + 头部操作）+ 内容区 + 底部区（FR-496 阶段 3 补齐的三段式）。
 *
 * FR-496 阶段 6 的收敛点：
 * - 三段各自带 `data-slot` 钩子（panel-header / panel-body / panel-footer），
 *   页面可以精确命中某一段做样式或断言，不必再靠 `:first-child` 猜结构；
 * - 间距与字号回到设计标度（4px 栅格 / `text-xs`），不再写 `px-[15px] text-[11px]`
 *   这类脱离标度的任意值——任意值在换主题时不会跟着变；
 * - hover 抬升的过渡取 lib 里的共享常量（时长绑 motion token）。
 */
export function Panel({
  title,
  actions,
  children,
  className,
  bodyClassName,
  icon,
  tone = 'primary',
  hoverable,
  footer,
  ...props
}: Omit<React.ComponentProps<'div'>, 'title'> & {
  title?: React.ReactNode
  /** 标题栏右侧操作区（按钮/选择器等）。 */
  actions?: React.ReactNode
  /** 内容区额外类名（覆盖默认 padding）。 */
  bodyClassName?: string
  /** 标题左侧语义图标块（传入图标元素时显示）。 */
  icon?: React.ReactNode
  /** 图标块色调，默认主色。 */
  tone?: Tone
  /** 启用 hover 反馈（主色晕染阴影 shadow-lift，iOS 缓动；FR-176 去位移、只换阴影不抬位）。 */
  hoverable?: boolean
  /** 底部区（FR-496 阶段 3）：对齐原型 `.panel-foot` 三段式，用于分页、合计、批量操作等。 */
  footer?: React.ReactNode
}) {
  return (
    <div
      data-slot="panel"
      className={cn(
        // 【为什么去掉 backdrop-blur-sm】底色是 bg-card/95（95% 不透明），模糊只作用于透出的
        // 那 5%，肉眼几乎不可见——是纯成本：每个 Panel 都因此成为一个独立合成层，
        // 祖先尺寸一变就要重算背景模糊。实测（主控台 1141 元素、24 个模糊层）：
        // 禁用 backdrop-filter 后侧栏开合过渡从 51.6fps / p95 33.4ms 升到
        // 58.8fps / p95 16.8ms，严重掉帧由 10 帧降到 2 帧。Panel 是其中 22 个的来源，
        // 而全仓 <Panel> 有 183 处调用，是单点收益最大的一处。
        // 需要真毛玻璃效果的场景（下方确有内容滚动）请显式传 className 加回。
        'flex min-h-0 flex-col rounded-lg border bg-card/95 text-card-foreground shadow-soft',
        hoverable &&
          // FR-176：hover 只换阴影不位移（`hover:shadow-lift`）。
          // FR-244：时长/缓动取 motion token（slow≈320ms），不写定值。
          // FR-496：过渡串取 lib/interaction-overlay 的 shadowTransition —— 取值单一真源。
          // 全局守护测试已从「钉本文件出现字面量」改为「字面量或常量均可，但常量文件必须绑定
          // motion token」，所以这里可以放心用常量。
          `${shadowTransition} hover:shadow-lift`,
        className,
      )}
      {...props}
    >
      {(title || actions || icon) && (
        <div
          data-slot="panel-header"
          className="flex shrink-0 items-center justify-between gap-2 border-b bg-muted/30 px-3 py-2"
        >
          <div className="flex min-w-0 items-center gap-2">
            {icon && (
              <span
                className={cn('flex size-6 shrink-0 items-center justify-center rounded-md', toneChipClass(tone))}
              >
                {icon}
              </span>
            )}
            {title && <div className="truncate text-[13px] font-semibold tracking-wide text-foreground">{title}</div>}
          </div>
          {actions && <div className="flex shrink-0 items-center gap-1">{actions}</div>}
        </div>
      )}
      <div data-slot="panel-body" className={cn('min-h-0 flex-1 p-3', bodyClassName)}>
        {children}
      </div>
      {footer && (
        <div
          data-slot="panel-footer"
          className="flex shrink-0 items-center justify-between gap-2 border-t px-4 py-2.5 text-xs text-muted-foreground"
        >
          {footer}
        </div>
      )}
    </div>
  )
}
