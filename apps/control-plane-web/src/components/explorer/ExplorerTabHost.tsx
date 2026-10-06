import { toast } from 'sonner'

import ExplorerTabHost from '@jianmanager/ui/components/views/explorer/ExplorerTabHost'
import ResourceExplorer from './ResourceExplorer'

/**
 * 资源管理器多标签宿主接线层（ADR-097）：提示通道在这里注入；
 * 每个标签内的资源管理器由本层渲染自己的接线层实例（`ResourceExplorer` 会补齐它的全部取数注入）。
 */
export default function ExplorerTabHostConnected(
  props: Omit<Parameters<typeof ExplorerTabHost>[0], 'notify' | 'renderExplorer'>,
) {
  return (
    <ExplorerTabHost
      {...props}
      notify={(kind, message) => (kind === 'error' ? toast.error(message) : toast.success(message))}
      renderExplorer={(args) => <ResourceExplorer {...args} />}
    />
  )
}
