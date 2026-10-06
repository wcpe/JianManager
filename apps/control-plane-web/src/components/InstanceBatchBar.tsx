import { toast } from 'sonner'
import { useInstanceBatch } from '@/api/instances'
import type { BatchSelectedInstance } from '@jianmanager/ui/components/views/instances/InstanceBatchBar'
import InstanceBatchBarView from '@jianmanager/ui/components/views/instances/InstanceBatchBar'
import RollingBatchDialog from './RollingBatchDialog'

// 原文件的导出名必须原样再导出，否则调用点（RollingBatchDialog 等）会断。
export type { BatchSelectedInstance } from '@jianmanager/ui/components/views/instances/InstanceBatchBar'

/**
 * 实例批量操作栏的应用接线层（ADR-097 b 范式）。
 *
 * 批量动作接到 `useInstanceBatch`，提示经 toast 展示，滚动编排对话框（尚未迁包）以插槽注入；
 * 视图本身不取数、不发请求、不弹 toast。保留同路径导出与同一套 props，调用点无需改动。
 */
export default function InstanceBatchBar({
  selected,
  onClear,
  onRetainFailed,
}: {
  selected: BatchSelectedInstance[]
  onClear: () => void
  onRetainFailed: (ids: number[]) => void
}) {
  const batch = useInstanceBatch()

  return (
    <InstanceBatchBarView
      selected={selected}
      onClear={onClear}
      onRetainFailed={onRetainFailed}
      onRun={(payload) => batch.mutateAsync(payload)}
      notify={(kind, message) => {
        if (kind === 'success') toast.success(message)
        else if (kind === 'warning') toast.warning(message)
        else toast.error(message)
      }}
      rollingDialog={(onClose) => <RollingBatchDialog selected={selected} onClose={onClose} />}
    />
  )
}
