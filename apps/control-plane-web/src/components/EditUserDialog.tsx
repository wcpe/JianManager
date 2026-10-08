import { useUpdateUser, type UserInfo } from '@/api/users'
import { EditUserDialogView } from '@jianmanager/ui/components/views/EditUserDialogView'

interface EditUserDialogProps {
  /** 编辑目标用户（父组件须以 user.id 作 key 渲染，确保切换用户时表单重置）。 */
  user: UserInfo
  onClose: () => void
}

/**
 * 编辑用户对话框的应用接线层（ADR-097 b 范式）。
 *
 * 表单与校验（角色、可选重置密码）已迁入组件库并受控；本层只负责把视图上报的改动转成
 * `PUT /users/:id`（成功 toast 与列表失效由 `useUpdateUser` 挂），失败原样抛回视图作内联错误。
 * 保留同路径默认导出与同一套 props，调用点无需改动。
 */
export default function EditUserDialog({ user, onClose }: EditUserDialogProps) {
  const update = useUpdateUser()

  return (
    <EditUserDialogView
      user={user}
      onClose={onClose}
      submitting={update.isPending}
      onSubmit={async (values) => {
        await update.mutateAsync({ id: user.id, ...values })
      }}
    />
  )
}
