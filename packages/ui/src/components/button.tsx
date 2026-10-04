/* eslint-disable react-refresh/only-export-components -- shadcn 组件随组件导出 cva 变体，仅影响 Fast Refresh */
import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"
import { Slot } from "radix-ui"

import { cn } from "../lib/utils"
import { focusRing, focusRingClass, invalidFieldState } from "../lib/focus-ring"
import {
  disabledState,
  hoverOverlay,
  interactionOverlay,
  interactionTransition,
  pressOverlayClass,
} from "../lib/interaction-overlay"
import { Spinner } from "./spinner"

/**
 * 判定子节点里是否存在「可见文案」。
 *
 * 用途见 `Button` 的 tooltip：只有图标、没有文字的按钮，屏幕阅读器只会念"按钮"，
 * 必须由 tooltip 补上名字；而带文字的按钮若用同一段文本覆盖可访问名，会违反
 * WCAG 2.5.3（可见标签必须包含在可访问名里）。
 *
 * 只对宿主元素继续下钻：函数组件（图标组件）的 children 在渲染前不可见，
 * 保守当作"无文案"——图标按钮正是它的典型场景。
 */
function hasVisibleText(node: React.ReactNode): boolean {
  if (typeof node === "string" || typeof node === "number") return true
  if (Array.isArray(node)) return node.some(hasVisibleText)
  if (React.isValidElement(node)) {
    if (typeof node.type !== "string") return false
    const props = node.props as {
      children?: React.ReactNode
      "aria-hidden"?: boolean | "true" | "false"
    }
    // 装饰性子树里的文字不进可访问名（例如图标字形包在 aria-hidden 的 span 里），
    // 不能算作"按钮有文案"
    if (props["aria-hidden"] === true || props["aria-hidden"] === "true") return false
    return hasVisibleText(props.children)
  }
  return false
}

const buttonVariants = cva(
  cn(
    "inline-flex shrink-0 items-center justify-center gap-2 rounded-md text-sm font-medium whitespace-nowrap outline-none",
    // FR-496 阶段 6：焦点环/过渡/禁用态全部改取 lib 里的统一常量，
    // 组件只表达"用哪一种"，不再各自拼同一串类名（拼错时既不报错也很难发现）。
    interactionTransition,
    disabledState,
    focusRing,
    invalidFieldState,
    "[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
  ),
  {
    variants: {
      variant: {
        default: cn(
          "bg-primary text-primary-foreground hover:bg-primary/90",
          pressOverlayClass("primary"),
        ),
        // 销毁性按钮的焦点环换红色（见 lib/focus-ring 的说明），按压色同样落在 destructive 色系内
        destructive: cn(
          "bg-destructive text-white hover:bg-destructive/90 dark:bg-destructive/60",
          pressOverlayClass("destructive"),
          focusRingClass({ tone: "destructive" }),
        ),
        outline: cn(
          "border bg-background shadow-xs dark:border-input dark:bg-input/30",
          hoverOverlay,
          pressOverlayClass("neutral"),
          // 深色下的悬停底不用 accent 而用 input：描边按钮自身是浅底，accent 太接近背景色
          "dark:hover:bg-input/50",
        ),
        secondary: cn(
          "bg-secondary text-secondary-foreground hover:bg-secondary/80",
          pressOverlayClass("secondary"),
        ),
        ghost: interactionOverlay,
        link: "text-primary underline-offset-4 hover:underline active:text-primary/80",
      },
      size: {
        default: "h-9 px-4 py-2 has-[>svg]:px-3",
        xs: "h-6 gap-1 rounded-md px-2 text-xs has-[>svg]:px-1.5 [&_svg:not([class*='size-'])]:size-3",
        sm: "h-8 gap-1.5 rounded-md px-3 has-[>svg]:px-2.5",
        lg: "h-10 rounded-md px-6 has-[>svg]:px-4",
        icon: "size-9",
        "icon-xs": "size-6 rounded-md [&_svg:not([class*='size-'])]:size-3",
        "icon-sm": "size-8",
        "icon-lg": "size-10",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
)

function Button({
  className,
  variant = "default",
  size = "default",
  asChild = false,
  isLoading = false,
  tooltip,
  disabled,
  title,
  children,
  ...props
}: React.ComponentProps<"button"> &
  VariantProps<typeof buttonVariants> & {
    asChild?: boolean
    /**
     * 加载中（FR-496 阶段 6）：内置 Spinner + 自动禁用 + `aria-busy`。
     *
     * 三个动作必须捆在一起：只转圈不禁用会重复提交，只禁用不转圈看起来像坏了，
     * 不标 `aria-busy` 则辅助技术用户完全不知道刚才那次点击在等什么。
     * 文案不要跟着改（如"保存中…"）：可访问名一旦变化，正在操作的控件就从用户手里"消失"了。
     */
    isLoading?: boolean
    /**
     * 悬停提示（FR-496 阶段 6）。
     *
     * 实现为原生 `title`，不是自绘气泡：气泡需要 wrapper 元素或 Portal，
     * 而 Button 有 139 处调用、多在 flex 行内与表格单元格里，插一层包裹会改变布局
     * 与 `:first-child` 之类的选择器语义；`title` 则完全不加 DOM，且与仓库既有做法一致。
     *
     * 图标按钮（无可见文案）会同时把该文本作为可访问名——这是它比"纯提示"更重要的价值：
     * 只有图标的按钮，屏幕阅读器本来只会念"按钮"。
     */
    tooltip?: string
  }) {
  const Comp = asChild ? Slot.Root : "button"

  // 加载中一律禁用：此时点击要么被后端丢弃，要么造成重复提交
  const isDisabled = disabled === true || isLoading

  // 无可见文案的按钮把 tooltip 当可访问名（调用点显式传的 aria-label/aria-labelledby 优先，
  // 由后面的 {...props} 覆盖本次推断）
  const isIconOnly = !hasVisibleText(children)
  const inferredLabel =
    tooltip !== undefined &&
    isIconOnly &&
    props["aria-label"] === undefined &&
    props["aria-labelledby"] === undefined
      ? tooltip
      : undefined

  // 加载态要把 Spinner 与原有内容一起交给底层元素。这里必须"整串替换"而不是
  // 在 JSX 里并排放两个表达式：`asChild` 走 Radix Slot，它要求**恰好一个**元素子节点，
  // 多出来的 `false`/`null` 占位也会让它判定为"多子节点"并抛错。
  const content =
    !asChild && isLoading ? (
      <>
        <Spinner />
        {children}
      </>
    ) : (
      children
    )

  return (
    <Comp
      data-slot="button"
      data-variant={variant}
      data-size={size}
      data-loading={isLoading ? "true" : undefined}
      aria-busy={isLoading ? true : undefined}
      aria-label={inferredLabel}
      disabled={isDisabled}
      title={title ?? tooltip}
      className={cn(buttonVariants({ variant, size, className }))}
      {...props}
    >
      {content}
    </Comp>
  )
}

export { Button, buttonVariants }
