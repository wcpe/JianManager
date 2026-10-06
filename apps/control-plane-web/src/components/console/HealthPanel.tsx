import { useInstance } from '@/api/instances'
import { useServerState } from '@/api/serverState'
import { HealthPanel as HealthPanelView } from '@jianmanager/ui/components/views/instances/HealthPanel'

/**
 * 端口 + 健康检查面板的应用接线层（ADR-097 a 范式）。
 *
 * 面板本体已迁入组件库并受控（不取数）；本层取实例与探针连接态后注入。
 * 保留同名同路径的具名导出，使既有调用点无需改动。
 */
export function HealthPanel({ instanceId }: { instanceId: number }) {
  const { data: inst } = useInstance(instanceId)
  const { data: serverState } = useServerState(instanceId, true, 15_000)

  return (
    <HealthPanelView
      status={inst?.status}
      serverPort={inst?.serverPort}
      queryPort={inst?.queryPort}
      probePort={inst?.probePort}
      serverState={serverState}
    />
  )
}
