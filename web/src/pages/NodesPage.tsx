import { useNodes } from '@/api/nodes'

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
        <div className="border rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-muted/50">
              <tr>
                <th className="text-left p-3 font-medium">名称</th>
                <th className="text-left p-3 font-medium">IP</th>
                <th className="text-left p-3 font-medium">状态</th>
                <th className="text-left p-3 font-medium">CPU</th>
                <th className="text-left p-3 font-medium">内存</th>
                <th className="text-left p-3 font-medium">磁盘</th>
                <th className="text-left p-3 font-medium">系统</th>
              </tr>
            </thead>
            <tbody>
              {nodes?.map((node) => {
                const st = statusLabel[node.status] || statusLabel[0]
                return (
                  <tr key={node.id} className="border-t hover:bg-muted/30">
                    <td className="p-3 font-medium">{node.name}</td>
                    <td className="p-3 text-muted-foreground">{node.host}</td>
                    <td className="p-3">
                      <span className={st.color}>● {st.text}</span>
                    </td>
                    <td className="p-3">{node.cpuUsage ? `${(node.cpuUsage * 100).toFixed(0)}%` : '--'}</td>
                    <td className="p-3">{node.memoryUsage ? `${(node.memoryUsage * 100).toFixed(0)}%` : '--'}</td>
                    <td className="p-3">{node.diskUsage ? `${(node.diskUsage * 100).toFixed(0)}%` : '--'}</td>
                    <td className="p-3 text-muted-foreground">{node.os} {node.arch}</td>
                  </tr>
                )
              })}
              {(!nodes || nodes.length === 0) && (
                <tr>
                  <td colSpan={7} className="p-6 text-center text-muted-foreground">
                    暂无节点
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
