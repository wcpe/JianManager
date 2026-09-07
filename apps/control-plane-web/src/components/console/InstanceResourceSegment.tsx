import { Activity } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@jianmanager/ui'

import InstanceEnvSegment from './InstanceEnvSegment'
import WorkspaceCardBody from './WorkspaceCardBody'

/** 文件配置页签内的分段（FR-413）。 */
export type ResourceSegment = 'files' | 'env'

// 分段标签用「文件」而非「文件配置」——后者是本页签自己的名字，同名会撞可访问性名称。
const SEGMENTS: Array<{ key: ResourceSegment; labelKey: string }> = [
  { key: 'files', labelKey: 'serverConsole.segFiles' },
  { key: 'env', labelKey: 'serverConsole.env' },
]

interface InstanceResourceSegmentProps {
  instanceId: number
  segment: ResourceSegment
  onSegmentChange: (segment: ResourceSegment) => void
}

/**
 * 文件配置页签（FR-413）：把原独立的「环境变量」页签并入本页签的一个分段——
 * 环境变量本质是工作目录下的 `.env`，独占一个顶级页签既挤 Tab 栏又割裂心智模型。
 *
 * 两个分段都保活（`<Activity>`）：环境变量编辑器持未保存草稿，分段来回切不得丢；
 * 文件管理器的目录展开态与选中集同理。与页签级 keep-alive（FR-295）同一手法。
 */
export default function InstanceResourceSegment({ instanceId, segment, onSegmentChange }: InstanceResourceSegmentProps) {
  const { t } = useTranslation()

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border bg-card shadow-soft">
      <div className="flex flex-none items-center gap-1 border-b px-2 py-1.5">
        <div className="inline-flex rounded-full bg-muted p-0.5">
          {SEGMENTS.map(({ key, labelKey }) => (
            <button
              key={key}
              type="button"
              onClick={() => onSegmentChange(key)}
              aria-pressed={segment === key}
              className={cn(
                'rounded-full px-3 py-1 text-xs transition-colors',
                segment === key ? 'bg-card font-semibold text-foreground shadow-soft' : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {t(labelKey)}
            </button>
          ))}
        </div>
      </div>

      {/* 文件段：管理器内部自己收口滚动，故此层 overflow-hidden。 */}
      <Activity mode={segment === 'files' ? 'visible' : 'hidden'}>
        <div className={cn('flex min-h-0 flex-col overflow-hidden', segment === 'files' ? 'flex-1' : 'hidden')}>
          <WorkspaceCardBody instanceId={instanceId} type="resource" persistTerminal />
        </div>
      </Activity>

      {/* 环境变量段：纵向堆叠两个面板，在此层滚。 */}
      <Activity mode={segment === 'env' ? 'visible' : 'hidden'}>
        <div className={cn('flex min-h-0 flex-col overflow-auto', segment === 'env' ? 'flex-1' : 'hidden')}>
          <InstanceEnvSegment instanceId={instanceId} />
        </div>
      </Activity>
    </div>
  )
}
