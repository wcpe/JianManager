/** 
 * @file Card：通用卡片容器（标题 / 描述 / 操作 / 内容 / 底部五段）。
 * @input  lib/utils 的 cn、lib/interaction-overlay 的 cardSurface 与 shadowTransition
 * @output Card、CardHeader、CardTitle、CardDescription、CardAction、CardContent、CardFooter
 * @sync   改动基础样式时同步 panel.tsx / stat-card.tsx —— 三者共用 cardSurface 常量
 * @since（2026-10 恢复启用，原弃用决策撤销）
 */
/** 
 * 【为什么撤销的弃用】此前把 `Card` 标记为弃用、要求统一改用 `Panel`/`StatCard`，
 * 理由是「松散用法被统一卡片原语取代」。但实际收敛下来暴露出两个问题：
 *
 * 1. **`Panel` 不是 `Card` 的超集**。`Panel` 强制三段式结构（header/body/footer 各自带
 *    内边距与分隔线），当只需要一个「白盒 + 圆角 + 边框」的裸容器时，用 `Panel` 要额外
 *    覆盖 `bodyClassName` 才能去掉内边距，反而更绕。`Card` 才是那个最小容器。
 * 2. **弃用后没有替代路径，于是大家继续手写**。全站仍有 80 处手写
 *    `rounded-lg border bg-card`，因为「需要一个裸容器」这个需求本就存在。弃用一个组件
 *    并不会消灭需求，只会让需求散落成手写样式——那才是样式漂移的真正来源。
 *
 * 现在的分工是清晰的层次，而不是互斥的替代：
 *
 *     Card（最小白盒）
 *       ├─ Panel      = Card + 三段式结构 + 色调图标
 *       └─ StatCard   = Card + 数值/趋势布局
 *
 * 三者共用 `cardSurface` 常量（见 lib/interaction-overlay），因此基础视觉由一处决定。
 * 需要标题栏/底栏的分区用 `Panel`，需要 KPI 展示用 `StatCard`，只需要白盒就用 `Card`。
 */
import * as React from "react"

import { cn } from "../lib/utils"
import { cardSurface, shadowTransition, cardSurfaceTone, type CardSurfaceTone } from "../lib/interaction-overlay"

/** 
 * 卡片容器：圆角 + 描边 + 卡片底 + 柔和阴影，纵向堆叠子段。
 *
 * `interactive` 时叠加与 `Panel`/`StatCard` 同源的 hover 抬升（只换阴影不位移，
 *的既定行为），供可点击卡片使用。
 *
 * `surface` 用来表达「底色强弱」，取代此前散落全站的 `bg-card/30` ~ `bg-card/98`
 * 十余种任意透明度（见 `cardSurfaceTone` 注释）。
 */
function Card({
  className,
  interactive,
  surface = "solid",
  ...props
}: React.ComponentProps<"div"> & {
  /** 可点击卡片：启用 hover 抬升与焦点环。 */
  interactive?: boolean
  /** 
   * 底色强弱档位。收敛前全站有 12 种 `bg-card/NN` 任意值，语义无法区分；
   * 现按「承载关系」分三档（见 `cardSurfaceTone`）。
   */
  surface?: CardSurfaceTone
}) {
  return (
    <div
      data-slot="card"
      data-surface={surface}
      className={cn(
        /* 
          【为什么默认 gap-0、无内边距】Card 是**容器**，不是成品区块。
          初版默认 `gap-4 py-4` 的后果是：全站 68 处手写卡片几乎都没有这两个值，
          每一处替换都要手动写 `gap-0 px-4 py-4` 去抵消，等于把「统一」做成了「到处打补丁」，
          而且漏一处就凭空多出 16px 间距——这类失误在视觉上极难察觉。

          现在默认**不施加任何间距**，间距完全由调用方按场景决定：
          需要标准卡片时用 `<Card className="p-4">`，
          需要密集列表行时用 `<Card className="px-3 py-2">`，
          需要带子段的自定义留白时用 `<Card className="gap-2 p-4">`。
          容器只管「表面」（圆角/描边/底色/阴影），间距是内容的事。
        */
        "flex flex-col",
        cardSurface,
        cardSurfaceTone[surface],
        interactive &&
          cn(
            "text-left transition-colors hover:border-primary/40",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
            shadowTransition,
          ),
        className,
      )}
      {...props}
    />
  )
}

/** 卡头：标题与描述占一列，`CardAction` 占另一列（有 action 时自动两列）。 */
function CardHeader({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card-header"
      className={cn(
        "grid auto-rows-min grid-rows-[auto_auto] items-start gap-1.5 px-4",
        "has-data-[slot=card-action]:grid-cols-[1fr_auto]",
        className,
      )}
      {...props}
    />
  )
}

/** 卡标题。字号取设计标度（13px，与 `Panel` 的标题一致）。 */
function CardTitle({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card-title"
      className={cn("text-[13px] leading-none font-semibold text-foreground", className)}
      {...props}
    />
  )
}

/** 卡描述：次要说明文字。 */
function CardDescription({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card-description"
      className={cn("text-xs text-muted-foreground", className)}
      {...props}
    />
  )
}

/** 卡头右侧操作位（按钮/菜单等），自动落在第二列并跨两行。 */
function CardAction({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card-action"
      className={cn("col-start-2 row-span-2 row-start-1 self-start justify-self-end", className)}
      {...props}
    />
  )
}

/** 卡内容区。 */
function CardContent({ className, ...props }: React.ComponentProps<"div">) {
  return <div data-slot="card-content" className={cn("px-4", className)} {...props} />
}

/** 卡底部：横向排列，常放操作按钮或统计。 */
function CardFooter({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="card-footer"
      className={cn("flex items-center gap-2 px-4", className)}
      {...props}
    />
  )
}

export {
  Card,
  CardHeader,
  CardFooter,
  CardTitle,
  CardAction,
  CardDescription,
  CardContent,
}
