"use client"
/**
 * @file Slider：滑杆（radix-ui 原语包装；自上游 shadcn 基线拉取后适配本项目 token）。
 *       支持多拇指（value/defaultValue 数组）；方向钩子写 `data-[orientation=…]` 显式形式。
 * @input  lib/utils 的 cn、radix-ui 的 Slider
 * @output Slider
 * @sync   行为/样式变更时同步 slider.test.tsx
 * @since  上游 shadcn 基线拉取适配（2026-10）
 */
import * as React from "react"
import { Slider as SliderPrimitive } from "radix-ui"

import { cn } from "../lib/utils"

function Slider({
  className,
  defaultValue,
  value,
  min = 0,
  max = 100,
  // 可访问名转发（上游基线只把它留在 Root 上，而 role=slider 的**拇指**才是可聚焦元素——
  // 名字不落在可聚焦元素上等于没有，这里显式下传给每个拇指）。
  "aria-label": ariaLabel,
  ...props
}: React.ComponentProps<typeof SliderPrimitive.Root>) {
  const _values = React.useMemo(
    () =>
      Array.isArray(value)
        ? value
        : Array.isArray(defaultValue)
          ? defaultValue
          : [min, max],
    [value, defaultValue, min, max],
  )

  return (
    <SliderPrimitive.Root
      data-slot="slider"
      defaultValue={defaultValue}
      value={value}
      min={min}
      max={max}
      className={cn(
        "relative flex w-full touch-none items-center select-none data-[disabled]:opacity-50",
        "data-[orientation=vertical]:h-full data-[orientation=vertical]:w-auto data-[orientation=vertical]:flex-col",
        className,
      )}
      {...props}
    >
      <SliderPrimitive.Track
        data-slot="slider-track"
        className={cn(
          "relative grow overflow-hidden rounded-full bg-muted",
          "data-[orientation=horizontal]:h-1.5 data-[orientation=horizontal]:w-full",
          "data-[orientation=vertical]:h-full data-[orientation=vertical]:w-1.5",
        )}
      >
        <SliderPrimitive.Range
          data-slot="slider-range"
          className={cn(
            "absolute bg-primary",
            "data-[orientation=horizontal]:h-full data-[orientation=vertical]:w-full",
          )}
        />
      </SliderPrimitive.Track>
      {Array.from({ length: _values.length }, (_, index) => (
        <SliderPrimitive.Thumb
          data-slot="slider-thumb"
          key={index}
          aria-label={ariaLabel}
          className={cn(
            "block size-4 shrink-0 rounded-full border border-primary bg-background shadow-sm transition-[color,box-shadow] select-none",
            "hover:ring-4 hover:ring-ring/50 focus-visible:ring-4 focus-visible:ring-ring/50 focus-visible:outline-none",
            "disabled:pointer-events-none disabled:opacity-50",
          )}
        />
      ))}
    </SliderPrimitive.Root>
  )
}

export { Slider }
