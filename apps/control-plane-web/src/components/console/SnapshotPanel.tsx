import { useCreateSnapshot, useDeleteSnapshot, useInstanceSnapshots, useRollbackSnapshot } from '@/api/snapshots'
import { useInstance } from '@/api/instances'
import { usePermissionsStore } from '@/stores/permissions'
import SnapshotPanelView from '@/components/views/instances/SnapshotPanel'

/**
 * 实例整机快照面板的应用接线层（ADR-097 b 范式）。
 *
 * 面板本体是受控视图（见 components/views）；本层取四样它不该自己拿的东西：快照列表（四态齐全）、
 * 实例运行态、两个权限位（`instance.write` 与 `instance.delete` 门槛不同）。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 *
 * 三个动作都不弹 toast——与迁移前一致：结果由列表刷新体现，破坏性操作本身已有二次确认。
 */
export default function SnapshotPanel({ instanceId }: { instanceId: number }) {
  // 四态齐全：loading / error / 空 / 有数据。**绝不能用 `= []` 默认值吞掉错误**——
  // 那会把「读失败」渲染成「暂无快照」，让运维以为数据没丢（与事实相反）。
  const { data: snapshots, isLoading, isError, refetch } = useInstanceSnapshots(instanceId)
  // 实例状态用于运行态提示（快照创建允许运行态，但产出可能世界文件不一致）。
  const { data: instance } = useInstance(instanceId)
  const createSnapshot = useCreateSnapshot(instanceId)
  const rollbackSnapshot = useRollbackSnapshot(instanceId)
  const deleteSnapshot = useDeleteSnapshot(instanceId)

  // 与 InstanceConsolePage 同范式的前端门禁（平台管理员 hasPerm 恒 true）。
  const canWrite = usePermissionsStore((s) => s.hasPerm('instance.write'))
  const canDelete = usePermissionsStore((s) => s.hasPerm('instance.delete'))
  const instanceLive = !!instance && ['STARTING', 'RUNNING', 'STOPPING'].includes(instance.status)

  return (
    <SnapshotPanelView
      snapshots={snapshots}
      isLoading={isLoading}
      isError={isError}
      instanceLive={instanceLive}
      canWrite={canWrite}
      canDelete={canDelete}
      creating={createSnapshot.isPending}
      rollingBack={rollbackSnapshot.isPending}
      deleting={deleteSnapshot.isPending}
      onRefresh={() => void refetch()}
      onCreate={async (name) => {
        try {
          await createSnapshot.mutateAsync(name)
          return true
        } catch {
          return false
        }
      }}
      onRollback={async (id) => {
        try {
          await rollbackSnapshot.mutateAsync(id)
          return true
        } catch {
          return false
        }
      }}
      onDelete={async (id) => {
        try {
          await deleteSnapshot.mutateAsync(id)
          return true
        } catch {
          return false
        }
      }}
    />
  )
}
