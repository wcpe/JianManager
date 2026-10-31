"use client"
/** 
 * @file Sheet：侧边抽屉（radix-ui Dialog 原语包装；2026-10 上游同步：取长补短合并替换）。
 * - 采纳上游增量：`SheetPortal` / `SheetOverlay` / `SheetFooter` 独立导出、四向 `side` 变体
 *   与对应滑入/滑出动画、`showCloseButton` 开关、关闭钮走 Button（ghost/icon-sm）；
 * - 保留本项目：token 样式与宽度（左/右默认 `max-w-md`）、关闭钮文案走 i18n（同 Dialog 口径，
 *   键 `common.close`，未接 i18n 时回落英文）。
 * @input  lib/utils 的 cn、radix-ui 的 Dialog（Sheet 底座）、Button、lucide-react、翻译上下文
 * @output Sheet 及全部子组件（Trigger/Close/Portal/Overlay/Content/Header/Footer/Title/Description）
 * @sync   行为/样式变更时同步 sheet.test.tsx；使用点：ConfigVersionDrawer 等
 * @since上游同步（2026-10）
 */
import * as React from "react"
import { Dialog as SheetPrimitive } from "radix-ui"
import { XIcon } from "lucide-react"
import { useTranslation } from "react-i18next"

import { cn } from "../lib/utils"
import { Button } from "./button"

/** 关闭文案的 i18n 键与英文兜底（与 Dialog 同口径，见 dialog.tsx 的说明）。 */
const SHEET_CLOSE_LABEL_KEY = "common.close"
const SHEET_CLOSE_LABEL_DEFAULT = "Close"

function Sheet(props: React.ComponentProps<typeof SheetPrimitive.Root>) {
  return <SheetPrimitive.Root data-slot="sheet" {...props} />
}

function SheetTrigger(props: React.ComponentProps<typeof SheetPrimitive.Trigger>) {
  return <SheetPrimitive.Trigger data-slot="sheet-trigger" {...props} />
}

function SheetClose(props: React.ComponentProps<typeof SheetPrimitive.Close>) {
  return <SheetPrimitive.Close data-slot="sheet-close" {...props} />
}

function SheetPortal(props: React.ComponentProps<typeof SheetPrimitive.Portal>) {
  return <SheetPrimitive.Portal data-slot="sheet-portal" {...props} />
}

function SheetOverlay({
  className,
  ...props
}: React.ComponentProps<typeof SheetPrimitive.Overlay>) {
  return (
    <SheetPrimitive.Overlay
      data-slot="sheet-overlay"
      className={cn(
        "fixed inset-0 z-50 bg-black/50 data-[state=open]:animate-in data-[state=open]:fade-in data-[state=closed]:animate-out data-[state=closed]:fade-out",
        className,
      )}
      {...props}
    />
  )
}

function SheetContent({
  className,
  children,
  side = "right",
  showCloseButton = true,
  ...props
}: React.ComponentProps<typeof SheetPrimitive.Content> & {
  side?: "top" | "right" | "bottom" | "left"
  showCloseButton?: boolean
}) {
  const { t } = useTranslation()
  return (
    <SheetPortal>
      <SheetOverlay />
      <SheetPrimitive.Content
        data-slot="sheet-content"
        data-side={side}
        className={cn(
          "fixed z-50 flex flex-col gap-2 bg-background p-4 shadow-lg outline-none",
          // 四向变体（宽度/贴边/边框方向）
          side === "right" && "inset-y-0 right-0 h-full w-full max-w-md border-l",
          side === "left" && "inset-y-0 left-0 h-full w-full max-w-md border-r",
          side === "top" && "inset-x-0 top-0 h-auto border-b",
          side === "bottom" && "inset-x-0 bottom-0 h-auto border-t",
          // 开合动画：淡入 + 对应方向的滑入/滑出
          "data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=closed]:animate-out data-[state=closed]:fade-out-0",
          side === "right" && "data-[state=open]:slide-in-from-right-10 data-[state=closed]:slide-out-to-right-10",
          side === "left" && "data-[state=open]:slide-in-from-left-10 data-[state=closed]:slide-out-to-left-10",
          side === "top" && "data-[state=open]:slide-in-from-top-10 data-[state=closed]:slide-out-to-top-10",
          side === "bottom" && "data-[state=open]:slide-in-from-bottom-10 data-[state=closed]:slide-out-to-bottom-10",
          className,
        )}
        {...props}
      >
        {children}
        {showCloseButton && (
          <SheetPrimitive.Close data-slot="sheet-close" asChild>
            <Button variant="ghost" size="icon-sm" className="absolute right-3 top-3">
              <XIcon className="size-4" />
              <span className="sr-only">{t(SHEET_CLOSE_LABEL_KEY, { defaultValue: SHEET_CLOSE_LABEL_DEFAULT })}</span>
            </Button>
          </SheetPrimitive.Close>
        )}
      </SheetPrimitive.Content>
    </SheetPortal>
  )
}

function SheetHeader({ className, ...props }: React.ComponentProps<"div">) {
  return <div data-slot="sheet-header" className={cn("flex flex-col gap-1", className)} {...props} />
}

function SheetFooter({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div data-slot="sheet-footer" className={cn("mt-auto flex flex-col gap-2", className)} {...props} />
  )
}

function SheetTitle({
  className,
  ...props
}: React.ComponentProps<typeof SheetPrimitive.Title>) {
  return (
    <SheetPrimitive.Title
      data-slot="sheet-title"
      className={cn("text-base font-semibold", className)}
      {...props}
    />
  )
}

function SheetDescription({
  className,
  ...props
}: React.ComponentProps<typeof SheetPrimitive.Description>) {
  return (
    <SheetPrimitive.Description
      data-slot="sheet-description"
      className={cn("text-sm text-muted-foreground", className)}
      {...props}
    />
  )
}

export {
  Sheet,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetOverlay,
  SheetPortal,
  SheetTitle,
  SheetTrigger,
}
