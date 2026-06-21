import { useInstanceEvents } from '@/api/events'
import ConsoleSidebar from '@/components/console/ConsoleSidebar'
import Workspace from '@/components/console/Workspace'

/**
 * 运维控制台 Shell（ADR-009 / FR-037 / FR-061）：
 * 左 = 常驻多级侧栏（分组可展开，实例树/节点切换并入「实例」组），右 = 工作区。
 * 登录后默认落地此处。
 */
export default function DashboardPage() {
  // 订阅实例状态变更 SSE，收到事件后自动失效缓存
  useInstanceEvents()

  return (
    <div className="flex h-screen">
      <ConsoleSidebar />
      <main className="min-w-0 flex-1">
        <Workspace />
      </main>
    </div>
  )
}
