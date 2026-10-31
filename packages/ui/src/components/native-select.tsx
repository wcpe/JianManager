"use client"
/**
 * @file NativeSelect：原生 select 的样式化包装（自上游 shadcn 基线拉取后适配本项目 token）。
 *       与 Select 的分工：原生语义/移动端系统选择器场景用它；千级候选的**受控**下拉一律走
 *       InstancePicker/Combobox（服务端搜索），不得用原生或 Radix Select 全量列举。
 * @input  lib/utils 的 cn、lucide-react 的 ChevronDownIcon
 * @output NativeSelect、NativeSelectOption、NativeSelectOptGroup
 * @sync   行为/样式变更时同步 native-select.test.tsx
 * @since  上游 shadcn 基线拉取适配（2026-10）
 */
import * as React from "react"
import { ChevronDownIcon } from "lucide-react"

import { cn } from "../lib/utils"

type NativeSelectProps = Omit<React.ComponentProps<"select">, "size"> & {
  size?: "sm" | "default"
}

function NativeSelect({ className, size = "default", ...props }: NativeSelectProps) {
  return (
    <div
      data-slot="native-select-wrapper"
      data-size={size}
      className={cn("group/native-select relative w-fit has-[select:disabled]:opacity-50", className)}
    >
      <select
        data-slot="native-select"
        data-size={size}
        className={cn(
          "h-9 w-full min-w-0 appearance-none rounded-md border border-input bg-transparent px-3 py-2 pr-9 text-sm shadow-xs transition-[color,box-shadow] outline-none",
          "placeholder:text-muted-foreground selection:bg-primary selection:text-primary-foreground dark:bg-input/30 dark:hover:bg-input/50",
          "focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50",
          "aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40",
          "disabled:pointer-events-none disabled:cursor-not-allowed",
          size === "sm" && "h-8",
        )}
        {...props}
      />
      <ChevronDownIcon
        aria-hidden="true"
        data-slot="native-select-icon"
        className="pointer-events-none absolute top-1/2 right-3 size-4 -translate-y-1/2 text-muted-foreground select-none"
      />
    </div>
  )
}

function NativeSelectOption({ className, ...props }: React.ComponentProps<"option">) {
  return <option data-slot="native-select-option" className={cn("bg-background text-foreground", className)} {...props} />
}

function NativeSelectOptGroup({ className, ...props }: React.ComponentProps<"optgroup">) {
  return (
    <optgroup
      data-slot="native-select-optgroup"
      className={cn("bg-background text-foreground", className)}
      {...props}
    />
  )
}

export { NativeSelect, NativeSelectOptGroup, NativeSelectOption }
