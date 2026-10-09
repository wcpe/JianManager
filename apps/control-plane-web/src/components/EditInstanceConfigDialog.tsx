import { toast } from 'sonner'
import { useUpdateInstance } from '@/api/instances'
import { useNodeJDKs } from '@/api/jdks'
import type { ComboboxOption } from '@jianmanager/ui/components/combobox'
import EditInstanceConfigDialogView from '@/components/views/instances/EditInstanceConfigDialog'

/**
 * 实例配置编辑器的应用接线层（ADR-097 b 范式）。
 *
 * 对话框本体已迁入组件库并受控；本层取该节点已登记的 JDK 供绑定、接保存动作、把提示交给 toast。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function EditInstanceConfigDialog({
  instanceId,
  instanceName,
  nodeId,
  jdkId,
  startCommand,
  autoRestart,
  onClose,
}: {
  instanceId: number
  instanceName: string
  /** 实例所属节点 id，用于列出该节点已登记的 JDK 供绑定。 */
  nodeId: number
  /** 当前绑定的 JDK id（0=未绑定/系统默认）。 */
  jdkId: number
  startCommand: string
  autoRestart: boolean
  onClose: () => void
}) {
  const { data: jdks } = useNodeJDKs(nodeId)
  const update = useUpdateInstance()

  const jdkOptions: ComboboxOption[] = (jdks ?? []).map((j) => ({
    value: String(j.id),
    label: `${j.vendor} ${j.majorVersion} (${j.version})`,
  }))

  return (
    <EditInstanceConfigDialogView
      instanceName={instanceName}
      startCommand={startCommand}
      jdkId={jdkId}
      autoRestart={autoRestart}
      onClose={onClose}
      jdkOptions={jdkOptions}
      onSave={(payload) => update.mutateAsync({ id: instanceId, body: payload }).then(() => undefined)}
      notify={(kind, message) => {
        if (kind === 'success') toast.success(message)
        else toast.error(message)
      }}
    />
  )
}
