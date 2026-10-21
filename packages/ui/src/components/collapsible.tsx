"use client"
/**
 * @file Collapsible：折叠容器（radix-ui 原语包装；自上游 shadcn 基线拉取后适配本项目 token）。
 *       无内置样式（结构/行为原语）；带样式的一次性折叠段直接用本组件 + 自定义类。
 * @input  radix-ui 的 Collapsible
 * @output Collapsible、CollapsibleTrigger、CollapsibleContent
 * @sync   行为变更时同步 collapsible.test.tsx
 * @since  上游 shadcn 基线拉取适配（2026-10）
 */
import { Collapsible as CollapsiblePrimitive } from "radix-ui"

function Collapsible({ ...props }: React.ComponentProps<typeof CollapsiblePrimitive.Root>) {
  return <CollapsiblePrimitive.Root data-slot="collapsible" {...props} />
}

function CollapsibleTrigger({
  ...props
}: React.ComponentProps<typeof CollapsiblePrimitive.CollapsibleTrigger>) {
  return <CollapsiblePrimitive.CollapsibleTrigger data-slot="collapsible-trigger" {...props} />
}

function CollapsibleContent({
  ...props
}: React.ComponentProps<typeof CollapsiblePrimitive.CollapsibleContent>) {
  return <CollapsiblePrimitive.CollapsibleContent data-slot="collapsible-content" {...props} />
}

export { Collapsible, CollapsibleContent, CollapsibleTrigger }
