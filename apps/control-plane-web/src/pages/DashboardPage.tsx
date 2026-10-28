import { type CSSProperties } from 'react'
import { useIsFetching, useIsMutating } from '@tanstack/react-query'
import { useLocation } from 'react-router'
import { TopLoadingBar } from '@/components/views/common/TopLoadingBar'
import { useInstanceEvents } from '@/api/events'
import ConsoleHeader from '@/components/console/ConsoleHeader'
import { AssistantDock } from '@/components/views/assistant/AssistantDock'
import { useAssistantChat } from '@/components/views/assistant/useAssistantChat'
import { AssistantChatProvider } from '@/components/views/assistant/assistant-chat-context'
import { useConsoleStore } from '@/stores/console'
import WorkspaceSidebar from '@/components/console/WorkspaceSidebar'
import CommandPalette from '@/components/console/CommandPalette'
import MobileConsoleNav from '@/components/console/MobileConsoleNav'
import Workspace from '@/components/console/Workspace'

/**
 * 侧栏基准宽度（FR-502 阶段 6）：展开 246px / 折叠 54px，照原型 `.sidebar`。
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
 * 登录后默认落地此处。页名不在顶栏出现，由内容页的 `PageHeader` 承担（FR-502 阶段 6 补丁）。
 *
 * 【折叠动画只有一个时钟（FR-502 阶段 6 补丁）】
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
  // 外壳注入：TopLoadingBar 已受控（见 @jianmanager/ui 的 components/views/TopLoadingBar），不自行读路由与请求状态。
  // routeKey 必须含 location.key——否则同一路径上重复点击不产生变化、进度条不再重播。
  const location = useLocation()
  const pendingCount = useIsFetching() + useIsMutating()
  // 助手浮窗开关放在 console store（顶栏入口与浮窗分处两棵树，且浮窗需跨路由常驻）。
  const assistantOpen = useConsoleStore((s) => s.assistantDockOpen)
  const setAssistantOpen = useConsoleStore((s) => s.setAssistantDockOpen)
  /*
    会话状态的**唯一持有者**（FR-519 §3「外壳级单一 manager」）。

    必须在这里调、且只调一次：`useAssistantChat` 内部全是组件级 useState，
    每多调一次就多一份独立状态。此前首页与浮窗各自调用，导致
    「首页正在输出的内容，浮窗里看不到」以及两边可能各发一轮撞 409。
    首页在路由树、浮窗在 Shell 层，两者非父子，只能经 Context 下发。
  */
  const assistantChat = useAssistantChat(true)

  return (
    <div
      data-slot="console-shell"
      style={SIDEBAR_WIDTH_VARS}
      className="jm-console-shell flex h-screen w-screen flex-col overflow-hidden"
    >
      {/* 整个外壳共享同一份助手会话状态：首页（路由树内）与浮窗（Shell 层）
          都从这里取，只有一份。拆成两个 Provider 虽然 value 相同也能工作，
          但会让人误以为存在两份状态，故统一包在最外层。 */}
      <AssistantChatProvider value={assistantChat}>
      <TopLoadingBar
        routeKey={`${location.key}:${location.pathname}${location.search}`}
        pendingCount={pendingCount}
      />
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
      {/* 全局助手浮窗（FR-519）：挂在壳层而非页面里，因此跨路由不卸载、
          会话与 run 不受路由切换影响（关闭浮窗也不停止 run）。 */}
      <AssistantDock open={assistantOpen} onOpenChange={setAssistantOpen} />
      </AssistantChatProvider>
    </div>
  )
}
