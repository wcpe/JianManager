import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { UserRound } from 'lucide-react'
import { useUsers, useDeleteUser, useUpdateUser, useUserInvitations, useRevokeInvitation, type UserInfo } from '@/api/users'
import { useAuthStore } from '@/stores/auth'
import DangerConfirm from '@/components/DangerConfirm'
import CreateUserDialog from '@/components/CreateUserDialog'
import CreateInvitationDialog from '@/components/CreateInvitationDialog'
import EditUserDialog from '@/components/EditUserDialog'
import { Button } from '@jianmanager/ui/components/button'
import { Panel } from '@jianmanager/ui/components/panel'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import {
  Table,
  TableBody,
  TableCard,
  TableCardFooter,
  TableCardHeader,
  TableCell,
  TableEmptyRow,
  TableHead,
  TableHeader,
  TableRow,
  TableSkeletonRows,
} from '@jianmanager/ui/components/table'
import {
  ConfigRow,
  ConfigSwitch,
  ConfigViewToggle,
  type ConfigView,
} from '@/pages/config-row'

export default function UsersPage() {
  const { t } = useTranslation()
  const [showCreate, setShowCreate] = useState(false)
  const [showInvite, setShowInvite] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<{ id: number; username: string } | null>(null)
  const [editUser, setEditUser] = useState<UserInfo | null>(null)
  const [view, setView] = useState<ConfigView>('list')
  const { data: users, isLoading } = useUsers()
  const { data: invitations } = useUserInvitations()
  const deleteUser = useDeleteUser()
  const updateUser = useUpdateUser()
  const revokeInvitation = useRevokeInvitation()
  const isPlatformAdmin = useAuthStore((s) => s.role === 10)

  const roleLabel = (role: number): string => {
    switch (role) {
      case 0:
        return t('users.member')
      case 1:
        return t('users.groupAdmin')
      case 10:
        return t('users.platformAdmin')
      default:
        return t('users.roleUnknown', { role })
    }
  }

  // 平台管理员主色 pill，其余中性，作角色 pill 着色。
  const roleTone = (role: number) => (role === 10 ? 'info' : 'neutral')
  // 用户状态 0=启用。toggle 即在 0/1 间切换。
  const toggleStatus = (u: UserInfo) =>
    updateUser.mutate(
      { id: u.id, status: u.status === 0 ? 1 : 0 },
      {
        onError: (err: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(err.response?.data?.message || t('common.error')),
      },
    )

  const totalUsers = users?.length ?? 0
  const enabledUsers = (users ?? []).filter((u) => u.status === 0).length

  return (
    <div data-page="users" className="jm-page-stack space-y-4">
      {/* 方案 A「精工卡片」：标题/计数/主操作收进卡片头，表格套 refined 外观，底部一句汇总。 */}
      <TableCard>
        <TableCardHeader
          title={t('users.title')}
          count={t('users.cardCount', { total: totalUsers })}
          actions={
            <>
              <ConfigViewToggle view={view} onChange={setView} cardLabel={t('common.cardView')} listLabel={t('common.listView')} />
              {isPlatformAdmin && <Button variant="outline" onClick={() => setShowInvite(true)}>{t('users.inviteUser')}</Button>}
              {isPlatformAdmin && <Button onClick={() => setShowCreate(true)}>+ {t('users.createUser')}</Button>}
            </>
          }
        />

        {isLoading ? (
          <Table appearance="refined">
            <TableHeader>
              <TableRow>
                <TableHead>{t('users.username')}</TableHead>
                <TableHead>{t('users.role')}</TableHead>
                <TableHead>{t('users.status')}</TableHead>
                <TableHead align="right">{t('users.createdAt')}</TableHead>
                <TableHead align="right">{t('users.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableSkeletonRows rows={4} cols={5} />
            </TableBody>
          </Table>
        ) : !users || users.length === 0 ? (
          <Table appearance="refined">
            <TableHeader>
              <TableRow>
                <TableHead>{t('users.username')}</TableHead>
                <TableHead>{t('users.role')}</TableHead>
                <TableHead>{t('users.status')}</TableHead>
                <TableHead align="right">{t('users.createdAt')}</TableHead>
                <TableHead align="right">{t('users.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableEmptyRow colSpan={5} icon={<UserRound />} title={t('users.empty')} description={t('users.emptyHint')} />
            </TableBody>
          </Table>
        ) : view === 'card' ? (
          <div className="flex flex-col gap-2.5 p-3">
            {users.map((u) => (
              <ConfigRow
                key={u.id}
                icon={<UserRound className="size-[18px]" />}
                tone={u.status === 0 ? 'primary' : 'neutral'}
                title={u.username}
                subtitle={`${roleLabel(u.role)} · ${new Date(u.createdAt).toLocaleDateString()}`}
                trailing={
                  <>
                    <StatusBadge level={roleTone(u.role)} label={roleLabel(u.role)} dot={false} />
                    <StatusBadge
                      level={u.status === 0 ? 'success' : 'neutral'}
                      label={u.status === 0 ? t('users.enabled') : t('users.disabled')}
                    />
                    <ConfigSwitch
                      checked={u.status === 0}
                      disabled={updateUser.isPending}
                      onChange={() => toggleStatus(u)}
                      label={t('users.status')}
                      onLabel={t('users.enabled')}
                      offLabel={t('users.disabled')}
                    />
                    <Button variant="ghost" size="xs" onClick={() => setEditUser(u)}>
                      {t('common.edit')}
                    </Button>
                    <Button
                      variant="ghost"
                      size="xs"
                      className="text-status-danger hover:text-status-danger"
                      onClick={() => setDeleteTarget({ id: u.id, username: u.username })}
                    >
                      {t('common.delete')}
                    </Button>
                  </>
                }
              />
            ))}
          </div>
        ) : (
          <Table appearance="refined">
            <TableHeader>
              <TableRow>
                <TableHead>{t('users.username')}</TableHead>
                <TableHead>{t('users.role')}</TableHead>
                <TableHead>{t('users.status')}</TableHead>
                <TableHead align="right">{t('users.createdAt')}</TableHead>
                <TableHead align="right">{t('users.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {users.map((u) => (
                <TableRow key={u.id}>
                  <TableCell className="font-medium text-foreground">{u.username}</TableCell>
                  <TableCell>
                    <StatusBadge level={roleTone(u.role)} label={roleLabel(u.role)} dot={false} />
                  </TableCell>
                  <TableCell>
                    <ConfigSwitch
                      checked={u.status === 0}
                      disabled={updateUser.isPending}
                      onChange={() => toggleStatus(u)}
                      label={t('users.status')}
                      onLabel={t('users.enabled')}
                      offLabel={t('users.disabled')}
                    />
                  </TableCell>
                  <TableCell align="right" className="text-muted-foreground tabular-nums">{new Date(u.createdAt).toLocaleDateString()}</TableCell>
                  <TableCell align="right">
                    <div className="flex justify-end gap-1">
                      <Button variant="ghost" size="xs" onClick={() => setEditUser(u)}>
                        {t('common.edit')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        onClick={() => setDeleteTarget({ id: u.id, username: u.username })}
                        className="text-status-danger hover:text-status-danger"
                      >
                        {t('common.delete')}
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}

        <TableCardFooter>
          {t('users.cardSummary', { total: totalUsers, enabled: enabledUsers })}
        </TableCardFooter>
      </TableCard>

      <CreateUserDialog open={showCreate} onClose={() => setShowCreate(false)} />
      <CreateInvitationDialog open={showInvite} onClose={() => setShowInvite(false)} />

      {isPlatformAdmin && (
        <Panel>
          <h2 className="text-sm font-semibold">{t('users.invitations')}</h2>
          {!invitations || invitations.length === 0 ? (
            <p className="mt-3 text-sm text-muted-foreground">{t('users.invitationsEmpty')}</p>
          ) : (
            <div className="mt-3 space-y-2">
              {invitations.map((invitation) => (
                <div key={invitation.id} className="flex items-center justify-between gap-3 rounded border px-3 py-2 text-sm">
                  <div className="min-w-0">
                    <p className="truncate font-medium">{invitation.email}</p>
                    <p className="text-xs text-muted-foreground">{t('users.invitationExpiresAt', { date: new Date(invitation.expiresAt).toLocaleString() })}</p>
                  </div>
                  {invitation.revoked ? (
                    <span className="text-xs text-muted-foreground">{t('users.invitationRevoked')}</span>
                  ) : !invitation.used && (
                    <Button variant="ghost" size="xs" onClick={() => revokeInvitation.mutate(invitation.id)} disabled={revokeInvitation.isPending}>
                      {t('users.revokeInvitation')}
                    </Button>
                  )}
                </div>
              ))}
            </div>
          )}
        </Panel>
      )}

      <DangerConfirm
        open={deleteTarget !== null}
        title={t('danger.deleteUserTitle', { name: deleteTarget?.username ?? '' })}
        description={t('danger.deleteUserDesc')}
        confirmLabel={t('common.delete')}
        confirmText={deleteTarget?.username}
        scope="platform"
        onConfirm={() => { if (deleteTarget) deleteUser.mutate(deleteTarget.id); setDeleteTarget(null) }}
        onCancel={() => setDeleteTarget(null)}
      />

      {editUser && (
        <EditUserDialog key={editUser.id} user={editUser} onClose={() => setEditUser(null)} />
      )}
    </div>
  )
}
