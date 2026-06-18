import FeatureNav from './FeatureNav'
import NodeSwitcher from './NodeSwitcher'
import InstanceTree from './InstanceTree'
import PlatformNav from './PlatformNav'

/**
 * 运维控制台左侧栏（ADR-009 / FR-037）：
 * 上 = 功能导航；中 = 节点切换 + 常驻实例树（可滚动占据剩余高度）；下 = 系统平台导航。
 */
export default function ConsoleSidebar() {
  return (
    <aside className="flex w-60 flex-col border-r">
      <div className="border-b p-4">
        <h2 className="text-lg font-bold">JianManager</h2>
      </div>

      {/* 上：功能导航 */}
      <FeatureNav />

      {/* 中：节点切换 + 实例树（占据剩余高度并可滚动） */}
      <div className="flex min-h-0 flex-1 flex-col border-t">
        <div className="p-2">
          <NodeSwitcher />
        </div>
        <div className="min-h-0 flex-1 overflow-auto px-1 pb-2">
          <InstanceTree />
        </div>
      </div>

      {/* 下：系统平台导航 + 主题/语言/退出/版本 */}
      <PlatformNav />
    </aside>
  )
}
