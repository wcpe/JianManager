import { useInstanceEvents } from '@/api/events'
import ConsoleSidebar from '@/components/console/ConsoleSidebar'
import ConsoleHeader from '@/components/console/ConsoleHeader'
import Workspace from '@/components/console/Workspace'

/**
 * 运维控制台 Shell（ADR-009 / FR-037 / FR-061 / FR-162）：
 * 左 = 常驻多级侧栏（全高，分组可展开，实例树/节点切换并入「实例」组）；
 * 右 = 全局顶栏（FR-162：标题/搜索/集群徽标/告警/账户）+ 其下工作区。
 * 登录后默认落地此处。
 */
export default function DashboardPage() {
  // 订阅实例状态变更 SSE，收到事件后自动失效缓存
  useInstanceEvents()

  return (
    <div className="flex h-screen">
      <ConsoleSidebar />
      <div className="flex min-w-0 flex-1 flex-col">
        <ConsoleHeader />
        <main className="min-h-0 flex-1">
          <Workspace />
        </main>
      </div>
    </div>
  )
}
