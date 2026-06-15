import { useGroups } from '@/api/groups'

export default function GroupsPage() {
  const { data: groups, isLoading } = useGroups()

  return (
    <div>
      <h1 className="text-2xl font-bold mb-4">用户组管理</h1>
      {isLoading ? (
        <p className="text-muted-foreground">加载中...</p>
      ) : (
        <div className="space-y-4">
          {groups?.map((g) => (
            <div key={g.id} className="border rounded-lg p-4">
              <div className="flex items-center justify-between mb-2">
                <h3 className="font-medium text-lg">{g.name}</h3>
              </div>
              {g.description && <p className="text-sm text-muted-foreground mb-3">{g.description}</p>}
              <div className="flex gap-4 text-sm">
                <span>成员: {g.members?.length ?? 0}</span>
                {g.quota && (
                  <>
                    <span>实例配额: {g.quota.maxInstances}</span>
                    <span>Bot 配额: {g.quota.maxBots}</span>
                    <span>存储配额: {g.quota.maxStorageMb}MB</span>
                  </>
                )}
              </div>
              {g.members && g.members.length > 0 && (
                <div className="mt-2 flex gap-1 flex-wrap">
                  {g.members.map((m) => (
                    <span key={m.id} className="px-2 py-0.5 text-xs bg-muted rounded">
                      {m.user?.username ?? `用户#${m.userId}`}
                      {m.role === 1 && ' (管理员)'}
                    </span>
                  ))}
                </div>
              )}
            </div>
          ))}
          {(!groups || groups.length === 0) && (
            <p className="text-muted-foreground text-center py-8">暂无用户组</p>
          )}
        </div>
      )}
    </div>
  )
}
