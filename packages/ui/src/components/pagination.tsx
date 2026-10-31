"use client"
/* eslint-disable react-refresh/only-export-components -- shadcn 组件随组件导出变体/常量，仅影响 Fast Refresh */
/**
 * @file Pagination：分页导航（自上游 shadcn 基线拉取后适配本项目 token 与 Button）。
 *       纯展示型导航：链接语义落在 `<a>` 上（经 Button asChild），当前页用 aria-current="page"。
 *       文案可经 text 覆盖（宿主接 i18n 时传入译文案）。
 * @input  lib/utils 的 cn、Button、lucide-react 图标
 * @output Pagination 及其子组件（Content / Item / Link / Previous / Next / Ellipsis）
 * @sync   行为/样式变更时同步 pagination.test.tsx
 * @since  上游 shadcn 基线拉取适配（2026-10）
 */
import * as React from "react"
import { ChevronLeftIcon, ChevronRightIcon, MoreHorizontalIcon } from "lucide-react"

import { cn } from "../lib/utils"
import { Button } from "./button"

function Pagination({ className, ...props }: React.ComponentProps<"nav">) {
  return (
    <nav
      role="navigation"
      aria-label="pagination"
      data-slot="pagination"
      className={cn("mx-auto flex w-full justify-center", className)}
      {...props}
    />
  )
}

function PaginationContent({ className, ...props }: React.ComponentProps<"ul">) {
  return (
    <ul
      data-slot="pagination-content"
      className={cn("flex items-center gap-1", className)}
      {...props}
    />
  )
}

function PaginationItem({ ...props }: React.ComponentProps<"li">) {
  return <li data-slot="pagination-item" {...props} />
}

type PaginationLinkProps = {
  isActive?: boolean
} & Pick<React.ComponentProps<typeof Button>, "size"> &
  React.ComponentProps<"a">

function PaginationLink({
  className,
  isActive,
  size = "icon",
  ...props
}: PaginationLinkProps) {
  return (
    <Button
      asChild
      variant={isActive ? "outline" : "ghost"}
      size={size}
      className={cn(className)}
    >
      <a
        aria-current={isActive ? "page" : undefined}
        data-slot="pagination-link"
        data-active={isActive}
        {...props}
      />
    </Button>
  )
}

function PaginationPrevious({
  className,
  text = "Previous",
  ...props
}: React.ComponentProps<typeof PaginationLink> & { text?: string }) {
  return (
    <PaginationLink
      aria-label="Go to previous page"
      size="default"
      className={cn("gap-1 pl-2.5", className)}
      {...props}
    >
      <ChevronLeftIcon className="size-4" />
      <span className="hidden sm:block">{text}</span>
    </PaginationLink>
  )
}

function PaginationNext({
  className,
  text = "Next",
  ...props
}: React.ComponentProps<typeof PaginationLink> & { text?: string }) {
  return (
    <PaginationLink
      aria-label="Go to next page"
      size="default"
      className={cn("gap-1 pr-2.5", className)}
      {...props}
    >
      <span className="hidden sm:block">{text}</span>
      <ChevronRightIcon className="size-4" />
    </PaginationLink>
  )
}

function PaginationEllipsis({ className, ...props }: React.ComponentProps<"span">) {
  return (
    <span
      aria-hidden
      data-slot="pagination-ellipsis"
      className={cn("flex size-9 items-center justify-center", className)}
      {...props}
    >
      <MoreHorizontalIcon className="size-4" />
      <span className="sr-only">More pages</span>
    </span>
  )
}

/**
 * 受控分页器：上一页 / 当前页-总页数 / 下一页。
 *
 * 【为什么要有这个而不只是上面的链接版】主控台**所有**分页都是受控状态
 * （`page` state + `onClick` 翻页），不是链接跳转——数据和页码在同一个 React 树里，
 * 用 `<a href>` 会导致整页刷新或路由跳转，语义完全不对。
 *
 * 初版 `Pagination` 是纯链接导航（`PaginationLink` 渲染 `<a>`），直接套到受控场景
 * 只能把 `<a>` 当按钮用（塞 onClick、去掉 href），既别扭又留下一个假链接。
 * 这里补一个**真正的受控版本**：按钮语义、键盘可达、禁用态由 Radix 之外的
 * 原生 `disabled` 表达，与站内其余 Button 一致。
 *
 * 与链接版的分工：
 * - 需要**可分享的页码 URL**（如日志深链 `?page=3`）→ 用链接版；
 * - 页码只是本地 UI 状态、翻页要走接口 → 用本组件。
 */
export function PaginationBar({
  page,
  totalPages,
  total,
  onChange,
  totalLabel,
  pageLabel,
  prevLabel = "上一页",
  nextLabel = "下一页",
  className,
}: {
  /** 当前页（1 基）。 */
  page: number
  /** 总页数。 */
  totalPages: number
  /** 总条数（可选，显示在左侧）。 */
  total?: number
  /** 翻页回调；接收目标页（已做 1..totalPages 夹取）。 */
  onChange: (page: number) => void
  /** 左侧总数文案（已格式化好）；省略则不显示。 */
  totalLabel?: React.ReactNode
  /** 中间页码文案（已格式化好，如「第 2 / 5 页」）；省略则用默认拼接。 */
  pageLabel?: React.ReactNode
  prevLabel?: React.ReactNode
  nextLabel?: React.ReactNode
  className?: string
}) {
  const clamp = (n: number) => Math.min(Math.max(1, n), Math.max(1, totalPages))
  const atFirst = page <= 1
  const atLast = page >= totalPages

  return (
    <div
      data-slot="pagination-bar"
      className={cn("flex items-center justify-between gap-2", className)}
    >
      {total !== undefined && (
        <span className="text-xs text-muted-foreground">
          {totalLabel ?? `共 ${total} 条`}
        </span>
      )}
      <div className="flex items-center gap-2">
        <Button
          type="button"
          size="xs"
          variant="outline"
          disabled={atFirst}
          onClick={() => onChange(clamp(page - 1))}
        >
          {prevLabel}
        </Button>
        <span className="text-xs text-muted-foreground">
          {pageLabel ?? `${page} / ${totalPages}`}
        </span>
        <Button
          type="button"
          size="xs"
          variant="outline"
          disabled={atLast}
          onClick={() => onChange(clamp(page + 1))}
        >
          {nextLabel}
        </Button>
      </div>
    </div>
  )
}

export {
  Pagination,
  PaginationContent,
  PaginationEllipsis,
  PaginationItem,
  PaginationLink,
  PaginationNext,
  PaginationPrevious,
}
