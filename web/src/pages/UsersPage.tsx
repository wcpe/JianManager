import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useUsers, useDeleteUser } from '@/api/users'
import DangerConfirm from '@/components/DangerConfirm'
import CreateUserDialog from '@/components/CreateUserDialog'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

export default function UsersPage() {
  const { t } = useTranslation()
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<{ id: number; username: string } | null>(null)
  const { data: users, isLoading } = useUsers()
  const deleteUser = useDeleteUser()

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

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-2xl font-bold">{t('users.title')}</h1>
        <Button onClick={() => setShowCreate(true)}>+ {t('users.createUser')}</Button>
      </div>

      <CreateUserDialog open={showCreate} onClose={() => setShowCreate(false)} />

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <div className="border rounded-lg">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>{t('users.username')}</TableHead>
                <TableHead>{t('users.role')}</TableHead>
                <TableHead>{t('users.status')}</TableHead>
                <TableHead>{t('users.createdAt')}</TableHead>
                <TableHead>{t('users.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {users?.map((u) => (
                <TableRow key={u.id}>
                  <TableCell className="font-medium">{u.username}</TableCell>
                  <TableCell>{roleLabel(u.role)}</TableCell>
                  <TableCell>
                    <span className={u.status === 0 ? 'text-green-500' : 'text-red-500'}>
                      {u.status === 0 ? `● ${t('users.enabled')}` : `○ ${t('users.disabled')}`}
                    </span>
                  </TableCell>
                  <TableCell className="text-muted-foreground">{new Date(u.createdAt).toLocaleDateString()}</TableCell>
                  <TableCell>
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() => setDeleteTarget({ id: u.id, username: u.username })}
                      className="text-red-600 hover:text-red-700"
                    >
                      {t('common.delete')}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
              {(!users || users.length === 0) && (
                <TableRow>
                  <TableCell colSpan={5} className="text-center text-muted-foreground">{t('users.empty')}</TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
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
    </div>
  )
}
