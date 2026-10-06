import { toast } from 'sonner'
import { useCrashSnapshots, useCrashTrend } from '@/api/crashSnapshots'
import CrashDiagnosticsView from '@jianmanager/ui/components/views/instances/CrashDiagnosticsCard'

/**
 * 崩溃诊断的应用接线层（ADR-097 a 范式）。
 *
 * 视图本体已迁入组件库并受控；本层取快照与趋势（趋势只在确有崩溃时才查）、把提示交给 toast。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function CrashDiagnostics({ instanceId }: { instanceId: number }) {
  const { data: snapshots = [] } = useCrashSnapshots(instanceId)
  // 趋势只在确有崩溃时查询：无崩溃的实例不必多打一个接口。
  const { data: trend } = useCrashTrend(instanceId, 30, snapshots.length > 0)

  return (
    <CrashDiagnosticsView
      snapshots={snapshots}
      trend={trend}
      notify={(kind, message) => {
        if (kind === 'success') toast.success(message)
        else toast.error(message)
      }}
    />
  )
}
