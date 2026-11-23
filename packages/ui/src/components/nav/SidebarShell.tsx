import * as React from 'react'

import { cn } from '../../lib/utils'
import { SideNav } from './SideNav'

/**
 * 侧栏外壳（原语层）：只承载**宽度、折叠态、过渡节拍**这三件与内容无关的事。
 *
 * 【为什么组合 `SideNav` 而不是自己渲染 `<aside>`】折叠态是靠 `SideNav` 内部的 context
 * 下发给子组件的——`SideNavRow` 的对称内边距、文字 `max-width` 收起、徽章 `ml-auto` 去留
 * 全都读它。本组件若另起一个 `<aside>`，就得再复制一份 context，两份真源迟早漂移；
 * 组合则天然共用同一份折叠态，行的折叠动画不必重写一行。
 *
 * 【为什么自己注入宽度变量】折叠动画依赖 `--sidebar-collapsed-width` /
 * `--sidebar-expanded-width`：`SideNavRow` 的居中 padding（`calc((var(--sidebar-collapsed-width,…)-29px)/2)`）
 * 与宿主 CSS 的宽度过渡都读它们。这两个变量此前只由主控台的 `.jm-console-shell` 提供，
 * 于是包内组件**只能在那个外壳里工作**，换个宿主就退回 fallback、图标直接贴左。
 * 改为由外壳自己注入后，组件在包外也是完整可用的。
 *
 * 【宽度由谁表达】本组件只注入变量，不写 `width`：真实宽度由 `SideNav` 的固定宽度类
 * （`w-[246px]` / `w-[54px]`）或宿主 CSS（主控台 `.jm-console-sidebar` 读这两个变量）
 * 承担。默认值取 54 / 246（原型实测值），与主控台现行值一致，因此接回主控台零跳变。
 */
export interface SidebarShellProps extends React.ComponentProps<'aside'> {
  /** 折叠为图标窄条。 */
  collapsed?: boolean
  /** 展开态宽度（px），写进 `--sidebar-expanded-width`。 */
  expandedWidth?: number
  /** 折叠态宽度（px），写进 `--sidebar-collapsed-width`。 */
  collapsedWidth?: number
  /** 与外壳自带样式合并（`cn` 语义：后者覆盖冲突的工具类）。 */
  className?: string
}

export function SidebarShell({
  collapsed = false,
  expandedWidth = 246,
  collapsedWidth = 54,
  className,
  style,
  children,
  ...props
}: SidebarShellProps) {
  return (
    <SideNav
      collapsed={collapsed}
      data-slot="sidebar-shell"
      // 折叠态同时用 `data-state` 表达：宿主的宽度过渡、以及「折叠时才生效」的样式
      // 都按这个钩子写（主控台 `.jm-console-sidebar[data-state='collapsed']`）。
      // 调用方仍可通过 props 覆盖（JSX 里显式属性优于前面的展开）。
      data-state={collapsed ? 'collapsed' : 'expanded'}
      style={{
        '--sidebar-expanded-width': `${expandedWidth}px`,
        '--sidebar-collapsed-width': `${collapsedWidth}px`,
        ...style,
      } as React.CSSProperties}
      className={cn(
        // 宽度过渡与宿主 CSS 同一条属性、同一节拍：时长读 `--sidebar-motion-duration`
        // （主控台由 `.jm-console-shell` 提供），包外退回 motion token 的 slow(320ms)。
        // 两者取值相同时不会叠出「两个时钟」，因此接回主控台不改变任何观感。
        'transition-[width] duration-[var(--sidebar-motion-duration,var(--motion-duration-slow))] ease-ios',
        className,
      )}
      {...props}
    >
      {children}
    </SideNav>
  )
}
