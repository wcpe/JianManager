import { useNodes } from '@/api/nodes'
import { useInstances } from '@/api/instances'

export default function OverviewPage() {
  const { data: nodes } = useNodes()
  const { data: instances } = useInstances()

  const onlineNodes = nodes?.filter((n) => n.status === 1).length ?? 0
  const totalNodes = nodes?.length ?? 0
  const totalInstances = instances?.length ?? 0
  const runningInstances = instances?.filter((i) => i.status === 'RUNNING').length ?? 0

  const cards = [
    { label: '节点', value: `${onlineNodes} 在线`, sub: `${totalNodes} 总计` },
    { label: '实例', value: `${totalInstances} 总计`, sub: `${runningInstances} 运行中` },
  ]

  return (
    <div>
      <h1 className="text-2xl font-bold mb-4">仪表盘</h1>
      <div className="grid grid-cols-2 md:grid-cols-4 gap-4 mb-6">
        {cards.map((card) => (
          <div key={card.label} className="border rounded-lg p-4">
            <p className="text-sm text-muted-foreground">{card.label}</p>
            <p className="text-2xl font-bold mt-1">{card.value}</p>
            <p className="text-xs text-muted-foreground mt-1">{card.sub}</p>
          </div>
        ))}
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div className="border rounded-lg p-4">
          <h3 className="font-medium mb-3">节点状态</h3>
          <div className="space-y-2">
            {nodes?.map((node) => (
              <div key={node.id} className="flex items-center justify-between text-sm">
                <span>{node.name}</span>
                <span className={node.status === 1 ? 'text-green-500' : 'text-red-500'}>
                  {node.status === 1 ? '● 在线' : '○ 离线'}
                </span>
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
