import { useUpdateGroup, useUpdateGroupQuota, type GroupInfo } from '@/api/groups'
import { GroupEditDialogView } from '@jianmanager/ui/components/views/groups/GroupEditDialogView'

interface GroupEditDialogProps {
  /** 编辑目标组（父组件须以 group.id 作 key 渲染以重置表单）。 */
  group: GroupInfo
  onClose: () => void
}

/**
 * 编辑用户组对话框的应用接线层（ADR-097 b 范式）。
 *
 * 对话框本体已迁入组件库并受控；本层只负责发请求——按原有顺序先更新名称/描述、
 * 再更新配额，任一失败原样抛回视图作内联错误（成功提示与缓存失效由 mutation hook 挂）。
 * 保留同路径默认导出与同一套 props，调用点无需改动。
 */
export default function GroupEditDialog({ group, onClose }: GroupEditDialogProps) {
  const update = useUpdateGroup()
  const updateQuota = useUpdateGroupQuota()

  return (
    <GroupEditDialogView
      group={group}
      onClose={onClose}
      submitting={update.isPending || updateQuota.isPending}
      onSubmit={async ({ name, description, quota }) => {
        await update.mutateAsync({ id: group.id, name, description })
        await updateQuota.mutateAsync({ id: group.id, ...quota })
      }}
    />
  )
}
