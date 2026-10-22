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

/**
 * 分段控件（`.segments` 原型）的共享取值：凹槽底 + 白块选中。
 *
 * 【为什么抽成常量】`Segments`（layout/Segments.tsx）与 `ToggleGroup` 的 `segment` 变体
 * 表达的是**同一个视觉形态**——「同一份数据的少数几种呈现之间就地切换」。此前这是两套
 * 各自手写的取值：`Segments` 用 `bg-muted` 凹槽 + `bg-card` 选中，`ToggleGroup` 用
 * `toggleVariants` 的按钮风，于是同一个交互在站内有两种外观。
 *
 * 取值收敛到这里之后，两种实现都引用它：改圆角/底色/选中态只需改这一处。
 * 两者的差异只在**语义与能力**，不在视觉：
 *   - `Segments`：`role=tablist`，纯展示切换条，内容留在调用方；
 *   - `ToggleGroup`：Radix 的 ToggleGroup 语义，支持多选（`type="multiple"`）、
 *     方向键导航由 Radix 托管。
 * 需要「就地切换呈现」且要 `type="multiple"` 或多选能力时用 `ToggleGroup`，
 * 否则用更轻的 `Segments`。
 */
/*
  【分段控件的视觉口径】三道边界要同时说清，否则凹槽与选中块会糊成一片：

  1. 轨道（segmentTrack）：`bg-muted` 凹槽 + `border` 外框。凹槽比页面底色深一档，
     让「这一组是可切换的」在未操作时就能看出来；外框只保留极淡描边（40% 不透明度），
     因为凹槽本身已经有边界，实线框会与外层卡片边框形成「框中框」的噪音。
  2. 未选中项（segmentItem）：透明底、次要文字色。不设 hover 底色——凹槽内的
     小块 hover 会把它撑成一个「假选中块」，与真正的选中态混淆；只提升文字对比度。
  3. 选中项（segmentItemActive）：卡片底色 + 极浅阴影 + 加粗。阴影用 1px/4%
     而不是内建 shadow-sm：凹槽内空间小，阴影稍重就像贴纸浮起来，破坏「嵌在槽里的块」。
*/
export const segmentTrack =
  'inline-flex shrink-0 items-center gap-[2px] rounded-lg border border-border/40 bg-muted p-[3px]'

/** 分段轨内的单个分段项（未选中态）。选中态见 `segmentItemActive`。 */
export const segmentItem =
  'rounded-md px-[10px] py-1 text-[11px] whitespace-nowrap text-muted-foreground transition-[color,background-color] duration-[var(--motion-duration-normal)] ease-ios hover:text-foreground'

/** 分段项的选中态：抬为卡片底色 + 极浅阴影 + 加粗。 */
export const segmentItemActive =
  'bg-card font-medium text-foreground shadow-[0_1px_2px_rgb(var(--shadow-color)/0.04)]'

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
        /**
         * 分段风：与 `Segments` 同一套取值（见上方 `segmentItem` / `segmentItemActive`）。
         * 用于需要 Radix ToggleGroup 能力的场景（多选、方向键导航），
         * 使它与手写的凹槽分段在站内看起来是同一个控件。
         */
        segment: cn(
          segmentItem,
          "hover:bg-transparent data-[state=on]:bg-card data-[state=on]:font-medium",
          "data-[state=on]:text-foreground",
          "data-[state=on]:shadow-[0_1px_2px_rgb(var(--shadow-color)/0.04)]",
          "focus-visible:ring-2 focus-visible:ring-ring/40",
        ),
      },
      size: {
        default: "h-9 min-w-9 px-2",
        sm: "h-8 min-w-8 px-1.5",
        lg: "h-10 min-w-10 px-2.5",
        /** 分段专用尺寸：与 `Segments` 的 `px-[10px] py-1 text-[11px]` 对齐（不设固定高度）。 */
        segment: "h-auto min-w-0 px-[10px] py-1 text-[11px]",
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
