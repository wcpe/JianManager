"use client"
/**
 * @file Dialog：模态对话框基座。表单类弹窗须配合 scrollable-dialog 壳使用
 *       （约束见 .claude/rules/ui-modals.md），本文件本身不实现滚动区。
 * @input  lib/utils 的 cn、lib/focus-ring、Button、radix-ui 的 Dialog；关闭文案取 i18n 键 common.close
 * @output Dialog 及其子组件（Trigger / Content / Header / Title / Footer 等）
 * @sync   改动结构或关闭语义时同步 dialog.test.tsx；模态约束同步 .claude/rules/ui-modals.md
 * @since  FR-496
 */
import * as React from "react"
import { XIcon } from "lucide-react"
import { useTranslation } from "react-i18next"
import { Dialog as DialogPrimitive } from "radix-ui"

import { cn } from "../lib/utils"
import { focusRing } from "../lib/focus-ring"
import { Button } from "./button"

/**
 * 关闭文案的 i18n 键与兜底值（FR-496 阶段 6）。
 *
 * 组件库不自己初始化 i18next——初始化意味着替宿主决定语言与资源。这里只在宿主已初始化
 * 时按键取值，取不到就回落英文，因此「未接 i18n 的应用/组件库单测」的行为与硬编码英文完全一致。
 * 键名取主控台 common.close（zh=关闭 / en=Close），宿主已有资源可直接生效。
 */
const DIALOG_CLOSE_LABEL_KEY = "common.close"
const DIALOG_CLOSE_LABEL_DEFAULT = "Close"

/** 解析关闭按钮文案：显式传入 > i18n > 英文兜底。 */
function useDialogCloseLabel(closeLabel?: string): string {
  const { t } = useTranslation()
  if (closeLabel !== undefined) return closeLabel
  return t(DIALOG_CLOSE_LABEL_KEY, { defaultValue: DIALOG_CLOSE_LABEL_DEFAULT })
}

function Dialog({
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Root>) {
  return <DialogPrimitive.Root data-slot="dialog" {...props} />
}

function DialogTrigger({
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Trigger>) {
  return <DialogPrimitive.Trigger data-slot="dialog-trigger" {...props} />
}

/**
 * 门户（FR-496 阶段 6 修复死钩子）。
 *
 * Radix 的 `DialogPortal` 只读 `container` / `forceMount` / `children`，其余 props 直接丢弃，
 * 所以以前挂在它身上的 `data-slot="dialog-portal"` 从未落进 DOM（阶段 0 记录的缺陷）。
 * 现在在门户内部包一层 `display: contents` 的标记元素：它不生成布局盒（子元素照旧
 * 按 fixed 定位参与布局），但钩子真实存在，供样式与测试命中。
 */
function DialogPortal({
  children,
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Portal>) {
  return (
    <DialogPrimitive.Portal {...props}>
      <div data-slot="dialog-portal" className="contents">
        {children}
      </div>
    </DialogPrimitive.Portal>
  )
}

function DialogClose({
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Close>) {
  return <DialogPrimitive.Close data-slot="dialog-close" {...props} />
}

function DialogOverlay({
  className,
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Overlay>) {
  return (
    <DialogPrimitive.Overlay
      data-slot="dialog-overlay"
      className={cn(
        "fixed inset-0 z-[200] bg-black/50 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:animate-in data-[state=open]:fade-in-0",
        className
      )}
      {...props}
    />
  )
}

/**
 * 表单型 Dialog：点遮罩/外部不关闭（避免下拉展开时误关整个弹窗）。
 * 仅 取消 / 右上角 X / ESC 关闭。下拉打开期间的 pointer-events 修复见 index.css。
 */
function DialogContent({
  className,
  children,
  showCloseButton = true,
  closeLabel,
  onPointerDownOutside,
  onInteractOutside,
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Content> & {
  showCloseButton?: boolean
  /** 覆盖关闭按钮文案（默认取 i18n `common.close`，缺失时英文 Close）。 */
  closeLabel?: string
}) {
  const resolvedCloseLabel = useDialogCloseLabel(closeLabel)

  return (
    <DialogPortal>
      <DialogOverlay />
      <DialogPrimitive.Content
        data-slot="dialog-content"
        onPointerDownOutside={(e) => {
          e.preventDefault()
          onPointerDownOutside?.(e)
        }}
        onInteractOutside={(e) => {
          e.preventDefault()
          onInteractOutside?.(e)
        }}
        onFocusOutside={(e) => {
          e.preventDefault()
        }}
        className={cn(
          // FR-244 收敛：进/出场时长绑 motion token（normal≈180ms），机制仍走 tw-animate 的 data-[state] 淡入缩放，
          // 只把散落的硬编码时长换成 token 取值，避免脱离全局动画节奏。
          "fixed top-[50%] left-[50%] z-[200] grid w-full max-w-[calc(100%-2rem)] translate-x-[-50%] translate-y-[-50%] gap-4 rounded-lg border bg-background p-6 shadow-lg duration-[var(--motion-duration-normal)] outline-none data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95 data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95 sm:max-w-lg",
          className
        )}
        {...props}
      >
        {children}
        {showCloseButton && (
          <DialogClose
            className={cn(
              // 阶段 6 收敛：原来的 `focus:ring-*` 会在鼠标点击时也亮出焦点环，
              // 改为统一的 `:focus-visible` 细环（键盘才显示），避免"点一下框一下"的闪烁。
              focusRing,
              "absolute top-4 right-4 rounded-xs opacity-70 transition-opacity duration-[var(--motion-duration-fast)] ease-ios hover:opacity-100 disabled:pointer-events-none data-[state=open]:bg-accent data-[state=open]:text-muted-foreground [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4"
            )}
          >
            <XIcon />
            <span className="sr-only">{resolvedCloseLabel}</span>
          </DialogClose>
        )}
      </DialogPrimitive.Content>
    </DialogPortal>
  )
}

function DialogHeader({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="dialog-header"
      className={cn("flex flex-col gap-2 text-center sm:text-left", className)}
      {...props}
    />
  )
}

function DialogFooter({
  className,
  showCloseButton = false,
  closeLabel,
  children,
  ...props
}: React.ComponentProps<"div"> & {
  showCloseButton?: boolean
  /** 覆盖关闭按钮文案（默认取 i18n `common.close`，缺失时英文 Close）。 */
  closeLabel?: string
}) {
  const resolvedCloseLabel = useDialogCloseLabel(closeLabel)

  return (
    <div
      data-slot="dialog-footer"
      className={cn(
        "flex flex-col-reverse gap-2 sm:flex-row sm:justify-end",
        className
      )}
      {...props}
    >
      {children}
      {showCloseButton && (
        <DialogClose asChild>
          {/*
            钩子显式落在 Button 上：Radix Slot 合并 props 时子元素优先，
            Button 自带的 data-slot="button" 会把外层传入的 dialog-close 盖掉，
            结果页脚关闭按钮与右上角 X 的钩子不对称（阶段 0 记录的缺陷）。
          */}
          <Button variant="outline" data-slot="dialog-close">
            {resolvedCloseLabel}
          </Button>
        </DialogClose>
      )}
    </div>
  )
}

function DialogTitle({
  className,
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Title>) {
  return (
    <DialogPrimitive.Title
      data-slot="dialog-title"
      className={cn("text-lg leading-none font-semibold", className)}
      {...props}
    />
  )
}

function DialogDescription({
  className,
  ...props
}: React.ComponentProps<typeof DialogPrimitive.Description>) {
  return (
    <DialogPrimitive.Description
      data-slot="dialog-description"
      className={cn("text-sm text-muted-foreground", className)}
      {...props}
    />
  )
}

export {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogOverlay,
  DialogPortal,
  DialogTitle,
  DialogTrigger,
}
