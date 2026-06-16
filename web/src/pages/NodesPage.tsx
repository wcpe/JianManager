import { useNodes } from '@/api/nodes'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

const statusLabel: Record<number, { text: string; color: string }> = {
  0: { text: '离线', color: 'text-red-500' },
  1: { text: '在线', color: 'text-green-500' },
  2: { text: '启动中', color: 'text-yellow-500' },
}

export default function NodesPage() {
  const { data: nodes, isLoading } = useNodes({ refetchInterval: 30_000 })

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-2xl font-bold">节点管理</h1>
      </div>

      {isLoading ? (
        <p className="text-muted-foreground">加载中...</p>
      ) : (
        <div className="border rounded-lg">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>名称</TableHead>
                <TableHead>IP</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>CPU</TableHead>
                <TableHead>内存</TableHead>
                <TableHead>磁盘</TableHead>
                <TableHead>系统</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {nodes?.map((node) => {
                const st = statusLabel[node.status] || statusLabel[0]
                return (
                  <TableRow key={node.id}>
                    <TableCell className="font-medium">{node.name}</TableCell>
                    <TableCell className="text-muted-foreground">{node.host}</TableCell>
                    <TableCell>
                      <span className={st.color}>{st.text}</span>
                    </TableCell>
                    <TableCell>{node.cpuUsage ? `${(node.cpuUsage * 100).toFixed(0)}%` : '--'}</TableCell>
                    <TableCell>{node.memoryUsage ? `${(node.memoryUsage * 100).toFixed(0)}%` : '--'}</TableCell>
                    <TableCell>{node.diskUsage ? `${(node.diskUsage * 100).toFixed(0)}%` : '--'}</TableCell>
                    <TableCell className="text-muted-foreground">{node.os} {node.arch}</TableCell>
                  </TableRow>
                )
              })}
              {(!nodes || nodes.length === 0) && (
                <TableRow>
                  <TableCell colSpan={7} className="text-center text-muted-foreground">
                    暂无节点
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  )
}
