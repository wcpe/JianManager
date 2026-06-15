import { useInstances, useStartInstance, useStopInstance, useRestartInstance, useDeleteInstance } from '@/api/instances'

const statusConfig: Record<string, { text: string; color: string }> = {
  STOPPED: { text: '停止', color: 'text-gray-500' },
  STARTING: { text: '启动中', color: 'text-yellow-500' },
  RUNNING: { text: '运行', color: 'text-green-500' },
  STOPPING: { text: '停止中', color: 'text-yellow-500' },
  CRASHED: { text: '崩溃', color: 'text-red-500' },
}

export default function InstancesPage() {
  const { data: instances, isLoading } = useInstances()
  const start = useStartInstance()
  const stop = useStopInstance()
  const restart = useRestartInstance()
  const del = useDeleteInstance()

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-2xl font-bold">实例管理</h1>
      </div>

      {isLoading ? (
        <p className="text-muted-foreground">加载中...</p>
      ) : (
        <div className="border rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-muted/50">
              <tr>
                <th className="text-left p-3 font-medium">名称</th>
                <th className="text-left p-3 font-medium">类型</th>
                <th className="text-left p-3 font-medium">启动方式</th>
                <th className="text-left p-3 font-medium">状态</th>
                <th className="text-left p-3 font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {instances?.map((inst) => {
                const st = statusConfig[inst.status] || statusConfig.STOPPED
                return (
                  <tr key={inst.id} className="border-t hover:bg-muted/30">
                    <td className="p-3 font-medium">{inst.name}</td>
                    <td className="p-3 text-muted-foreground">{inst.type}</td>
                    <td className="p-3 text-muted-foreground">{inst.processType}</td>
                    <td className="p-3">
                      <span className={st.color}>● {st.text}</span>
                    </td>
                    <td className="p-3">
                      <div className="flex gap-1">
                        {inst.status === 'STOPPED' && (
                          <button
                            onClick={() => start.mutate(inst.id)}
                            className="px-2 py-1 text-xs bg-green-500/10 text-green-600 rounded hover:bg-green-500/20"
                          >
                            启动
                          </button>
                        )}
                        {inst.status === 'RUNNING' && (
                          <>
                            <button
                              onClick={() => stop.mutate(inst.id)}
                              className="px-2 py-1 text-xs bg-yellow-500/10 text-yellow-600 rounded hover:bg-yellow-500/20"
                            >
                              停止
                            </button>
                            <button
                              onClick={() => restart.mutate(inst.id)}
                              className="px-2 py-1 text-xs bg-blue-500/10 text-blue-600 rounded hover:bg-blue-500/20"
                            >
                              重启
                            </button>
                          </>
                        )}
                        {inst.status === 'STOPPED' && (
                          <button
                            onClick={() => {
                              if (confirm('确定删除？')) del.mutate(inst.id)
                            }}
                            className="px-2 py-1 text-xs bg-red-500/10 text-red-600 rounded hover:bg-red-500/20"
                          >
                            删除
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                )
              })}
              {(!instances || instances.length === 0) && (
                <tr>
                  <td colSpan={5} className="p-6 text-center text-muted-foreground">
                    暂无实例
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
