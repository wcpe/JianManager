"use client"
/**
 * @file Progress：进度条（radix-ui 原语包装；自上游 shadcn 基线拉取后适配本项目 token）。
 *       只表达「完成比例」；带状态的容量/健康读数用 gauge / mini-bar。
 * @input  lib/utils 的 cn、radix-ui 的 Progress
 * @output Progress
 * @sync   行为/样式变更时同步 progress.test.tsx
 * @since  上游 shadcn 基线拉取适配（2026-10）
 */
import * as React from "react"
import { Progress as ProgressPrimitive } from "radix-ui"

import { cn } from "../lib/utils"

function Progress({
  className,
  value,
  ...props
}: React.ComponentProps<typeof ProgressPrimitive.Root>) {
  return (
    <ProgressPrimitive.Root
      data-slot="progress"
      // `value` 显式下传（上游基线把值只给了指示条的 transform，Root 拿不到值就退化为
      // indeterminate，aria-valuenow 会整体缺失——本处修正该 a11y 缺口）。
      value={value}
      className={cn(
        "relative flex h-2 w-full items-center overflow-x-hidden rounded-full bg-primary/20",
        className,
      )}
      {...props}
    >
      <ProgressPrimitive.Indicator
        data-slot="progress-indicator"
        className="size-full flex-1 rounded-full bg-primary transition-all"
        style={{ transform: `translateX(-${100 - (value || 0)}%)` }}
      />
    </ProgressPrimitive.Root>
  )
}

export { Progress }
