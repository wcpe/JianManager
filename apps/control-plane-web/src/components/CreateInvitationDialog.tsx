import { useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'
import type { CreateInvitationResponse } from '@/api/users'
import CreateInvitationDialogView from '@jianmanager/ui/components/views/CreateInvitationDialog'

/**
 * 签发邀请对话框的应用接线层（ADR-097 b 范式）。
 *
 * 对话框本体已迁入组件库并受控；本层发签发请求并失效邀请列表。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function CreateInvitationDialog({
  open,
  onClose,
}: {
  open: boolean
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const create = useMutation({
    mutationFn: async (email: string) => {
      const { data } = await api.post<CreateInvitationResponse>('/users/invitations', { email, sendEmail: true })
      return data
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['users', 'invitations'] })
    },
  })

  return (
    <CreateInvitationDialogView
      open={open}
      onClose={onClose}
      onCreate={(email) => create.mutateAsync(email).then((d) => ({ invitationUrl: d.invitationUrl }))}
    />
  )
}
