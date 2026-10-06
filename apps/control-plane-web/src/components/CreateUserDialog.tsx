import { toast } from 'sonner'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'
import CreateUserDialogView from '@jianmanager/ui/components/views/CreateUserDialog'

/**
 * 新建用户对话框的应用接线层（ADR-097 b 范式）。
 *
 * 对话框本体已迁入组件库并受控；本层发创建请求、失效用户列表、把提示交给 toast。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function CreateUserDialog({
  open,
  onClose,
}: {
  open: boolean
  onClose: () => void
}) {
  const qc = useQueryClient()
  const create = useMutation({
    mutationFn: async (body: { username: string; password: string; role: number; status: number }) => {
      await api.post('/users', body)
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['users'] })
    },
  })

  return (
    <CreateUserDialogView
      open={open}
      onClose={onClose}
      onCreate={(payload) => create.mutateAsync(payload).then(() => undefined)}
      notify={(kind, message) => {
        if (kind === 'success') toast.success(message)
        else toast.error(message)
      }}
    />
  )
}
