import type { ReactNode } from 'react'
import { cn } from '@jianmanager/ui'
import FileBrowser from '@/components/views/file-browser/FileBrowser'
import type { FileBrowserSource } from '@/lib/file-browser-types'
import {
  browserPropsFromCapability,
  type ExplorerCapability,
} from '@/lib/file-browser-capability'
import ExplorerTabHost from '@/components/views/explorer/ExplorerTabHost'

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
  /**
   * FR-422：塞进内层管理器已有横栏左端的控件（instance-files 模式 → ExplorerTabHost 标签条）。
   * 与 `header` 的区别：`header` 自成一条横栏，`leading` 蹭已有横栏、不增加高度。
   * browser / custom 模式没有可蹭的横栏，此时被忽略——宿主该用 `header`。
   */
  leading?: ReactNode
  className?: string
  refreshKey?: number
  /**
   * 透传给内层 ExplorerTabHost 的两项注入（instance-files 模式用；
   * 标签内的资源管理器需要一整套取数，只有外壳接线层持有）。
   * browser / custom 模式忽略。
   */
  tabHost: {
    notify: (kind: 'error', message: string) => void
    renderExplorer: Parameters<typeof ExplorerTabHost>[0]['renderExplorer']
  }
  /** 解析后的主题（外壳注入），browser 模式下透传给内层文件浏览器。 */
  theme: 'light' | 'dark'
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
  leading,
  className,
  refreshKey,
  tabHost,
  theme,
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
            leading={leading}
            notify={tabHost.notify}
            renderExplorer={tabHost.renderExplorer}
          />
        )}
        {capability.mode === 'browser' && source && (
          <FileBrowser
            source={source}
            {...browserPropsFromCapability(capability)}
            refreshKey={refreshKey}
            theme={theme}
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