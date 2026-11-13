"use client"
/**
 * @file Separator：分隔线（radix-ui 原语包装；自上游 shadcn 基线拉取后适配本项目 token）。
 *       方向用 data-orientation 钩子（本项目 Tailwind 未装上游的 data-horizontal 自定义变体，
 *       一律写 `data-[orientation=…]` 显式形式）。
 * @input  lib/utils 的 cn、radix-ui 的 Separator
 * @output Separator
 * @sync   行为/样式变更时同步 display-primitives.test.tsx
 * @since  上游 shadcn 基线拉取适配（2026-10）
 */
import * as React from "react"
import { Separator as SeparatorPrimitive } from "radix-ui"

import { cn } from "../lib/utils"

function Separator({
  className,
  orientation = "horizontal",
  decorative = true,
  ...props
}: React.ComponentProps<typeof SeparatorPrimitive.Root>) {
  return (
    <SeparatorPrimitive.Root
      data-slot="separator"
      decorative={decorative}
      orientation={orientation}
      className={cn(
        "shrink-0 bg-border",
        "data-[orientation=horizontal]:h-px data-[orientation=horizontal]:w-full",
        "data-[orientation=vertical]:h-full data-[orientation=vertical]:w-px data-[orientation=vertical]:self-stretch",
        className,
      )}
      {...props}
    />
  )
}

export { Separator }
