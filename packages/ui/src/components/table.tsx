import * as React from "react"

import { cn } from "../lib/utils"
import { EmptyState } from "./empty-state"
import { Skeleton } from "./skeleton"

/** 列内容对齐方式。省略时不加对齐类，保证默认渲染的类集合与旧版一致（书写顺序可能不同，无渲染影响）。 */
type TableAlign = "left" | "right" | "center"

const ALIGN_CLASS: Record<TableAlign, string> = {
  left: "text-left",
  right: "text-right",
  center: "text-center",
}

/**
 * 表格外观开关：`classic`（默认，逐字沿用旧观感，其余 35 处表格零回归）|
 * `refined`（方案 A「精工卡片」：实底浅灰表头 + 2px 主色刻度线、发丝分隔线、
 * 整行提亮 + 首列主色指针、更宽松的单元格内距）。opt-in，经 context 下发给
 * TableHead/TableCell/TableRow，无需在消费方逐个改 props。
 */
type TableAppearance = "classic" | "refined"

const TableAppearanceContext = React.createContext<TableAppearance>("classic")

/**
 * 表头吸附开关的内部通道。
 *
 * Table 与 TableHeader 是两个独立组件，用 context 让 TableHeader 感知父级是否开启
 * stickyHeader，从而无需在 39 个消费方逐个改 TableHeader 的 props（默认 false，
 * 关闭时不追加任何内联类，类集合与旧版一致）。
 */
const TableStickyContext = React.createContext(false)

function Table({
  className,
  containerClassName,
  stickyHeader = false,
  appearance = "classic",
  ...props
}: React.ComponentProps<"table"> & {
  /** 表头是否吸附（配合 containerClassName 的 max-h + overflow 使用）。默认 false。 */
  stickyHeader?: boolean
  /** 外层滚动容器额外类名（如 `max-h-[70vh] overflow-y-auto`）。默认无。 */
  containerClassName?: string
  /** 外观：`classic`（默认，旧观感不变）| `refined`（方案 A 精工卡片）。 */
  appearance?: TableAppearance
}) {
  return (
    <div
      data-slot="table-container"
      className={cn("relative w-full overflow-x-auto", containerClassName)}
    >
      <TableStickyContext.Provider value={stickyHeader}>
        <TableAppearanceContext.Provider value={appearance}>
          <table
            data-slot="table"
            data-appearance={appearance}
            className={cn("w-full caption-bottom border-separate border-spacing-0 text-[13px]", className)}
            {...props}
          />
        </TableAppearanceContext.Provider>
      </TableStickyContext.Provider>
    </div>
  )
}

function TableHeader({ className, ...props }: React.ComponentProps<"thead">) {
  const sticky = React.useContext(TableStickyContext)
  const refined = React.useContext(TableAppearanceContext) === "refined"
  return (
    <thead
      data-slot="table-header"
      className={cn(
        refined
          ? [
              // refined：表头底色与底边线由每个 th 承担（实底 bg-muted + box-shadow 画刻度线与底边线），
              // thead 自身只负责吸附定位，避免 border-separate 下 tr 边框错位。
              "[&_tr]:border-0",
              sticky && "sticky top-0 z-10",
            ]
          : [
              "bg-muted/35 [&_tr]:border-b",
              // 吸附表头：因表格是 border-separate，sticky 时 tr 的 border 会随内容滚动而错位，
              // 改用 background 遮挡 + 每个 th 的 1px box-shadow 画底边线，滚动时线不飘。
              sticky && [
                "sticky top-0 z-10",
                "bg-card/95 backdrop-blur",
                "[&_tr]:border-b-0",
                "[&_th]:shadow-[0_1px_0_0_var(--border)]",
              ],
            ],
        className,
      )}
      {...props}
    />
  )
}

function TableBody({ className, ...props }: React.ComponentProps<"tbody">) {
  const refined = React.useContext(TableAppearanceContext) === "refined"
  return (
    <tbody
      data-slot="table-body"
      className={cn(
        "[&_tr:last-child]:border-0",
        // refined 的行分隔线画在 td 上（border-separate 下 tr 边框不渲染），末行需连带去掉 td 底边线。
        refined && "[&_tr:last-child>td]:border-b-0",
        className,
      )}
      {...props}
    />
  )
}

function TableFooter({ className, ...props }: React.ComponentProps<"tfoot">) {
  return (
    <tfoot
      data-slot="table-footer"
      className={cn(
        "border-t bg-muted/50 font-medium [&>tr]:last:border-b-0",
        className
      )}
      {...props}
    />
  )
}

function TableRow({ className, ...props }: React.ComponentProps<"tr">) {
  const refined = React.useContext(TableAppearanceContext) === "refined"
  return (
    <tr
      data-slot="table-row"
      className={cn(
        refined
          ? [
              "transition-colors duration-[var(--motion-duration-fast)] ease-ios",
              // 悬停整行提亮（取中性 muted，冷灰调）；首列左侧画 2px 主色指针——记忆点②。
              // data-flat 用于空态/骨架行 opt-out（:not 守卫），避免空行出现可点反馈与指针。
              "[&:not([data-flat]):hover]:bg-muted has-aria-expanded:bg-muted/60 data-[state=selected]:bg-accent/45",
              "[&:not([data-flat]):hover>td:first-child]:shadow-[inset_2px_0_0_0_var(--primary)]",
            ]
          : "border-b transition-colors hover:bg-accent/35 has-aria-expanded:bg-muted/50 data-[state=selected]:bg-accent/45",
        className,
      )}
      {...props}
    />
  )
}

function TableHead({
  className,
  align = "left",
  ...props
}: React.ComponentProps<"th"> & {
  /** 对齐方式，默认 left（与旧版 `text-left` 等价，不改变默认渲染）。 */
  align?: TableAlign
}) {
  const refined = React.useContext(TableAppearanceContext) === "refined"
  return (
    <th
      data-slot="table-head"
      className={cn(
        refined
          ? [
              // 记忆点①：inset 顶线 = 2px 主色刻度线；0 1px = 表头底边线。二者同处 box-shadow，
              // 吸附时随 th 移动不飘。实底 bg-muted（浅灰）遮挡滚动内容。
              "px-3.5 py-2.5 align-middle text-xs font-semibold whitespace-nowrap",
              "bg-muted text-[color:var(--subtle-fg)]",
              "shadow-[inset_0_2px_0_0_var(--primary),0_1px_0_0_var(--border)]",
              "[&:has([role=checkbox])]:pr-0 [&>[role=checkbox]]:translate-y-[2px]",
            ]
          : "h-9 px-2 align-middle text-[11px] font-semibold uppercase tracking-wide whitespace-nowrap text-muted-foreground [&:has([role=checkbox])]:pr-0 [&>[role=checkbox]]:translate-y-[2px]",
        ALIGN_CLASS[align],
        className,
      )}
      {...props}
    />
  )
}

function TableCell({
  className,
  align,
  ...props
}: React.ComponentProps<"td"> & {
  /** 对齐方式，省略时不加对齐类（保持旧版默认渲染）。 */
  align?: TableAlign
}) {
  const refined = React.useContext(TableAppearanceContext) === "refined"
  return (
    <td
      data-slot="table-cell"
      className={cn(
        refined
          ? [
              "px-3.5 py-3 align-middle whitespace-nowrap text-[color:var(--subtle-fg)]",
              // 发丝分隔线（--hairline，比 --border 更浅），末行由 TableBody 去除。
              "border-b border-[color:var(--hairline)]",
              "[&:has([role=checkbox])]:pr-0 [&>[role=checkbox]]:translate-y-[2px]",
            ]
          : "px-2 py-1.5 align-middle whitespace-nowrap [&:has([role=checkbox])]:pr-0 [&>[role=checkbox]]:translate-y-[2px]",
        align && ALIGN_CLASS[align],
        className,
      )}
      {...props}
    />
  )
}

function TableCaption({
  className,
  ...props
}: React.ComponentProps<"caption">) {
  return (
    <caption
      data-slot="table-caption"
      className={cn("mt-4 text-sm text-muted-foreground", className)}
      {...props}
    />
  )
}

/**
 * 表格空态行（opt-in）：铺满整行渲染 EmptyState，替代各页内联裸文字 `/ <p>`。
 * 悬停不变色（空行不该有可点行反馈）。
 */
function TableEmptyRow({
  colSpan,
  icon,
  title,
  description,
  action,
  className,
}: {
  /** 跨列数，需与表头列数一致。 */
  colSpan: number
  /** 语义图标（lucide 元素）。 */
  icon?: React.ReactNode
  /** 标题（已翻译）。 */
  title: string
  /** 说明文案（已翻译），可选。 */
  description?: string
  /** 操作引导（按钮/链接），可选。 */
  action?: React.ReactNode
  /** 内容额外类名。 */
  className?: string
}) {
  return (
    <TableRow data-flat className="hover:bg-transparent">
      {/* 覆盖 TableCell 基类的 whitespace-nowrap：空态说明文案较长时需正常换行，否则溢出被裁。 */}
      <TableCell colSpan={colSpan} className={cn("p-0 whitespace-normal", className)}>
        <EmptyState icon={icon} title={title} description={description} action={action} />
      </TableCell>
    </TableRow>
  )
}

/**
 * 表格骨架行（opt-in）：N 行 × M 列 Skeleton 脉冲占位，替代加载空白。
 * 每列首块更宽模拟主字段，其余收窄，观感更接近真实内容轮廓。
 */
function TableSkeletonRows({
  rows,
  cols,
  className,
}: {
  /** 占位行数。 */
  rows: number
  /** 占位列数（与表头列数一致）。 */
  cols: number
  /** 单元格额外类名。 */
  className?: string
}) {
  return (
    <>
      {Array.from({ length: rows }).map((_, r) => (
        <TableRow key={r} data-flat className="hover:bg-transparent">
          {Array.from({ length: cols }).map((__, c) => (
            <TableCell key={c} className={className}>
              <Skeleton className={cn("h-4 max-w-[8rem]", c === 0 ? "w-32" : "w-20")} />
            </TableCell>
          ))}
        </TableRow>
      ))}
    </>
  )
}

/**
 * 方案 A 卡片外壳（opt-in）：把表格装进一张有厚度的卡片。
 *
 * 为什么单独做原语而不复用 Panel：Panel 是"带模糊底的通用分区容器"（bg-card/95 + backdrop-blur），
 * 观感偏轻；本卡片要有实心白底 + 双层阴影（近距 1px + 远距 24px 负扩散）形成的"厚度感"，
 * 且 overflow-hidden 让圆角裁切表头/汇总条边缘。命名与现有 Table* 一致，四页统一 opt-in。
 */
function TableCard({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="table-card"
      className={cn(
        "overflow-hidden rounded-[10px] border border-border bg-card text-card-foreground",
        "shadow-[0_1px_2px_rgba(16,24,40,.05),0_8px_24px_-12px_rgba(16,24,40,.16)]",
        className,
      )}
      {...props}
    />
  )
}

/**
 * 卡片头：标题 + 计数（等宽数字）+ 右侧操作组（次/主按钮），可选副说明。
 * 背景用极浅的纵向渐变（白→近乎白）拉开与表体的层级；下缘一条发丝线收口。
 */
function TableCardHeader({
  title,
  count,
  description,
  actions,
  className,
  children,
  ...props
}: Omit<React.ComponentProps<"div">, "title"> & {
  /** 卡片标题（已翻译）。 */
  title: React.ReactNode
  /** 标题右侧计数/简述（等宽数字），可选。 */
  count?: React.ReactNode
  /** 标题下方的副说明（如页面功能说明），可选。 */
  description?: React.ReactNode
  /** 右侧操作组（次按钮 + 主按钮），可选。 */
  actions?: React.ReactNode
}) {
  return (
    <div
      data-slot="table-card-header"
      className={cn(
        "flex flex-col gap-1 border-b border-[color:var(--hairline)] px-4 py-3.5",
        className,
      )}
      // 极浅纵向渐变：inline style 避免 Tailwind 任意值里 color-mix/逗号转义歧义，且随主题变量自适应。
      style={{ backgroundImage: "linear-gradient(180deg, var(--card), color-mix(in srgb, var(--card) 97%, var(--muted)))" }}
      {...props}
    >
      <div className="flex flex-wrap items-center gap-2.5">
        <h2 className="text-sm font-semibold text-foreground">{title}</h2>
        {count != null && (
          <span className="font-mono text-xs text-muted-foreground tabular-nums">{count}</span>
        )}
        {children}
        {actions && <div className="ml-auto flex flex-wrap items-center gap-2">{actions}</div>}
      </div>
      {description != null && (
        <p className="max-w-3xl text-xs text-muted-foreground">{description}</p>
      )}
    </div>
  )
}

/**
 * 卡片脚（汇总条）：上缘发丝线 + 近白底，放一句有意义的汇总，解决表格"薄片悬空"。
 */
function TableCardFooter({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="table-card-footer"
      className={cn(
        "border-t border-[color:var(--hairline)] px-4 py-[11px] text-xs text-muted-foreground",
        className,
      )}
      style={{ backgroundColor: "color-mix(in srgb, var(--card) 97%, var(--muted))" }}
      {...props}
    />
  )
}

/** 操作列图标按钮色调：default（中性）/ primary（主操作，"测试"类）/ danger（删除类）。 */
type TableIconTone = "default" | "primary" | "danger"

const ICON_TONE_CLASS: Record<TableIconTone, string> = {
  default: "border-transparent hover:bg-muted hover:text-foreground hover:border-border",
  // 主操作常显弱底（primary-weak）+ 主墨色（primary-ink）+ 主色描边；hover 略加深仍保持同色系。
  primary:
    "border-[color:var(--primary-line)] bg-[color:var(--primary-weak)] text-[color:var(--primary-ink)] hover:bg-[color-mix(in_srgb,var(--primary)_20%,var(--card))]",
  // 危险操作常态中性，hover 才转红底红字红边（克制）。
  danger:
    "border-transparent hover:bg-[color:var(--danger-weak)] hover:text-[color:var(--status-danger)] hover:border-[color:var(--danger-line)]",
}

/**
 * 表格操作列的图标按钮（opt-in 原语）：28×28 命中区、15px 图标、聚焦 2px 主色环。
 *
 * 为什么独立而不改共享 Button：四页操作列需要"28px + 主操作弱底 + 危险 hover 转红"这套
 * 精细层级，而共享 Button 被其余 35 处表格复用，改它会有全局回归风险。此原语仅四页 opt-in，
 * 完整透传 aria-label/title/disabled/ref（含 Radix asChild 场景），不破坏按可达名查询的现有测试。
 */
function TableIconButton({
  tone = "default",
  className,
  ...props
}: React.ComponentProps<"button"> & {
  /** 色调：default / primary（主操作）/ danger（删除类）。 */
  tone?: TableIconTone
}) {
  return (
    <button
      type="button"
      data-slot="table-icon-button"
      data-tone={tone}
      className={cn(
        "inline-grid size-7 shrink-0 place-items-center rounded-md border bg-transparent text-muted-foreground",
        "transition-[background-color,color,border-color] duration-[var(--motion-duration-fast)] ease-ios",
        "outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2",
        "disabled:pointer-events-none disabled:opacity-50",
        "[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg]:size-[15px]",
        ICON_TONE_CLASS[tone],
        className,
      )}
      {...props}
    />
  )
}

export {
  Table,
  TableHeader,
  TableBody,
  TableFooter,
  TableHead,
  TableRow,
  TableCell,
  TableCaption,
  TableEmptyRow,
  TableSkeletonRows,
  TableCard,
  TableCardHeader,
  TableCardFooter,
  TableIconButton,
}
