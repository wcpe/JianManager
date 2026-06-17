import { useState } from 'react'
import { useUsers, useDeleteUser } from '@/api/users'
import ConfirmDialog from '@/components/ConfirmDialog'
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

const roleLabel: Record<number, string> = {
  0: '组成员',
  1: '组管理员',
  10: '平台管理员',
}

export default function UsersPage() {
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<number | null>(null)
  const { data: users, isLoading } = useUsers()
  const deleteUser = useDeleteUser()

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-2xl font-bold">用户管理</h1>
        <Button onClick={() => setShowCreate(true)}>+ 创建用户</Button>
      </div>

      <CreateUserDialog open={showCreate} onClose={() => setShowCreate(false)} />

      {isLoading ? (
        <p className="text-muted-foreground">加载中...</p>
      ) : (
        <div className="border rounded-lg">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>用户名</TableHead>
                <TableHead>角色</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>创建时间</TableHead>
                <TableHead>操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {users?.map((u) => (
                <TableRow key={u.id}>
                  <TableCell className="font-medium">{u.username}</TableCell>
                  <TableCell>{roleLabel[u.role] ?? `未知(${u.role})`}</TableCell>
                  <TableCell>
                    <span className={u.status === 0 ? 'text-green-500' : 'text-red-500'}>
                      {u.status === 0 ? '● 启用' : '○ 禁用'}
                    </span>
                  </TableCell>
                  <TableCell className="text-muted-foreground">{new Date(u.createdAt).toLocaleDateString()}</TableCell>
                  <TableCell>
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() => setDeleteTarget(u.id)}
                      className="text-red-600 hover:text-red-700"
                    >
                      删除
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
              {(!users || users.length === 0) && (
                <TableRow>
                  <TableCell colSpan={5} className="text-center text-muted-foreground">暂无用户</TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      )}

      <ConfirmDialog
        open={deleteTarget !== null}
        title="确认删除用户"
        description="此操作不可撤销。"
        confirmLabel="删除"
        variant="destructive"
        onConfirm={() => { if (deleteTarget) deleteUser.mutate(deleteTarget); setDeleteTarget(null) }}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}
