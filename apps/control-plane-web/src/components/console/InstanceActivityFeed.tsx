import { toast } from 'sonner'

import InstanceActivityFeed from '@jianmanager/ui/components/views/console/InstanceActivityFeed'
import CrashDiagnostics from './CrashDiagnosticsCard'

/**
 * 动态与告警接线层（ADR-097）：注入提示通道与崩溃现场区块（后者自带快照/趋势取数）。
 * 消费方（InstanceConsolePage）零改动。
 */
export default function InstanceActivityFeedConnected(
  props: Omit<Parameters<typeof InstanceActivityFeed>[0], 'crashDiagnostics' | 'notify'> & {
    /** 崩溃诊断区块需要它来取快照与趋势（包内视图不需要，故不在此列）。 */
    instanceId: number
  },
) {
  const { instanceId, ...rest } = props
  return (
    <InstanceActivityFeed
      {...rest}
      crashDiagnostics={<CrashDiagnostics instanceId={instanceId} />}
      notify={(kind, message) => (kind === 'success' ? toast.success(message) : toast.error(message))}
    />
  )
}
