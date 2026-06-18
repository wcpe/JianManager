import FeatureNav from './FeatureNav'
import NodeSwitcher from './NodeSwitcher'
import InstanceTree from './InstanceTree'
import PlatformNav from './PlatformNav'

/**
 * 运维控制台左侧栏（ADR-009 / FR-037）：上 = 功能导航；中 = 节点切换 + 常驻实例树；下 = 系统平台导航。
 *
 * 高度分配策略：整列为定高 flex column；中部实例树是主区域（flex-1 占据剩余高度并独立滚动），
 * 上部功能导航限高且可滚动、可收缩，下部系统平台导航固定。
 * 关键：每个 flex 后代都带 min-h-0，使可滚动子项能真正收缩并由自身 overflow-y-auto 裁剪滚动，
 * 短屏（如 1366×700 / 1280×640）下不会溢出压到下部导航。
 */
export default function ConsoleSidebar() {
  return (
    <aside className="flex h-full min-h-0 w-60 flex-col border-r">
      <div className="shrink-0 border-b p-4">
        <h2 className="text-lg font-bold">JianManager</h2>
      </div>

      {/* 上：功能导航（视口相对限高 + 可滚动，短屏下优先让位给实例树） */}
      <div className="max-h-[30vh] min-h-0 shrink overflow-y-auto">
        <FeatureNav />
      </div>

      {/* 中：节点切换 + 实例树（主区域，占据剩余高度并可滚动） */}
      <div className="flex min-h-0 flex-1 flex-col border-t">
        <div className="shrink-0 p-2">
          <NodeSwitcher />
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-1 pb-2">
          <InstanceTree />
        </div>
      </div>

      {/* 下：系统平台导航 + 主题/语言/退出/版本（固定不压缩，始终可见） */}
      <div className="shrink-0">
        <PlatformNav />
      </div>
    </aside>
  )
}
