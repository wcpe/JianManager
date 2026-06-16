import { useSchedules } from '@/api/schedules'
import { Badge } from '@/components/ui/badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

export default function SchedulesPage() {
  const { data: schedules, isLoading } = useSchedules()

  return (
    <div>
      <h1 className="text-2xl font-bold mb-4">定时任务</h1>
      {isLoading ? (
        <p className="text-muted-foreground">加载中...</p>
      ) : (
        <div className="border rounded-lg">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>名称</TableHead>
                <TableHead>实例 ID</TableHead>
                <TableHead>Cron</TableHead>
                <TableHead>操作</TableHead>
                <TableHead>启用</TableHead>
                <TableHead>上次执行</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {schedules?.map((s) => (
                <TableRow key={s.id}>
                  <TableCell className="font-medium">{s.name}</TableCell>
                  <TableCell className="text-muted-foreground">{s.instanceId}</TableCell>
                  <TableCell className="font-mono text-xs">{s.cronExpr}</TableCell>
                  <TableCell>{s.action}</TableCell>
                  <TableCell>
                    <Badge variant={s.enabled ? 'default' : 'secondary'}>
                      {s.enabled ? '启用' : '禁用'}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {s.lastRun ? new Date(s.lastRun).toLocaleString() : '未执行'}
                  </TableCell>
                </TableRow>
              ))}
              {(!schedules || schedules.length === 0) && (
                <TableRow><TableCell colSpan={6} className="text-center text-muted-foreground">暂无定时任务</TableCell></TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  )
}
