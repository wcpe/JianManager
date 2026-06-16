import { useState } from 'react'
import { Link } from 'react-router'
import { useInstances, useStartInstance, useStopInstance, useRestartInstance, useDeleteInstance, useKillInstance } from '@/api/instances'
import CreateInstanceDialog from '@/components/CreateInstanceDialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

const statusConfig: Record<string, { text: string; variant: 'default' | 'secondary' | 'destructive' | 'outline' }> = {
  STOPPED: { text: '停止', variant: 'secondary' },
  STARTING: { text: '启动中', variant: 'outline' },
  RUNNING: { text: '运行', variant: 'default' },
  STOPPING: { text: '停止中', variant: 'outline' },
  CRASHED: { text: '崩溃', variant: 'destructive' },
}

export default function InstancesPage() {
  const [showCreate, setShowCreate] = useState(false)
  const { data: instances, isLoading } = useInstances()
  const start = useStartInstance()
  const stop = useStopInstance()
  const restart = useRestartInstance()
  const kill = useKillInstance()
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
                      <Badge variant={st.variant}>{st.text}</Badge>
                    </TableCell>
                    <TableCell>
                      <div className="flex gap-1">
                        {(inst.status === 'STOPPED' || inst.status === 'CRASHED') && (
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
                        {(inst.status === 'STARTING' || inst.status === 'STOPPING') && (
                          <Button
                            variant="ghost"
                            size="xs"
                            onClick={() => kill.mutate(inst.id)}
                            className="text-yellow-600 hover:text-yellow-700"
                          >
                            强制停止
                          </Button>
                        )}
                        {(inst.status === 'STOPPED' || inst.status === 'CRASHED') && (
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
