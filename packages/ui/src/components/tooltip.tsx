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
  children,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Provider>) {
  return (
    <TooltipPrimitive.Provider data-slot="tooltip-provider" delayDuration={delayDuration} {...props}>
      {/* 标记「已在上文」，让 `Tooltip` 不再重复包一层 Provider。 */}
      <TooltipProviderPresent.Provider value={true}>{children}</TooltipProviderPresent.Provider>
    </TooltipPrimitive.Provider>
  )
}

/**
 * 【为什么这里自带 Provider】Radix 的 `Tooltip.Root` 必须处在 `Tooltip.Provider` 内，
 * 否则运行期直接抛 `\`Tooltip\` must be used within \`TooltipProvider\``。
 *
 * 此前约定「调用方负责包 Provider」，实际后果是：任何独立渲染该组件的场景（单测直接
 * `render(<X/>)`、组件博物馆预览、将来插件复用）都会炸，而报错点落在被测组件内部，
 * 排查成本远高于收益。切换成本却只是多一层 Provider——Radix 的 Provider 本身可嵌套且
 * 无额外 DOM，重复包裹是安全的（内层覆盖外层配置）。
 *
 * 因此改为**自带兜底**：检测到外层已有 Provider 时直接复用（共享 delayDuration 等配置），
 * 没有时才自己包一层。全局 Provider（`App.tsx`）依旧保留，用来统一 `delayDuration`。
 */
const TooltipProviderPresent = React.createContext(false)

function Tooltip({ ...props }: React.ComponentProps<typeof TooltipPrimitive.Root>) {
  const inherited = React.useContext(TooltipProviderPresent)
  const root = <TooltipPrimitive.Root data-slot="tooltip" {...props} />
  if (inherited) return root
  return (
    <TooltipProvider>
      <TooltipProviderPresent.Provider value={true}>{root}</TooltipProviderPresent.Provider>
    </TooltipProvider>
  )
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
