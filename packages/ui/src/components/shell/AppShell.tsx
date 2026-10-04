import * as React from 'react'

import { cn } from '../../lib/utils'

/**
 * 应用外壳（FR-496 阶段 4）：主控台的最外层骨架——顶栏 + 资源侧栏 + 页面区。
 *
 * 为什么单独抽这一层：原型把外壳做成固定三段（`#app{height:100dvh}` 里
 * 「53px 顶栏 / 撑满的主体 / 状态栏」），所有页面都嵌在主体里。此前每个页面
 * 各自处理外层高度与滚动，同一份骨架在不同页里会漂移。这里收敛成唯一出处：
 * 调用方只负责往 `topbar` / `sidebar` 两个槽位放内容，不再自己拼高度。
 *
 * 两条硬约束（都是原型实测值，不要顺手改）：
 *   1. 顶栏 53px 且 `shrink-0`——任何页面都不该挤动顶栏高度；
 *   2. 页面区自己滚（`overflow-auto`），整个外壳不滚。页面内已有滚动模型时
 *      （`PageShell` 三种壳态）内层会先吃饱高度、外层不产生第二条滚动条；
 *      外层这份是所有页面迁完之前的兜底——没有它，未迁移页面会直接**被裁掉**
 *      且无从滚动，比多一条滚动条难查得多。
 */
export function AppShell({
  topbar,
  sidebar,
  className,
  children,
  ...props
}: React.ComponentProps<'div'> & {
  /** 顶栏内容（通常传 `TopNav`）。 */
  topbar?: React.ReactNode
  /** 左侧资源导航（通常传 `SideNav`）。 */
  sidebar?: React.ReactNode
}) {
  return (
    <div
      data-slot="app-shell"
      className={cn(
        // h-dvh 而非 h-screen：移动端浏览器地址栏收起时 vh 不变，会留出一截空白。
        'flex h-dvh w-full flex-col overflow-hidden bg-background text-foreground',
        className,
      )}
      {...props}
    >
      {topbar && (
        <div data-slot="app-shell-topbar" className="flex h-[53px] min-w-0 shrink-0 items-stretch">
          {topbar}
        </div>
      )}
      <div data-slot="app-shell-body" className="flex min-h-0 min-w-0 flex-1">
        {sidebar}
        <main
          data-slot="app-shell-main"
          className="flex min-h-0 min-w-0 flex-1 flex-col overflow-auto"
        >
          {children}
        </main>
      </div>
    </div>
  )
}
