"use client"
/**
 * @file Switch：开关（radix-ui 原语包装；自上游 shadcn 基线拉取后适配本项目 token）。
 *       形态与 Checkbox 同族（同一焦点环/禁用态口径）；尺寸走 size 变体 + data-size 钩子。
 * @input  lib/utils 的 cn、radix-ui 的 Switch
 * @output Switch
 * @sync   行为/样式变更时同步 switch.test.tsx
 * @since  上游 shadcn 基线拉取适配（2026-10）
 */
import * as React from "react"
import { Switch as SwitchPrimitive } from "radix-ui"

import { cn } from "../lib/utils"

function Switch({
  className,
  size = "default",
  ...props
}: React.ComponentProps<typeof SwitchPrimitive.Root> & {
  size?: "sm" | "default"
}) {
  return (
    <SwitchPrimitive.Root
      data-slot="switch"
      data-size={size}
      className={cn(
        "peer group/switch inline-flex shrink-0 items-center rounded-full border border-transparent shadow-xs transition-all outline-none",
        "focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50",
        "disabled:cursor-not-allowed disabled:opacity-50",
        "data-[state=checked]:bg-primary data-[state=unchecked]:bg-input dark:data-[state=unchecked]:bg-input/80",
        size === "sm" ? "h-4 w-6" : "h-[1.15rem] w-8",
        className,
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb
        data-slot="switch-thumb"
        className={cn(
          "pointer-events-none block rounded-full bg-background ring-0 transition-transform",
          size === "sm"
            ? "size-3 data-[state=checked]:translate-x-2 data-[state=unchecked]:translate-x-0"
            : "size-4 data-[state=checked]:translate-x-[calc(100%-2px)] data-[state=unchecked]:translate-x-0",
        )}
      />
    </SwitchPrimitive.Root>
  )
}

export { Switch }
