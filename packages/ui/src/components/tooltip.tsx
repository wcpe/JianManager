"use client"
/**
 * @file Tooltip：悬停/聚焦提示（radix-ui 原语包装；自上游 shadcn 基线拉取后适配本项目 token）。
 *       与 HoverCard 的分工：Tooltip 只承载**纯文本**提示；结构化预览（多行/徽章）用 HoverCard。
 * @input  lib/utils 的 cn、radix-ui 的 Tooltip
 * @output TooltipProvider、Tooltip、TooltipTrigger、TooltipContent
 * @sync   用法注意：Tooltip 需被 TooltipProvider 包裹（可局部包裹；delayDuration 默认 0 与上游一致）
 * @since  上游 shadcn 基线拉取适配（2026-10）
 */
import * as React from "react"
import { Tooltip as TooltipPrimitive } from "radix-ui"

import { cn } from "../lib/utils"

function TooltipProvider({
  delayDuration = 0,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Provider>) {
  return <TooltipPrimitive.Provider data-slot="tooltip-provider" delayDuration={delayDuration} {...props} />
}

function Tooltip({ ...props }: React.ComponentProps<typeof TooltipPrimitive.Root>) {
  return <TooltipPrimitive.Root data-slot="tooltip" {...props} />
}

function TooltipTrigger({ ...props }: React.ComponentProps<typeof TooltipPrimitive.Trigger>) {
  return <TooltipPrimitive.Trigger data-slot="tooltip-trigger" {...props} />
}

function TooltipContent({
  className,
  sideOffset = 0,
  children,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Content>) {
  return (
    <TooltipPrimitive.Portal>
      <TooltipPrimitive.Content
        data-slot="tooltip-content"
        sideOffset={sideOffset}
        className={cn(
          "z-[400] w-fit max-w-xs rounded-md bg-foreground px-3 py-1.5 text-xs text-balance text-background",
          "origin-(--radix-tooltip-content-transform-origin)",
          "data-[state=delayed-open]:animate-in data-[state=delayed-open]:fade-in-0 data-[state=delayed-open]:zoom-in-95",
          "data-[state=instant-open]:animate-in data-[state=instant-open]:fade-in-0",
          className,
        )}
        {...props}
      >
        {children}
        <TooltipPrimitive.Arrow className="z-[400] size-2.5 translate-y-[calc(-50%_-_2px)] rotate-45 rounded-[2px] bg-foreground fill-foreground" />
      </TooltipPrimitive.Content>
    </TooltipPrimitive.Portal>
  )
}

export { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger }
