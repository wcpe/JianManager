import FeatureNav from './FeatureNav'
import NodeSwitcher from './NodeSwitcher'
import InstanceTree from './InstanceTree'
import PlatformNav from './PlatformNav'

/**
 * 运维控制台左侧栏（ADR-009 / FR-037）：上 = 功能导航；中 = 节点切换 + 常驻实例树；下 = 系统平台导航。
 *
 * 高度分配策略：中部实例树是主区域，始终保留可用高度（min-h 兜底）并独立滚动；
 * 上部功能导航被 max-h 限高且可滚动，不会因条目增多而挤占实例树；下部系统平台导航固定。
 */
export default function ConsoleSidebar() {
  return (
    <aside className="flex h-full w-60 flex-col border-r">
      <div className="shrink-0 border-b p-4">
        <h2 className="text-lg font-bold">JianManager</h2>
      </div>

      {/* 上：功能导航（限高 + 可滚动，让位给实例树） */}
      <div className="max-h-[38%] shrink overflow-y-auto">
        <FeatureNav />
      </div>

      {/* 中：节点切换 + 实例树（主区域，占据剩余高度并可滚动，保留最小可用高度） */}
      <div className="flex min-h-0 flex-1 flex-col border-t">
        <div className="shrink-0 p-2">
          <NodeSwitcher />
        </div>
        <div className="min-h-[160px] flex-1 overflow-y-auto px-1 pb-2">
          <InstanceTree />
        </div>
      </div>

      {/* 下：系统平台导航 + 主题/语言/退出/版本（固定不压缩） */}
      <div className="shrink-0">
        <PlatformNav />
      </div>
    </aside>
  )
}
