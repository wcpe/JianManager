import { useCreateRollingOp, useRollingControl, useRollingOp } from '@/api/instanceRolling'
import type { BatchSelectedInstance } from '@/components/InstanceBatchBar'
import RollingBatchDialogView, { RollingProgress } from '@/components/views/instances/RollingBatchDialog'

/**
 * 滚动/分批/灰度编排对话框的应用接线层（ADR-097 b 范式）。
 *
 * 对话框本体是受控视图（见 components/views）；本层接三样它不该自己拿的东西：创建动作、会话轮询、
 * 控制动作。进度视图用轮询到的会话数据渲染后以插槽交回视图。
 * 保留同路径的默认导出与同一套 props，调用点（InstanceBatchBar）无需改动。
 */
export default function RollingBatchDialog({
  selected,
  onClose,
}: {
  selected: BatchSelectedInstance[]
  onClose: () => void
}) {
  const create = useCreateRollingOp()

  return (
    <RollingBatchDialogView
      selected={selected}
      onClose={onClose}
      onCreate={(payload) => create.mutateAsync(payload)}
      progressSlot={(opId) => <RollingProgressContainer opId={opId} />}
    />
  )
}

/** 会话进度：轮询 + 控制动作的接线层，把数据喂给受控的 `RollingProgress`。 */
function RollingProgressContainer({ opId }: { opId: number }) {
  const { data: op } = useRollingOp(opId)
  const control = useRollingControl()
  return (
    <RollingProgress
      op={op}
      controlling={control.isPending}
      onControl={(action) => control.mutate({ opId, action })}
    />
  )
}
