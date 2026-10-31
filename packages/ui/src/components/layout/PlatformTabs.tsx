import * as React from 'react'

import { cn } from '../../lib/utils'

/** 
 * 页签条（阶段 3）：「同一容器内的一组内容视图就地切换」。
 *
 * 与顶栏四工作区切换的区别：顶栏切的是**工作区**（导航层级，页面真的跳走了），
 * 本组件切的是**同一页内的若干视图**（内容层级，地址可能变也可能不变）。
 *
 * 【为什么改成 role=tablist】原实现用 `role=navigation` + `aria-current="page"`，
 * 语义是「导航到别处」；但实际使用场景（玩家列表 / 日志 / 对象详情）切的是**同一份
 * 数据的几个视图**，内容面板在同页内挂载卸载，用 `aria-current` 会让读屏把它们念成
 * 「当前页面」而不是「已选中的页签」。
 *
 * 现在按 WAI-ARIA Tabs 模式给完整语义：
 *   容器 `role="tablist"`，项 `role="tab"` + `aria-selected` + `aria-controls={panelId}`，
 *   面板侧由调用方给 `role="tabpanel"` + `aria-labelledby={tabId}`。
 * `id` 由调用方传入（`tabId` / `panelId`），因为 id 必须成对且全页唯一——
 * 组件无法替调用方猜出配对关系。
 *
 * 【键盘导航内置】WAI-ARIA Tabs 模式要求方向键在页签间移动、Home/End 跳首尾，
 * 且只有当前选中项可 Tab 聚焦（roving tabindex）。这三点由本组件统一处理：
 * 键盘事件挂在容器上（事件委托），通过 `onActiveChange` 通知调用方切换。
 * 调用方不必各自实现一遍——此前实例控制台手写了一份，玩家页则完全没有，
 * 同一个模式两处不一致正是「组件库存在但没人用」的后果。
 *
 * 与 `Segments` 的分工：`Segments` 是**视觉上更紧凑的凹槽分段**，用于工具栏里的
 * 密度切换；本组件是**下划线页签**，用于内容区顶部的视图分区（下面通常跟着分隔线）。
 * 两者都在切视图，官方分工见 `Segments.tsx` 的注释。
 */
export function PlatformTabs({
  className,
  children,
  onKeyDown,
  ...props
}: React.ComponentProps<'nav'>) {
  /** 
   * 方向键 / Home / End 处理：只在容器上挂一次，靠事件委托定位当前页签。
   *
   * 为什么用委托而不是给每个 `PlatformTab` 挂监听：页签数量可变（实例控制台按画像
   * 过滤后可能是 4 个也可能是 8 个），逐个挂监听要处理增删，而委托天然跟随 DOM。
   */
  const handleKeyDown = React.useCallback(
    (event: React.KeyboardEvent<HTMLElement>) => {
      onKeyDown?.(event)
      if (event.defaultPrevented) return

      const keys = ['ArrowRight', 'ArrowLeft', 'Home', 'End'] as const
      if (!keys.includes(event.key as (typeof keys)[number])) return

      const list = event.currentTarget
      const tabs = Array.from(
        list.querySelectorAll<HTMLButtonElement>('[role="tab"]:not([disabled])'),
      )
      if (tabs.length === 0) return

      const current = tabs.indexOf(document.activeElement as HTMLButtonElement)
      let next: number
      switch (event.key) {
        case 'ArrowRight':
          next = current < 0 ? 0 : (current + 1) % tabs.length
          break
        case 'ArrowLeft':
          next = current < 0 ? tabs.length - 1 : (current - 1 + tabs.length) % tabs.length
          break
        case 'Home':
          next = 0
          break
        default:
          next = tabs.length - 1
      }

      event.preventDefault()
      const target = tabs[next]
      target?.focus()
      // 移动焦点即激活（WAI-ARIA 的 automatic activation 模式），
      // 与该模式在实例控制台里的既有行为一致。
      target?.click()
    },
    [onKeyDown],
  )

  return (
    <nav
      // `<nav>` 保留 navigation landmark（这组页签确实是该区域的导航），
      // 显式 `role="tablist"` 覆盖其默认 landmark 语义——两者不能并存，
      // 而 tablist 才是这里真正要表达的关系（与已迁移的两处手写实现一致）。
      role="tablist"
      data-slot="platform-tabs"
      onKeyDown={handleKeyDown}
      className={cn(
        'flex shrink-0 gap-1 overflow-x-auto border-b px-0.5 scrollbar-none',
        className,
      )}
      {...props}
    >
      {children}
    </nav>
  )
}

/** 
 * 页签项。`active` 时以 2px 主色下划线标记（原型 `.platform-tabs button.active`）。
 *
 * 【未选中态为什么加极淡 hover 底】原先 hover 只改文字色（muted-foreground → foreground），
 * 在密集的页签条里几乎看不出指针落在哪一项——文字由灰转黑是很弱的信号，尤其深色主题下。
 * 现在叠加 `bg-muted/40` 的浅底，指向感明确，同时不干扰「选中 = 主色下划线」这套
 * 主次关系（下划线仍是唯一的选中标记，浅底只是指针反馈）。
 *
 * 【选中态为何反而不给 hover 底】选中项本身已有下划线 + 加粗 + 前景色三重标记，
 * 再叠一层 hover 浅底会让它在鼠标经过时「跳一下」，反而像未选中。故选中态 hover 清空底色。
 *
 * 圆角用 `rounded-t-[var(--radius-sm)]`：页签贴着底部分隔线，只需上方两角。
 * 取 `--radius-sm`（＝容器档 5px）而非写死像素，与全局圆角体系同源。
 */
export function PlatformTab({
  active = false,
  className,
  children,
  tabId,
  panelId,
  ...props
}: React.ComponentProps<'button'> & {
  /** 是否选中。 */
  active?: boolean
  /** 页签自身 id；面板用 `aria-labelledby` 指向它。省略则不输出（仅作纯展示时）。 */
  tabId?: string
  /** 受控面板的 id，写入 `aria-controls`。省略则不输出。 */
  panelId?: string
}) {
  return (
    <button
      type="button"
      role="tab"
      id={tabId}
      aria-selected={active}
      aria-controls={panelId}
      tabIndex={active ? 0 : -1}
      data-slot="platform-tab"
      data-active={active || undefined}
      className={cn(
        'shrink-0 whitespace-nowrap border-b-2 border-transparent px-2.5 pb-[11px] pt-2 text-xs text-muted-foreground',
        'rounded-t-[var(--radius-sm)]',
        'transition-[color,background-color,border-color] duration-[var(--motion-duration-normal)] ease-ios',
        'hover:bg-muted/40 hover:text-foreground',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40 focus-visible:ring-inset',
        active && 'border-primary font-medium text-foreground hover:bg-transparent',
        className,
      )}
      {...props}
    >
      {children}
    </button>
  )
}
