import type { ReactNode } from 'react'
import { cn } from '@jianmanager/ui'
import FileBrowser from './FileBrowser'
import type { FileBrowserSource } from './types'
import {
  browserPropsFromCapability,
  type ExplorerCapability,
} from './capability'
import ExplorerTabHost from '@/components/explorer/ExplorerTabHost'

export interface UnifiedExplorerShellProps {
  /** 场景能力描述（FR-378）。 */
  capability: ExplorerCapability
  /**
   * browser 模式必填：数据源。
   * instance-files 模式忽略，改用 instanceId。
   */
  source?: FileBrowserSource
  /** instance-files 模式必填。 */
  instanceId?: number
  /** 深链/初始目录（仅 instance-files）。 */
  initialDir?: string
  initialFile?: string
  /** custom 模式或额外插槽。 */
  children?: ReactNode
  /** 壳顶栏（可选）。 */
  header?: ReactNode
  className?: string
  refreshKey?: number
}

/**
 * 统一文件浏览壳（FR-378）：按 {@link ExplorerCapability} 选择
 * FileBrowser / ExplorerTabHost / 自定义 children。
 * **不删除**业务专用树（分发编排 FileExplorer 等仍走 custom）。
 */
export default function UnifiedExplorerShell({
  capability,
  source,
  instanceId,
  initialDir,
  initialFile,
  children,
  header,
  className,
  refreshKey,
}: UnifiedExplorerShellProps) {
  return (
    <div
      className={cn('flex h-full min-h-0 flex-col', className)}
      data-testid="unified-explorer-shell"
      data-cap={capability.id}
      data-mode={capability.mode}
    >
      {header}
      <div className="min-h-0 flex-1">
        {capability.mode === 'instance-files' && instanceId != null && (
          <ExplorerTabHost
            instanceId={instanceId}
            initialDir={initialDir}
            initialFile={initialFile}
          />
        )}
        {capability.mode === 'browser' && source && (
          <FileBrowser
            source={source}
            {...browserPropsFromCapability(capability)}
            refreshKey={refreshKey}
            className="h-full min-h-[320px]"
          />
        )}
        {capability.mode === 'custom' && children}
        {capability.mode === 'instance-files' && instanceId == null && (
          <p className="p-3 text-sm text-destructive">UnifiedExplorerShell: instanceId required</p>
        )}
        {capability.mode === 'browser' && !source && (
          <p className="p-3 text-sm text-destructive">UnifiedExplorerShell: source required</p>
        )}
      </div>
    </div>
  )
}
