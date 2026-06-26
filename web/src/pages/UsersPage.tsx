import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { UserRound } from 'lucide-react'
import { useUsers, useDeleteUser, useUpdateUser, type UserInfo } from '@/api/users'
import DangerConfirm from '@/components/DangerConfirm'
import CreateUserDialog from '@/components/CreateUserDialog'
import EditUserDialog from '@/components/EditUserDialog'
import { Button } from '@/components/ui/button'
import { Panel } from '@/components/ui/panel'
import { StatusBadge } from '@/components/ui/status-badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  ConfigRow,
  ConfigSwitch,
  ConfigViewToggle,
  type ConfigView,
} from '@/pages/config-row'

export default function UsersPage() {
  const { t } = useTranslation()
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<{ id: number; username: string } | null>(null)
  const [editUser, setEditUser] = useState<UserInfo | null>(null)
  const [view, setView] = useState<ConfigView>('list')
  const { data: users, isLoading } = useUsers()
  const deleteUser = useDeleteUser()
  const updateUser = useUpdateUser()

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
  const toggleStatus = (u: UserInfo) => updateUser.mutate({ id: u.id, status: u.status === 0 ? 1 : 0 })

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-2xl font-bold">{t('users.title')}</h1>
        <div className="flex items-center gap-2">
          <ConfigViewToggle view={view} onChange={setView} cardLabel={t('common.cardView')} listLabel={t('common.listView')} />
          <Button onClick={() => setShowCreate(true)}>+ {t('users.createUser')}</Button>
        </div>
      </div>

      <CreateUserDialog open={showCreate} onClose={() => setShowCreate(false)} />

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : !users || users.length === 0 ? (
        <Panel>
          <p className="py-6 text-center text-sm text-muted-foreground">{t('users.empty')}</p>
        </Panel>
      ) : view === 'card' ? (
        <div className="flex flex-col gap-2.5">
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
        <Panel bodyClassName="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('users.username')}</TableHead>
                <TableHead>{t('users.role')}</TableHead>
                <TableHead>{t('users.status')}</TableHead>
                <TableHead>{t('users.createdAt')}</TableHead>
                <TableHead className="text-right">{t('users.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {users.map((u) => (
                <TableRow key={u.id}>
                  <TableCell className="font-medium">{u.username}</TableCell>
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
                  <TableCell className="text-muted-foreground">{new Date(u.createdAt).toLocaleDateString()}</TableCell>
                  <TableCell className="text-right">
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
