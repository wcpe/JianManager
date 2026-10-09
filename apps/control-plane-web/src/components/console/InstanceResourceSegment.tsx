// 外壳已迁至 @jianmanager/ui（ADR-097）；本层只把三段内容接线层注入插槽。
import InstanceResourceSegmentView from '@/components/views/console/InstanceResourceSegmentView'
import type { ResourceSegment } from '@jianmanager/ui/lib/instance-console-tabs'

import InstanceConfigSurfacePanel from './InstanceConfigSurfacePanel'
import InstanceEnvSegment from './InstanceEnvSegment'
import WorkspaceCardBody from './WorkspaceCardBody'

/** 文件配置页签内的分段（FR-413；FR-451 增「关键配置」段）。类型定义已归包（与控制台页签解析同源）。 */
export type { ResourceSegment } from '@jianmanager/ui/lib/instance-console-tabs'

interface InstanceResourceSegmentProps {
  instanceId: number
  segment: ResourceSegment
  onSegmentChange: (segment: ResourceSegment) => void
}

/**
 * 文件配置页签接线层（ADR-097 b 范式）：分段外壳（分段控件 + 三段 `<Activity>` 保活）已迁入组件库，
 * 这里是三段内容的取数接线层注入点——「关键配置」面板、文件管理器、环境变量编辑器本体
 * 都是自取数的视图接线层，故由本层注入而不是在包内取。
 *
 * 分段值与切换仍由调用方（控制台页）持有：它与深链解析结果同源，故容器不接管该状态。
 * 保留同路径默认导出与同名 props（含 `ResourceSegment` 再导出），调用点无需改动。
 */
export default function InstanceResourceSegment({ instanceId, segment, onSegmentChange }: InstanceResourceSegmentProps) {
  return (
    <InstanceResourceSegmentView
      segment={segment}
      onSegmentChange={onSegmentChange}
      configContent={<InstanceConfigSurfacePanel instanceId={instanceId} />}
      filesContent={<WorkspaceCardBody instanceId={instanceId} type="resource" persistTerminal />}
      envContent={<InstanceEnvSegment instanceId={instanceId} />}
    />
  )
}
