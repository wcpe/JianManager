// 视图已迁至 @jianmanager/ui（ADR-097）；本层只做用户/邀请取数、五个写动作、写入口与删除门禁、路由链接接线。
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'
import {
  useUsers,
  useDeleteUser,
  useUpdateUser,
  useUserInvitations,
  useRevokeInvitation,
  type CreateInvitationResponse,
} from '@/api/users'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import { isPlatformAdmin as isAdminRole } from '@/lib/roles'
import { useDangerPermission } from '@/lib/danger'
import {
  UsersPageView,
  type CreateUserPayload,
} from '@jianmanager/ui/components/views/users/UsersPageView'

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * 用户管理页容器（ADR-097 b 范式）：用户/邀请取数、五个写动作、写入口门禁（FR-432 user.manage）
 * 与平台级删除门禁、toast 文案都在这里决定，列表、邀请面板与三个对话框交共享视图。
 *
 * 两处门禁的判定留在应用侧（包内不持鉴权状态）：`canManageUsers` 决定写入口是否可见，
 * `dangerAllowed` 决定删除用户是否放行（scope 固定 platform，不降级）。
 * 权限页跳转经 `renderPermissionsLink` 插槽注入（包内不依赖 react-router）。
 * 保留同路径默认导出，路由表无需改动。
 */
export default function UsersPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { data: users, isLoading } = useUsers()
  const { data: invitations } = useUserInvitations()
  const deleteUser = useDeleteUser()
  const updateUser = useUpdateUser()
  const revokeInvitation = useRevokeInvitation()

  // 新建用户与签发邀请没有现成 hook（原先的内联 mutation 在对话框接线层里），故在容器内联：
  // 成功失效对应缓存，失败原样抛回视图作内联错误（与迁包前一致，不额外弹错误 toast）。
  const createUser = useMutation({
    mutationFn: async (payload: CreateUserPayload) => {
      await api.post('/users', payload)
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['users'] })
    },
  })
  const createInvitation = useMutation({
    mutationFn: async (email: string) => {
      const { data } = await api.post<CreateInvitationResponse>('/users/invitations', { email, sendEmail: true })
      return data
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['users', 'invitations'] })
    },
  })

  const isPlatformAdmin = useAuthStore((s) => isAdminRole(s.role))
  // FR-432：用户写操作与 API 对齐（user.manage），不再仅看 role===10
  const hasPerm = usePermissionsStore((s) => s.hasPerm)
  const canManageUsers = isPlatformAdmin || hasPerm('user.manage')

  // 删除是平台级破坏操作：角色门禁在应用侧判定后注入（包内不持鉴权状态）。
  const { allowed: dangerAllowed } = useDangerPermission('platform')

  return (
    <UsersPageView
      users={users}
      isLoading={isLoading}
      invitations={invitations}
      canManageUsers={canManageUsers}
      updating={updateUser.isPending}
      revoking={revokeInvitation.isPending}
      dangerAllowed={dangerAllowed}
      notify={(kind, message) => {
        if (kind === 'success') toast.success(message)
        else toast.error(message)
      }}
      onCreateUser={async (payload) => {
        await createUser.mutateAsync(payload)
      }}
      onCreateInvitation={async (email) => {
        const data = await createInvitation.mutateAsync(email)
        return { invitationUrl: data.invitationUrl }
      }}
      onToggleStatus={(u) => {
        // 0=启用，toggle 即在 0/1 间切换。
        updateUser.mutate(
          { id: u.id, status: u.status === 0 ? 1 : 0 },
          {
            onError: (err: Error & { response?: { data?: { message?: string } } }) =>
              toast.error(errMessage(err, t('common.error'))),
          },
        )
      }}
      onUpdateUser={async (id, values) => {
        await updateUser.mutateAsync({ id, ...values })
      }}
      onDeleteUser={(id) => {
        deleteUser.mutate(id)
      }}
      onRevokeInvitation={(id) => {
        revokeInvitation.mutate(id)
      }}
      renderPermissionsLink={({ to, title, children }) => (
        <Link to={to} title={title}>
          {children}
        </Link>
      )}
    />
  )
}
