import { useNodes } from '@/api/nodes'
import { useInstances } from '@/api/instances'
import { PieChart, Pie, Cell, ResponsiveContainer, Tooltip } from 'recharts'

const COLORS = ['#22c55e', '#ef4444', '#f59e0b', '#3b82f6']

export default function OverviewPage() {
  const { data: nodes } = useNodes()
  const { data: instances } = useInstances()

  const onlineNodes = nodes?.filter((n) => n.status === 1).length ?? 0
  const offlineNodes = nodes?.filter((n) => n.status === 0).length ?? 0
  const totalNodes = nodes?.length ?? 0
  const totalInstances = instances?.length ?? 0
  const runningInstances = instances?.filter((i) => i.status === 'RUNNING').length ?? 0
  const stoppedInstances = instances?.filter((i) => i.status === 'STOPPED').length ?? 0
  const crashedInstances = instances?.filter((i) => i.status === 'CRASHED').length ?? 0

  const nodeData = [
    { name: '在线', value: onlineNodes },
    { name: '离线', value: offlineNodes },
  ].filter((d) => d.value > 0)

  const instanceData = [
    { name: '运行中', value: runningInstances },
    { name: '已停止', value: stoppedInstances },
    { name: '崩溃', value: crashedInstances },
  ].filter((d) => d.value > 0)

  const cards = [
    { label: '节点', value: `${onlineNodes} 在线`, sub: `${totalNodes} 总计` },
    { label: '实例', value: `${totalInstances} 总计`, sub: `${runningInstances} 运行中` },
  ]

  return (
    <div>
      <h1 className="text-2xl font-bold mb-4">仪表盘</h1>

      {/* 概览卡片 */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-4 mb-6">
        {cards.map((card) => (
          <div key={card.label} className="border rounded-lg p-4">
            <p className="text-sm text-muted-foreground">{card.label}</p>
            <p className="text-2xl font-bold mt-1">{card.value}</p>
            <p className="text-xs text-muted-foreground mt-1">{card.sub}</p>
          </div>
        ))}
      </div>

      {/* 图表区域 */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mb-6">
        <div className="border rounded-lg p-4">
          <h3 className="font-medium mb-3">节点状态</h3>
          <div className="h-48">
            {nodeData.length > 0 ? (
              <ResponsiveContainer width="100%" height="100%">
                <PieChart>
                  <Pie
                    data={nodeData}
                    cx="50%"
                    cy="50%"
                    innerRadius={40}
                    outerRadius={70}
                    paddingAngle={5}
                    dataKey="value"
                    label={({ name, value }) => `${name} ${value}`}
                  >
                    {nodeData.map((_, i) => (
                      <Cell key={i} fill={COLORS[i]} />
                    ))}
                  </Pie>
                  <Tooltip />
                </PieChart>
              </ResponsiveContainer>
            ) : (
              <p className="text-muted-foreground text-sm text-center py-8">暂无节点数据</p>
            )}
          </div>
        </div>

        <div className="border rounded-lg p-4">
          <h3 className="font-medium mb-3">实例状态</h3>
          <div className="h-48">
            {instanceData.length > 0 ? (
              <ResponsiveContainer width="100%" height="100%">
                <PieChart>
                  <Pie
                    data={instanceData}
                    cx="50%"
                    cy="50%"
                    innerRadius={40}
                    outerRadius={70}
                    paddingAngle={5}
                    dataKey="value"
                    label={({ name, value }) => `${name} ${value}`}
                  >
                    {instanceData.map((_, i) => (
                      <Cell key={i} fill={COLORS[i]} />
                    ))}
                  </Pie>
                  <Tooltip />
                </PieChart>
              </ResponsiveContainer>
            ) : (
              <p className="text-muted-foreground text-sm text-center py-8">暂无实例数据</p>
            )}
          </div>
        </div>
      </div>

      {/* 节点和实例列表 */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div className="border rounded-lg p-4">
          <h3 className="font-medium mb-3">节点状态</h3>
          <div className="space-y-2">
            {nodes?.map((node) => (
              <div key={node.id} className="flex items-center justify-between text-sm">
                <span>{node.name}</span>
                <div className="flex items-center gap-2">
                  {node.cpuUsage > 0 && (
                    <span className="text-xs text-muted-foreground">CPU {(node.cpuUsage * 100).toFixed(0)}%</span>
                  )}
                  <span className={node.status === 1 ? 'text-green-500' : 'text-red-500'}>
                    {node.status === 1 ? '● 在线' : '○ 离线'}
                  </span>
                </div>
              </div>
            ))}
            {(!nodes || nodes.length === 0) && (
              <p className="text-muted-foreground text-sm">暂无节点</p>
            )}
          </div>
        </div>

        <div className="border rounded-lg p-4">
          <h3 className="font-medium mb-3">最近实例</h3>
          <div className="space-y-2">
            {instances?.slice(0, 5).map((inst) => (
              <div key={inst.id} className="flex items-center justify-between text-sm">
                <span>{inst.name}</span>
                <span
                  className={
                    inst.status === 'RUNNING'
                      ? 'text-green-500'
                      : inst.status === 'CRASHED'
                        ? 'text-red-500'
                        : 'text-gray-500'
                  }
                >
                  {inst.status}
                </span>
              </div>
            ))}
            {(!instances || instances.length === 0) && (
              <p className="text-muted-foreground text-sm">暂无实例</p>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
