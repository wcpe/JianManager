import { useState } from 'react'
import { Link } from 'react-router'
import { useInstances, useStartInstance, useStopInstance, useRestartInstance, useDeleteInstance } from '@/api/instances'
import CreateInstanceDialog from '@/components/CreateInstanceDialog'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

const statusConfig: Record<string, { text: string; color: string }> = {
  STOPPED: { text: '停止', color: 'text-gray-500' },
  STARTING: { text: '启动中', color: 'text-yellow-500' },
  RUNNING: { text: '运行', color: 'text-green-500' },
  STOPPING: { text: '停止中', color: 'text-yellow-500' },
  CRASHED: { text: '崩溃', color: 'text-red-500' },
}

export default function InstancesPage() {
  const [showCreate, setShowCreate] = useState(false)
  const { data: instances, isLoading } = useInstances()
  const start = useStartInstance()
  const stop = useStopInstance()
  const restart = useRestartInstance()
  const del = useDeleteInstance()

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-2xl font-bold">实例管理</h1>
        <Button onClick={() => setShowCreate(true)}>+ 创建实例</Button>
      </div>

      <CreateInstanceDialog open={showCreate} onClose={() => setShowCreate(false)} />

      {isLoading ? (
        <p className="text-muted-foreground">加载中...</p>
      ) : (
        <div className="border rounded-lg">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>名称</TableHead>
                <TableHead>类型</TableHead>
                <TableHead>启动方式</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {instances?.map((inst) => {
                const st = statusConfig[inst.status] || statusConfig.STOPPED
                return (
                  <TableRow key={inst.id}>
                    <TableCell className="font-medium">
                      <Link to={`/instances/${inst.id}`} className="hover:underline text-primary">
                        {inst.name}
                      </Link>
                    </TableCell>
                    <TableCell className="text-muted-foreground">{inst.type}</TableCell>
                    <TableCell className="text-muted-foreground">{inst.processType}</TableCell>
                    <TableCell>
                      <span className={st.color}>{st.text}</span>
                    </TableCell>
                    <TableCell>
                      <div className="flex gap-1">
                        {inst.status === 'STOPPED' && (
                          <Button
                            variant="ghost"
                            size="xs"
                            onClick={() => start.mutate(inst.id)}
                            className="text-green-600 hover:text-green-700"
                          >
                            启动
                          </Button>
                        )}
                        {inst.status === 'RUNNING' && (
                          <>
                            <Button
                              variant="ghost"
                              size="xs"
                              onClick={() => stop.mutate(inst.id)}
                              className="text-yellow-600 hover:text-yellow-700"
                            >
                              停止
                            </Button>
                            <Button
                              variant="ghost"
                              size="xs"
                              onClick={() => restart.mutate(inst.id)}
                              className="text-blue-600 hover:text-blue-700"
                            >
                              重启
                            </Button>
                          </>
                        )}
                        {inst.status === 'STOPPED' && (
                          <Button
                            variant="ghost"
                            size="xs"
                            onClick={() => {
                              if (confirm('确定删除？')) del.mutate(inst.id)
                            }}
                            className="text-red-600 hover:text-red-700"
                          >
                            删除
                          </Button>
                        )}
                      </div>
                    </TableCell>
                  </TableRow>
                )
              })}
              {(!instances || instances.length === 0) && (
                <TableRow>
                  <TableCell colSpan={5} className="text-center text-muted-foreground">
                    暂无实例
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
