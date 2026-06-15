import { useSchedules } from '@/api/schedules'

export default function SchedulesPage() {
  const { data: schedules, isLoading } = useSchedules()

  return (
    <div>
      <h1 className="text-2xl font-bold mb-4">定时任务</h1>
      {isLoading ? (
        <p className="text-muted-foreground">加载中...</p>
      ) : (
        <div className="border rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-muted/50">
              <tr>
                <th className="text-left p-3 font-medium">名称</th>
                <th className="text-left p-3 font-medium">实例 ID</th>
                <th className="text-left p-3 font-medium">Cron</th>
                <th className="text-left p-3 font-medium">操作</th>
                <th className="text-left p-3 font-medium">启用</th>
                <th className="text-left p-3 font-medium">上次执行</th>
              </tr>
            </thead>
            <tbody>
              {schedules?.map((s) => (
                <tr key={s.id} className="border-t hover:bg-muted/30">
                  <td className="p-3 font-medium">{s.name}</td>
                  <td className="p-3 text-muted-foreground">{s.instanceId}</td>
                  <td className="p-3 font-mono text-xs">{s.cronExpr}</td>
                  <td className="p-3">{s.action}</td>
                  <td className="p-3">
                    <span className={s.enabled ? 'text-green-500' : 'text-gray-400'}>
                      {s.enabled ? '● 启用' : '○ 禁用'}
                    </span>
                  </td>
                  <td className="p-3 text-muted-foreground">
                    {s.lastRun ? new Date(s.lastRun).toLocaleString() : '未执行'}
                  </td>
                </tr>
              ))}
              {(!schedules || schedules.length === 0) && (
                <tr><td colSpan={6} className="p-6 text-center text-muted-foreground">暂无定时任务</td></tr>
              )}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
