import { toast } from 'sonner'
import {
  useInstanceGroups,
  useCreateInstanceGroup,
  useUpdateInstanceGroup,
  useDeleteInstanceGroup,
  useAddInstanceGroupMembers,
} from '@/api/instanceGroups'
import { useConsoleStore } from '@/stores/console'
import { InstanceGroupTree as InstanceGroupTreeView } from '@jianmanager/ui/components/views/instances/InstanceGroupTree'

// 原文件的导出名必须原样再导出，否则调用点（InstanceGroupManager 等）会断。
export { INSTANCE_DND_MIME } from '@jianmanager/ui/components/views/instances/InstanceGroupTree'

/**
 * 分组树的应用接线层（ADR-097 b 范式）。
 *
 * 树本体已迁入组件库并受控；本层取分组列表、接 console store 的折叠态与四个写动作、
 * 把提示交给 toast。保留同路径的导出与同一套 props，调用点无需改动。
 */
export function InstanceGroupTree({
  selectedGroupId,
  onSelect,
}: {
  /** 当前选中组 id；null=未选（右列表显示「全部/未选」）。 */
  selectedGroupId: number | null
  onSelect: (groupId: number | null) => void
}) {
  const { data: groups, isLoading } = useInstanceGroups()
  const collapsedGroups = useConsoleStore((s) => s.collapsedGroups)
  const toggleGroup = useConsoleStore((s) => s.toggleGroup)

  const create = useCreateInstanceGroup()
  const update = useUpdateInstanceGroup()
  const del = useDeleteInstanceGroup()
  const addMembers = useAddInstanceGroupMembers()

  return (
    <InstanceGroupTreeView
      selectedGroupId={selectedGroupId}
      onSelect={onSelect}
      groups={groups ?? []}
      loading={isLoading}
      collapsedGroups={collapsedGroups}
      onToggleCollapsed={toggleGroup}
      onCreate={(payload) => create.mutateAsync(payload).then(() => undefined)}
      onRename={(payload) => update.mutateAsync(payload).then(() => undefined)}
      onDelete={(id) => del.mutateAsync(id).then(() => undefined)}
      onDropInstances={(payload) =>
        addMembers.mutateAsync({ id: payload.groupId, instanceIds: payload.instanceIds })
      }
      notify={(kind, message) => {
        if (kind === 'success') toast.success(message)
        else toast.error(message)
      }}
    />
  )
}
