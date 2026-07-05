import { useEffect, useState } from 'react'
import { useInstanceEvents } from '@/api/events'
import ConsoleSidebar from '@/components/console/ConsoleSidebar'
import ConsoleHeader from '@/components/console/ConsoleHeader'
import CommandPalette from '@/components/console/CommandPalette'
import MobileConsoleNav from '@/components/console/MobileConsoleNav'
import { TopLoadingBar } from '@/components/console/TopLoadingBar'
import Workspace from '@/components/console/Workspace'
import { useConsoleStore } from '@/stores/console'

const SIDEBAR_ANIMATION_MS = 320
type SidebarLayoutState = 'expanded' | 'collapsed'
type SidebarMotionState = 'idle' | 'expanding' | 'collapsing'

/**
 * 运维控制台 Shell（ADR-009 / FR-037 / FR-061 / FR-162）：
 * 左 = 常驻多级侧栏（全高，分组可展开，实例树/节点切换并入「实例」组）；
 * 右 = 全局顶栏（FR-162：标题/搜索/集群徽标/告警/账户）+ 其下工作区。
 * 登录后默认落地此处。
 */
export default function DashboardPage() {
  // 订阅实例状态变更 SSE，收到事件后自动失效缓存
  useInstanceEvents()
  const sidebarCollapsed = useConsoleStore((s) => s.sidebarCollapsed)
  const sidebarState: SidebarLayoutState = sidebarCollapsed ? 'collapsed' : 'expanded'
  const [sidebarLayout, setSidebarLayout] = useState<SidebarLayoutState>(sidebarState)
  const sidebarMotion: SidebarMotionState =
    sidebarState === sidebarLayout ? 'idle' : sidebarState === 'collapsed' ? 'collapsing' : 'expanding'

  useEffect(() => {
    if (sidebarState === sidebarLayout) return

    const timer = window.setTimeout(() => setSidebarLayout(sidebarState), SIDEBAR_ANIMATION_MS)
    return () => window.clearTimeout(timer)
  }, [sidebarLayout, sidebarState])

  return (
    <div
      data-slot="console-shell"
      data-sidebar-target={sidebarState}
      data-sidebar-layout={sidebarLayout}
      data-sidebar-motion={sidebarMotion}
      className="jm-console-shell flex h-screen w-screen overflow-hidden"
    >
      <ConsoleSidebar />
      <div data-slot="console-content" className="jm-console-content relative flex min-w-0 w-full flex-1 flex-col">
        <TopLoadingBar />
        <ConsoleHeader />
        <main data-slot="console-main" className="jm-console-main jm-workspace-bg min-h-0 w-full flex-1 pb-16 sm:pb-0">
          <Workspace />
        </main>
      </div>
      <MobileConsoleNav />
      {/* 全局命令面板（FR-241）：始终挂载以监听 Ctrl+K，打开时覆盖全屏。 */}
      <CommandPalette />
    </div>
  )
}
