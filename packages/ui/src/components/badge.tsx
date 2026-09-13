/* eslint-disable react-refresh/only-export-components -- shadcn 组件随组件导出 cva 变体，仅影响 Fast Refresh */
import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"
import { Slot } from "radix-ui"

import { cn } from "../lib/utils"

const badgeVariants = cva(
  "inline-flex w-fit shrink-0 items-center justify-center gap-1 overflow-hidden rounded-full border border-transparent px-2 py-0.5 text-xs font-medium whitespace-nowrap transition-[color,box-shadow] focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 [&>svg]:pointer-events-none [&>svg]:size-3",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground [a&]:hover:bg-primary/90",
        secondary:
          "bg-secondary text-secondary-foreground [a&]:hover:bg-secondary/90",
        destructive:
          "bg-destructive text-white focus-visible:ring-destructive/20 dark:bg-destructive/60 dark:focus-visible:ring-destructive/40 [a&]:hover:bg-destructive/90",
        outline:
          "border-border text-foreground [a&]:hover:bg-accent [a&]:hover:text-accent-foreground",
        ghost: "[a&]:hover:bg-accent [a&]:hover:text-accent-foreground",
        link: "text-primary underline-offset-4 [a&]:hover:underline",
        // 方案 A「精工卡片」柔和实底状态 chip（opt-in）：浅实底 + 描边 + 前导圆点（取 currentColor），
        // 密度比 default/outline 更紧凑（11px/600、6px 圆角）。基础类带胶囊圆角与常规字号，
        // 这里用 important 覆盖圆角/字号/内距等冲突项，仅在本变体生效，不影响既有 variant 观感。
        // 颜色走 --status-* 派生的 weak/ink/line 令牌，明暗自适应。
        "chip-ok":
          "rounded-[6px]! gap-1.5! px-2! py-[3px]! text-[11px]! font-semibold! border-[color:var(--success-line)]! bg-[color:var(--success-weak)]! text-[color:var(--success-ink)]! before:content-[''] before:size-1.5 before:shrink-0 before:rounded-full before:bg-current",
        "chip-bad":
          "rounded-[6px]! gap-1.5! px-2! py-[3px]! text-[11px]! font-semibold! border-[color:var(--danger-line)]! bg-[color:var(--danger-weak)]! text-[color:var(--danger-ink)]! before:content-[''] before:size-1.5 before:shrink-0 before:rounded-full before:bg-current",
        "chip-idle":
          "rounded-[6px]! gap-1.5! px-2! py-[3px]! text-[11px]! font-semibold! border-border! bg-muted! text-muted-foreground! before:content-[''] before:size-1.5 before:shrink-0 before:rounded-full before:bg-current",
        "chip-neutral":
          "rounded-[6px]! gap-1.5! px-2! py-[3px]! text-[11px]! font-semibold! border-border! bg-secondary! text-secondary-foreground!",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
)

function Badge({
  className,
  variant = "default",
  asChild = false,
  ...props
}: React.ComponentProps<"span"> &
  VariantProps<typeof badgeVariants> & { asChild?: boolean }) {
  const Comp = asChild ? Slot.Root : "span"

  return (
    <Comp
      data-slot="badge"
      data-variant={variant}
      className={cn(badgeVariants({ variant }), className)}
      {...props}
    />
  )
}

export { Badge, badgeVariants }
