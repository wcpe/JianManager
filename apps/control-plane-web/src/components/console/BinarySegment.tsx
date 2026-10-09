import { useInstance, useUpdateInstance } from '@/api/instances'
import BinarySegmentView from '@/components/views/instances/BinarySegment'
import { ProcessPanel } from './ProcessPanel'

/**
 * 进程能力分段的应用接线层（ADR-097 b 范式）。
 *
 * 分段本体是受控视图（见 components/views）；本层提供两样东西：进程指标块（`ProcessPanel` 自带取数，
 * 经 slot 注入）+ 启动参数的取数与保存。保留同路径的默认导出，调用点无需改动。
 */
export default function BinarySegment({ instanceId }: { instanceId: number }) {
  const { data: inst } = useInstance(instanceId)
  const update = useUpdateInstance()

  return (
    <BinarySegmentView
      processSlot={<ProcessPanel instanceId={instanceId} />}
      launch={{
        startCommand: inst?.startCommand ?? '',
        // 实例未加载时不给保存：避免把空草稿当作「用户改了」写回去。
        loaded: !!inst,
        saving: update.isPending,
        onSave: async (command) => {
          try {
            await update.mutateAsync({ id: instanceId, body: { startCommand: command } })
            return true
          } catch {
            return false
          }
        },
      }}
    />
  )
}
