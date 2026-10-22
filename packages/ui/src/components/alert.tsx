"use client"
/* eslint-disable react-refresh/only-export-components -- shadcn 组件随组件导出 cva 变体，仅影响 Fast Refresh */
/**
 * @file Alert：页内提示条（自上游 shadcn 基线拉取后适配本项目 token）。
 *       非模态、非弹层：承载说明/警告文本，可带标题、描述与动作槽位。
 *       破坏性操作的最后确认走 DangerConfirm（弹窗），不用本组件。
 * @input  lib/utils 的 cn、class-variance-authority
 * @output Alert、AlertTitle、AlertDescription、AlertAction、alertVariants
 * @sync   变体变更时同步 display-primitives.test.tsx
 * @since  上游 shadcn 基线拉取适配（2026-10）
 */
import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"

import { cn } from "../lib/utils"

const alertVariants = cva(
  "group/alert relative grid w-full grid-cols-[0_1fr] items-start gap-y-0.5 rounded-lg border px-4 py-3 text-sm has-[>svg]:grid-cols-[calc(var(--spacing)*4)_1fr] has-[>svg]:gap-x-3 [&>svg]:size-4 [&>svg]:translate-y-0.5 [&>svg]:text-current",
  {
    variants: {
      variant: {
        default: "bg-card text-card-foreground",
        destructive:
          "bg-card text-destructive *:data-[slot=alert-description]:text-destructive/90 [&>svg]:text-current",
        /*
          【为什么补 warning / success / info】初版只有 default 与 destructive，
          而主控台实际大量警告用的是 `status-warning` 语义色（如运行态漂移提示、
          配额接近上限、客户端更新成功率偏低）。缺 this 档的直接后果是：
          项目里 40+ 处告警条全部手写，因为「带边框带图标的警告框」没有对应组件可用。

          这三档的着色一律走项目既有的 `--status-*` 色板（与 StatCard 的 StatusLevel、
          threshold 的语义色同源），不引入新的颜色变量——否则又会出现两套状态色。
        */
        warning:
          "border-status-warning/40 bg-status-warning/10 text-status-warning *:data-[slot=alert-description]:text-status-warning/90",
        success: "border-status-success/40 bg-status-success/10 text-status-success",
        info: "border-primary/40 bg-primary/[0.06] text-primary",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  },
)

function Alert({
  className,
  variant,
  ...props
}: React.ComponentProps<"div"> & VariantProps<typeof alertVariants>) {
  return (
    <div
      data-slot="alert"
      role="alert"
      className={cn(alertVariants({ variant }), className)}
      {...props}
    />
  )
}

function AlertTitle({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="alert-title"
      className={cn(
        "col-start-2 line-clamp-1 min-h-4 font-medium tracking-tight",
        className,
      )}
      {...props}
    />
  )
}

function AlertDescription({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="alert-description"
      className={cn(
        "col-start-2 grid justify-items-start gap-1 text-sm text-muted-foreground [&_p]:leading-relaxed",
        className,
      )}
      {...props}
    />
  )
}

/**
 * 提示条的动作槽位。
 *
 * 【默认绝对定位为什么改成可选】初版写死 `absolute top-3 right-3`，只适合
 * 「右上角一个关闭/采纳按钮」这一种形态。但主控台的告警条里动作五花八门：
 * 有的跟在文案后面（行内）、有的在下方（块状）、有的要占满一行。
 * 写死绝对定位会把这些全部顶到角上，替换时反而比手写更麻烦。
 *
 * 现在 `inline` 为默认（相对定位，随文档流排在描述下方/右侧），
 * 只有确实要「角标」形态时才传 `corner`。
 */
function AlertAction({
  className,
  placement = "inline",
  ...props
}: React.ComponentProps<"div"> & {
  /** 动作位置：`inline` 随流排布（默认）；`corner` 贴右上角。 */
  placement?: "inline" | "corner"
}) {
  return (
    <div
      data-slot="alert-action"
      data-placement={placement}
      className={cn(
        placement === "corner" ? "absolute top-3 right-3" : "col-start-2 mt-1",
        className,
      )}
      {...props}
    />
  )
}

export { Alert, AlertAction, AlertDescription, AlertTitle, alertVariants }
