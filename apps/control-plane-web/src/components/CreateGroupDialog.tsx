import { useCreateGroup } from '@/api/groups'
import { CreateGroupDialogView } from '@jianmanager/ui/components/views/groups/CreateGroupDialogView'

interface CreateGroupDialogProps {
  open: boolean
  onClose: () => void
}

/**
 * 新建用户组对话框的应用接线层（ADR-097 b 范式）。
 *
 * 对话框本体已迁入组件库并受控；本层只负责发请求，成功 toast 与列表失效由 `useCreateGroup`
 * 挂（与迁包前一致），失败原样抛回视图作内联错误。保留同路径默认导出与同一套 props，
 * 调用点无需改动。
 */
export default function CreateGroupDialog({ open, onClose }: CreateGroupDialogProps) {
  const create = useCreateGroup()

  return (
    <CreateGroupDialogView
      open={open}
      onClose={onClose}
      submitting={create.isPending}
      onSubmit={async ({ name, description }) => {
        await create.mutateAsync({ name, description })
      }}
    />
  )
}
