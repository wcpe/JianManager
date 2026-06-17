import { useParams } from 'react-router'
import { useInstance, useStartInstance, useStopInstance, useRestartInstance, useKillInstance } from '@/api/instances'
import { useInstanceMetrics } from '@/api/metrics'
import { useTerminalToken } from '@/api/terminal'
import { useBots } from '@/api/bots'
import FileBrowser from '@/components/FileBrowser'
import TerminalComponent from '@/components/Terminal'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

export default function InstanceDetailPage() {
  const { id } = useParams<{ id: string }>()
  const instanceId = Number(id)
  const { data: instance, isLoading } = useInstance(instanceId)

  const startMut = useStartInstance()
  const stopMut = useStopInstance()
  const restartMut = useRestartInstance()
  const killMut = useKillInstance()

  if (isLoading) {
    return <p className="text-muted-foreground">加载中...</p>
  }

  if (!instance) {
    return <p className="text-muted-foreground">实例不存在</p>
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <div>
          <h1 className="text-2xl font-bold">{instance.name}</h1>
          <p className="text-sm text-muted-foreground">
            状态:{' '}
            <span className={
              instance.status === 'RUNNING' ? 'text-green-500' :
              instance.status === 'CRASHED' ? 'text-red-500' :
              instance.status === 'STARTING' || instance.status === 'STOPPING' ? 'text-yellow-500' :
              'text-gray-500'
            }>
              {instance.status}
            </span>
            {' | '}类型: {instance.type}
            {' | '}启动方式: {instance.processType}
          </p>
        </div>
        <div className="flex gap-2">
          {(instance.status === 'STOPPED' || instance.status === 'CRASHED') && (
            <Button
              variant="outline"
              onClick={() => startMut.mutate(instanceId)}
              disabled={startMut.isPending}
              className="text-green-600 border-green-200 hover:bg-green-50 hover:text-green-700"
            >
              {startMut.isPending ? '启动中...' : '启动'}
            </Button>
          )}
          {instance.status === 'RUNNING' && (
            <>
              <Button
                variant="outline"
                onClick={() => stopMut.mutate(instanceId)}
                disabled={stopMut.isPending}
                className="text-yellow-600 border-yellow-200 hover:bg-yellow-50 hover:text-yellow-700"
              >
                {stopMut.isPending ? '停止中...' : '停止'}
              </Button>
              <Button
                variant="outline"
                onClick={() => restartMut.mutate(instanceId)}
                disabled={restartMut.isPending}
                className="text-blue-600 border-blue-200 hover:bg-blue-50 hover:text-blue-700"
              >
                {restartMut.isPending ? '重启中...' : '重启'}
              </Button>
            </>
          )}
          {(instance.status === 'STARTING' || instance.status === 'STOPPING') && (
            <Button
              variant="outline"
              onClick={() => killMut.mutate(instanceId)}
              disabled={killMut.isPending}
              className="text-yellow-600 border-yellow-200 hover:bg-yellow-50 hover:text-yellow-700"
            >
              {killMut.isPending ? '终止中...' : '强制停止'}
            </Button>
          )}
        </div>
      </div>

      <Tabs defaultValue="控制台">
        <TabsList variant="line">
          <TabsTrigger value="控制台">控制台</TabsTrigger>
          <TabsTrigger value="终端">终端</TabsTrigger>
          <TabsTrigger value="文件">文件</TabsTrigger>
          <TabsTrigger value="配置">配置</TabsTrigger>
          <TabsTrigger value="备份">备份</TabsTrigger>
          <TabsTrigger value="Bot">Bot</TabsTrigger>
        </TabsList>

        <TabsContent value="控制台">
          <ConsoleTab instanceId={instanceId} status={instance.status} />
        </TabsContent>
        <TabsContent value="终端">
          <TerminalTab instanceId={instanceId} status={instance.status} />
        </TabsContent>
        <TabsContent value="文件">
          <FileBrowser instanceId={instanceId} />
        </TabsContent>
        <TabsContent value="配置">
          <ConfigTab instance={instance} />
        </TabsContent>
        <TabsContent value="备份">
          <BackupsTab />
        </TabsContent>
        <TabsContent value="Bot">
          <BotsTab instanceId={instanceId} />
        </TabsContent>
      </Tabs>
    </div>
  )
}

function ConsoleTab({ instanceId, status }: { instanceId: number; status: string }) {
  const { data: metrics, isLoading } = useInstanceMetrics(instanceId, status === 'RUNNING')
  const { data: tokenData, isLoading: tokenLoading, error: tokenError } = useTerminalToken(instanceId, 'read')

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-3 gap-4">
        <div className="border rounded-lg p-3">
          <p className="text-xs text-muted-foreground">TPS</p>
          <p className="text-xl font-bold mt-1">
            {status === 'RUNNING' && !isLoading
              ? (metrics && metrics.tps >= 0 ? metrics.tps : 'N/A')
              : '--'}
          </p>
        </div>
        <div className="border rounded-lg p-3">
          <p className="text-xs text-muted-foreground">在线玩家</p>
          <p className="text-xl font-bold mt-1">
            {status === 'RUNNING' && !isLoading
              ? (metrics && metrics.onlinePlayers >= 0 ? metrics.onlinePlayers : 'N/A')
              : '--'}
          </p>
        </div>
        <div className="border rounded-lg p-3">
          <p className="text-xs text-muted-foreground">内存</p>
          <p className="text-xl font-bold mt-1">
            {status === 'RUNNING' && !isLoading
              ? (metrics && metrics.memoryMb > 0 ? `${metrics.memoryMb} MB` : 'N/A')
              : '--'}
          </p>
        </div>
      </div>

      {status === 'CRASHED' && (
        <div className="border border-red-300 bg-red-50 dark:bg-red-950 dark:border-red-800 rounded-lg p-3 text-sm text-red-700 dark:text-red-300">
          ⚠ 实例已崩溃 — 下方终端显示最近输出（含错误堆栈），请查看崩溃原因
        </div>
      )}
      {status === 'STARTING' && (
        <div className="border border-yellow-300 bg-yellow-50 dark:bg-yellow-950 dark:border-yellow-800 rounded-lg p-3 text-sm text-yellow-700 dark:text-yellow-300">
          ⏳ 实例启动中...
        </div>
      )}

      {tokenError ? (
        <div className="border rounded-lg p-4 bg-[#1a1b26] min-h-[400px] flex items-center justify-center">
          <p className="text-muted-foreground text-sm">无法获取终端连接: {(tokenError as Error).message || '连接失败'}</p>
        </div>
      ) : (
        <TerminalComponent
          instanceId={String(instanceId)}
          wsUrl={tokenData?.wsUrl}
          token={tokenData?.token}
          readOnly
          isLoading={tokenLoading}
        />
      )}
    </div>
  )
}

function TerminalTab({ instanceId, status }: { instanceId: number; status: string }) {
  const { data: tokenData, isLoading, error } = useTerminalToken(instanceId, 'write')

  if (error) {
    return (
      <div className="border rounded-lg p-4 bg-[#1a1b26] min-h-[400px] flex items-center justify-center">
        <p className="text-muted-foreground text-sm">无法获取终端连接</p>
      </div>
    )
  }

  return (
    <div className="space-y-2">
      {status !== 'RUNNING' && (
        <div className="border border-yellow-300 bg-yellow-50 dark:bg-yellow-950 dark:border-yellow-800 rounded-lg p-2 text-xs text-yellow-700 dark:text-yellow-300">
          实例未运行（{status}），终端为只读模式，显示最近输出
        </div>
      )}
      <TerminalComponent
        instanceId={String(instanceId)}
        wsUrl={tokenData?.wsUrl}
        token={tokenData?.token}
        readOnly={status !== 'RUNNING'}
        isLoading={isLoading}
      />
    </div>
  )
}

function ConfigTab({
  instance,
}: {
  instance: { startCommand: string; workDir: string; autoStart: boolean; autoRestart: boolean }
}) {
  return (
    <div className="space-y-4 max-w-lg">
      <div>
        <Label className="text-sm font-medium">启动命令</Label>
        <p className="mt-1 p-2 bg-muted rounded text-sm font-mono">{instance.startCommand}</p>
      </div>
      <div>
        <Label className="text-sm font-medium">工作目录</Label>
        <p className="mt-1 p-2 bg-muted rounded text-sm font-mono">{instance.workDir || '默认'}</p>
      </div>
      <div className="flex gap-4">
        <div className="flex items-center gap-2">
          <Checkbox checked={instance.autoStart} disabled />
          <Label className="text-sm">自动启动</Label>
        </div>
        <div className="flex items-center gap-2">
          <Checkbox checked={instance.autoRestart} disabled />
          <Label className="text-sm">崩溃自动重启</Label>
        </div>
      </div>
    </div>
  )
}

function BackupsTab() {
  return (
    <div>
      <p className="text-sm text-muted-foreground mb-2">备份管理</p>
      <div className="border rounded-lg p-4 min-h-[200px]">
        <p className="text-muted-foreground">备份列表待加载...</p>
      </div>
    </div>
  )
}

function BotsTab({ instanceId }: { instanceId: number }) {
  const { data: bots, isLoading } = useBots(instanceId)

  return (
    <div>
      <p className="text-sm text-muted-foreground mb-2">实例关联的 Bot</p>
      {isLoading ? (
        <p className="text-sm text-muted-foreground">加载中...</p>
      ) : bots && bots.length > 0 ? (
        <div className="border rounded-lg">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>名称</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>行为</TableHead>
                <TableHead>服务器</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {bots.map((bot) => (
                <TableRow key={bot.id}>
                  <TableCell className="font-medium">{bot.name}</TableCell>
                  <TableCell>
                    <span className={bot.status === 'connected' ? 'text-green-500' : 'text-gray-500'}>
                      {bot.status === 'connected' ? '● 已连接' : '○ 断开'}
                    </span>
                  </TableCell>
                  <TableCell className="text-muted-foreground">{bot.behavior}</TableCell>
                  <TableCell className="text-muted-foreground">
                    {bot.config.server}:{bot.config.port}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      ) : (
        <div className="border rounded-lg p-4 min-h-[200px]">
          <p className="text-muted-foreground">暂无关联 Bot</p>
        </div>
      )}
    </div>
  )
}
