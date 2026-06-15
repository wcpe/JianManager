import { useAuditLogs } from '@/api/audit'

export default function AuditPage() {
  const { data: logs, isLoading } = useAuditLogs({ limit: 100 })

  return (
    <div>
      <h1 className="text-2xl font-bold mb-4">审计日志</h1>
      {isLoading ? (
        <p className="text-muted-foreground">加载中...</p>
      ) : (
        <div className="border rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-muted/50">
              <tr>
                <th className="text-left p-3 font-medium">时间</th>
                <th className="text-left p-3 font-medium">用户</th>
                <th className="text-left p-3 font-medium">操作</th>
                <th className="text-left p-3 font-medium">目标</th>
                <th className="text-left p-3 font-medium">IP</th>
              </tr>
            </thead>
            <tbody>
              {logs?.map((log) => (
                <tr key={log.id} className="border-t hover:bg-muted/30">
                  <td className="p-3 text-muted-foreground whitespace-nowrap">
                    {new Date(log.createdAt).toLocaleString()}
                  </td>
                  <td className="p-3">{log.user?.username ?? `#${log.userId}`}</td>
                  <td className="p-3">
                    <span className="px-2 py-0.5 text-xs bg-muted rounded font-mono">{log.action}</span>
                  </td>
                  <td className="p-3 text-muted-foreground">
                    {log.targetType && `${log.targetType}#${log.targetId}`}
                  </td>
                  <td className="p-3 text-muted-foreground">{log.ip}</td>
                </tr>
              ))}
              {(!logs || logs.length === 0) && (
                <tr><td colSpan={5} className="p-6 text-center text-muted-foreground">暂无审计日志</td></tr>
              )}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
