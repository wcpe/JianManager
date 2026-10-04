import * as React from 'react'

import { cn } from '../../lib/utils'

/**
 * 工作区页签（FR-496 阶段 3）：用于「平台管理」这类把 14 个页面分入 5 个分类的工作区。
 *
 * 与顶栏的四工作区切换区别：顶栏切的是**工作区**（导航层级），页签切的是
 * **同一工作区内的一组页面**（内容层级）。原型要求进入某分类后，内容区上方只显示
 * 该分类的几个页面入口——本组件即承担后者。
 *
 * 数量少时用页签（横向排列）；分类内页面多时改由调用方换成 `SettingsLayout` 的左栏。
 */
export function PlatformTabs({ className, children, ...props }: React.ComponentProps<'nav'>) {
  return (
    <nav
      data-slot="platform-tabs"
      className={cn(
        'flex shrink-0 gap-[19px] overflow-x-auto border-b px-0.5 scrollbar-none',
        className,
      )}
      {...props}
    >
      {children}
    </nav>
  )
}

/** 页签项。`active` 时以 2px 主色下划线标记（原型 `.platform-tabs button.active`）。 */
export function PlatformTab({
  active = false,
  className,
  children,
  ...props
}: React.ComponentProps<'button'> & { active?: boolean }) {
  return (
    <button
      type="button"
      data-slot="platform-tab"
      data-active={active || undefined}
      aria-current={active ? 'page' : undefined}
      className={cn(
        'shrink-0 whitespace-nowrap border-b-2 border-transparent pb-[11px] pt-2 text-xs text-muted-foreground',
        'transition-colors duration-[var(--motion-duration-normal)] ease-ios hover:text-foreground',
        active && 'border-primary font-medium text-foreground',
        className,
      )}
      {...props}
    >
      {children}
    </button>
  )
}
