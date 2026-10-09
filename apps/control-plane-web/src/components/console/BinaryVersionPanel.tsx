import { useBinaryRollback, useBinaryUpgrade, useBinaryVersion } from '@/api/binaryVersion'
import { useInstance } from '@/api/instances'
import { usePermissionsStore } from '@/stores/permissions'
import BinaryVersionPanelView from '@/components/views/instances/BinaryVersionPanel'

/**
 * 实例「二进制版本」区的应用接线层（ADR-097 b 范式）。
 *
 * 面板本体已迁入组件库并受控；本层取三样它不该自己拿的东西：版本视图、实例运行态
 * （升级/回滚要求已停止）、写权限（来自 permissions store，属应用状态）。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function BinaryVersionPanel({ instanceId }: { instanceId: number }) {
  // 四态齐全：loading / error / 有数据 /（无空态语义，unbound 是显式业务态）。
  const { data: view, isLoading, isError, refetch } = useBinaryVersion(instanceId)
  // 实例状态：升级/回滚要求已停止，前端据此禁用（后端 409 只是最终兜底）。
  const { data: instance } = useInstance(instanceId)
  const upgrade = useBinaryUpgrade(instanceId)
  const rollback = useBinaryRollback(instanceId)

  const canWrite = usePermissionsStore((s) => s.hasPerm('instance.write'))
  const instanceLive = !!instance && ['STARTING', 'RUNNING', 'STOPPING'].includes(instance.status)

  return (
    <BinaryVersionPanelView
      view={view}
      isLoading={isLoading}
      isError={isError}
      instanceLive={instanceLive}
      canWrite={canWrite}
      upgrading={upgrade.isPending}
      rollingBack={rollback.isPending}
      onRefresh={() => void refetch()}
      onUpgrade={async (assetId) => {
        try {
          await upgrade.mutateAsync(assetId)
          return true
        } catch {
          return false
        }
      }}
      onRollback={async () => {
        try {
          await rollback.mutateAsync()
          return true
        } catch {
          return false
        }
      }}
    />
  )
}
