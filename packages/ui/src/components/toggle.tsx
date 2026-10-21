"use client"
/* eslint-disable react-refresh/only-export-components -- shadcn 组件随组件导出 cva 变体，仅影响 Fast Refresh */
/**
 * @file Toggle：双态按钮（radix-ui 原语包装；自上游 shadcn 基线拉取后适配本项目 token）。
 *       与 Button 的分工：Button 表达动作；Toggle 表达**持久开关态**（data-state=on/off）。
 *       成组的互斥/多选切换用 ToggleGroup（变体经 toggleVariants 与本文同源）。
 * @input  lib/utils 的 cn、class-variance-authority、radix-ui 的 Toggle
 * @output Toggle、toggleVariants
 * @sync   变体变更时同步 toggle.test.tsx 与 toggle-group.tsx
 * @since  上游 shadcn 基线拉取适配（2026-10）
 */
import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"
import { Toggle as TogglePrimitive } from "radix-ui"

import { cn } from "../lib/utils"

const toggleVariants = cva(
  cn(
    "group/toggle inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium whitespace-nowrap outline-none transition-[color,box-shadow]",
    "focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50",
    "disabled:pointer-events-none disabled:opacity-50",
    "[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
  ),
  {
    variants: {
      variant: {
        default: "bg-transparent hover:bg-muted hover:text-muted-foreground data-[state=on]:bg-accent data-[state=on]:text-accent-foreground",
        outline:
          "border border-input bg-transparent shadow-xs hover:bg-accent hover:text-accent-foreground data-[state=on]:bg-accent data-[state=on]:text-accent-foreground",
      },
      size: {
        default: "h-9 min-w-9 px-2",
        sm: "h-8 min-w-8 px-1.5",
        lg: "h-10 min-w-10 px-2.5",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  },
)

function Toggle({
  className,
  variant = "default",
  size = "default",
  ...props
}: React.ComponentProps<typeof TogglePrimitive.Root> & VariantProps<typeof toggleVariants>) {
  return (
    <TogglePrimitive.Root
      data-slot="toggle"
      className={cn(toggleVariants({ variant, size, className }))}
      {...props}
    />
  )
}

export { Toggle, toggleVariants }
