import { toast } from 'sonner'

import UnifiedExplorerShell from '@/components/views/file-browser/UnifiedExplorerShell'
import { useThemeStore } from '@/stores/theme'
import ResourceExplorer from '@/components/explorer/ResourceExplorer'

/**
 * 统一文件浏览壳接线层（ADR-097）：注入主题，以及内层标签宿主需要的两项
 * （提示通道 + 标签内资源管理器的渲染函数）。消费方（StoragePage 等）零改动。
 */
export default function UnifiedExplorerShellConnected(
  props: Omit<Parameters<typeof UnifiedExplorerShell>[0], 'tabHost' | 'theme'>,
) {
  const resolvedTheme = useThemeStore((s) => s.resolvedTheme)
  return (
    <UnifiedExplorerShell
      {...props}
      theme={resolvedTheme === 'dark' ? 'dark' : 'light'}
      tabHost={{
        notify: (kind, message) => (kind === 'error' ? toast.error(message) : toast.success(message)),
        renderExplorer: (args) => <ResourceExplorer {...args} />,
      }}
    />
  )
}
