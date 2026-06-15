import { useUsers, useDeleteUser } from '@/api/users'

const roleLabel: Record<number, string> = {
  0: '组成员',
  1: '组管理员',
  10: '平台管理员',
}

export default function UsersPage() {
  const { data: users, isLoading } = useUsers()
  const deleteUser = useDeleteUser()

  return (
    <div>
      <h1 className="text-2xl font-bold mb-4">用户管理</h1>
      {isLoading ? (
        <p className="text-muted-foreground">加载中...</p>
      ) : (
        <div className="border rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-muted/50">
              <tr>
                <th className="text-left p-3 font-medium">用户名</th>
                <th className="text-left p-3 font-medium">角色</th>
                <th className="text-left p-3 font-medium">状态</th>
                <th className="text-left p-3 font-medium">创建时间</th>
                <th className="text-left p-3 font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {users?.map((u) => (
                <tr key={u.id} className="border-t hover:bg-muted/30">
                  <td className="p-3 font-medium">{u.username}</td>
                  <td className="p-3">{roleLabel[u.role] ?? `未知(${u.role})`}</td>
                  <td className="p-3">
                    <span className={u.status === 0 ? 'text-green-500' : 'text-red-500'}>
                      {u.status === 0 ? '● 启用' : '○ 禁用'}
                    </span>
                  </td>
                  <td className="p-3 text-muted-foreground">{new Date(u.createdAt).toLocaleDateString()}</td>
                  <td className="p-3">
                    <button
                      onClick={() => { if (confirm('确定删除？')) deleteUser.mutate(u.id) }}
                      className="px-2 py-1 text-xs bg-red-500/10 text-red-600 rounded hover:bg-red-500/20"
                    >
                      删除
                    </button>
                  </td>
                </tr>
              ))}
              {(!users || users.length === 0) && (
                <tr><td colSpan={5} className="p-6 text-center text-muted-foreground">暂无用户</td></tr>
              )}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
