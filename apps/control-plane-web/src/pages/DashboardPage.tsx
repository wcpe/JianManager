import { type CSSProperties } from 'react'
import { useInstanceEvents } from '@/api/events'
import ConsoleHeader from '@/components/console/ConsoleHeader'
import WorkspaceSidebar from '@/components/console/WorkspaceSidebar'
import CommandPalette from '@/components/console/CommandPalette'
import MobileConsoleNav from '@/components/console/MobileConsoleNav'
import { TopLoadingBar } from '@/components/console/TopLoadingBar'
import Workspace from '@/components/console/Workspace'

/**
 * 侧栏基准宽度（FR-496 阶段 6）：展开 246px / 折叠 54px，照原型 `.sidebar`。
 *
 * 为什么要在外壳上写这两个变量：外壳 CSS 的原值是 15rem / 3.5rem（240 / 56），
 * 与 ui 包 `SideNav` 的 246 / 54 差 6px——两个真源不齐时，侧栏右缘会与品牌列、
 * 内容区错开几像素（折叠动画期间最明显）。变量挂在外壳而不是侧栏上，
 * 是因为顶栏品牌列读的是同一组变量，必须同源。
 */
const SIDEBAR_WIDTH_VARS = {
  '--sidebar-expanded-width': '246px',
  '--sidebar-collapsed-width': '54px',
} as CSSProperties

/**
 * 运维控制台 Shell（方案 C 品牌顶栏贯通，见 ADR-071；承接 ADR-009 / FR-037 / FR-061 / FR-162）：
 * 顶 = 横跨整宽的**唯一一条** 53px 全局顶栏（品牌区 + 工作区切换 + 搜索/集群徽标/任务/通知/账户）；
 * 其下为一行「侧栏 + 工作区」——侧栏下移到顶栏之下，品牌区宽度与侧栏同步，交界处合为一条竖线。
 * 登录后默认落地此处。页名不在顶栏出现，由内容页的 `PageHeader` 承担（FR-496 阶段 6 补丁）。
 *
 * 【折叠动画只有一个时钟（FR-496 阶段 6 补丁）】
 * 旧实现用 `sidebarLayout` / `sidebarMotion` 两个 state + 320ms `setTimeout` 造出「先锁宽、
 * 动画结束后落位」的两段式状态机，再配 `.jm-console-content` 的 `clip-path` + `translate3d`
 * 补偿。那套机制要求 JS 定时器与 CSS 过渡两个时钟严格同步：任何一帧掉帧或提前/滞后，
 * 内容区都会在落位瞬间跳一下（实测把过渡时长调慢即可复现）。补丁删掉状态机与内容区补偿，
 * 宽度过渡只由侧栏自身承担（见 `index.css` 的 `.jm-console-sidebar`），内容区作为同一 flex 行
 * 的兄弟节点平滑回流。外壳**不再承载折叠态**：原先的 `data-sidebar-target` 会迫使本组件
 * （页面树根）订阅 `sidebarCollapsed`，点一次 logo 就要 reconcile 全部约 920 个元素（实测 532ms）。
 * 现在折叠态只由侧栏自身的 `data-state` 表达，顶栏品牌列的宽度由 `index.css` 的 `:has()` 派生。
 */
export default function DashboardPage() {
  // 订阅实例状态变更 SSE，收到事件后自动失效缓存
  useInstanceEvents()

  return (
    <div
      data-slot="console-shell"
      style={SIDEBAR_WIDTH_VARS}
      className="jm-console-shell flex h-screen w-screen flex-col overflow-hidden"
    >
      <TopLoadingBar />
      <ConsoleHeader />
      <div data-slot="console-body" className="flex min-h-0 w-full flex-1">
        <WorkspaceSidebar />
        <div data-slot="console-content" className="jm-console-content relative flex min-w-0 w-full flex-1 flex-col">
          <main data-slot="console-main" className="jm-console-main jm-workspace-bg min-h-0 w-full flex-1 pb-16 sm:pb-0">
            <Workspace />
          </main>
        </div>
      </div>
      <MobileConsoleNav />
      {/* 全局命令面板（FR-241）：始终挂载以监听 Ctrl+K，打开时覆盖全屏。 */}
      <CommandPalette />
    </div>
  )
}
