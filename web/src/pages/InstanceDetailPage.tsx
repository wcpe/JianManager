import { useState } from 'react'
import { useParams } from 'react-router'
import { useInstance, useStartInstance, useStopInstance, useRestartInstance } from '@/api/instances'
import { useInstanceMetrics } from '@/api/metrics'
import { useTerminalToken } from '@/api/terminal'
import { useBots } from '@/api/bots'
import FileBrowser from '@/components/FileBrowser'
import TerminalComponent from '@/components/Terminal'

const tabs = ['控制台', '终端', '文件', '配置', '备份', 'Bot']

export default function InstanceDetailPage() {
  const { id } = useParams<{ id: string }>()
  const instanceId = Number(id)
  const { data: instance, isLoading } = useInstance(instanceId)
  const [activeTab, setActiveTab] = useState('控制台')

  const startMut = useStartInstance()
  const stopMut = useStopInstance()
  const restartMut = useRestartInstance()

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
            <span className={instance.status === 'RUNNING' ? 'text-green-500' : 'text-gray-500'}>
              {instance.status}
            </span>
            {' | '}类型: {instance.type}
            {' | '}启动方式: {instance.processType}
          </p>
        </div>
        <div className="flex gap-2">
          {instance.status === 'STOPPED' && (
            <button
              onClick={() => startMut.mutate(instanceId)}
              disabled={startMut.isPending}
              className="px-3 py-1.5 text-sm bg-green-500/10 text-green-600 rounded hover:bg-green-500/20 disabled:opacity-50"
            >
              {startMut.isPending ? '启动中...' : '启动'}
            </button>
          )}
          {instance.status === 'RUNNING' && (
            <>
              <button
                onClick={() => stopMut.mutate(instanceId)}
                disabled={stopMut.isPending}
                className="px-3 py-1.5 text-sm bg-yellow-500/10 text-yellow-600 rounded hover:bg-yellow-500/20 disabled:opacity-50"
              >
                {stopMut.isPending ? '停止中...' : '停止'}
              </button>
              <button
                onClick={() => restartMut.mutate(instanceId)}
                disabled={restartMut.isPending}
                className="px-3 py-1.5 text-sm bg-blue-500/10 text-blue-600 rounded hover:bg-blue-500/20 disabled:opacity-50"
              >
                {restartMut.isPending ? '重启中...' : '重启'}
              </button>
            </>
          )}
        </div>
      </div>

      <div className="border-b mb-4">
        <div className="flex gap-1">
          {tabs.map((tab) => (
            <button
              key={tab}
              onClick={() => setActiveTab(tab)}
              className={`px-4 py-2 text-sm border-b-2 transition-colors ${
                activeTab === tab
                  ? 'border-primary font-medium'
                  : 'border-transparent text-muted-foreground hover:text-foreground'
              }`}
            >
              {tab}
            </button>
          ))}
        </div>
      </div>

      <div>
        {activeTab === '控制台' && <ConsoleTab instanceId={instanceId} />}
        {activeTab === '终端' && <TerminalTab instanceId={instanceId} />}
        {activeTab === '文件' && <FilesTab instanceId={instanceId} />}
        {activeTab === '配置' && <ConfigTab instance={instance} />}
        {activeTab === '备份' && <BackupsTab instanceId={instanceId} />}
        {activeTab === 'Bot' && <BotsTab instanceId={instanceId} />}
      </div>
    </div>
  )
}

function ConsoleTab({ instanceId }: { instanceId: number }) {
  const { data: metrics, isLoading } = useInstanceMetrics(instanceId)
  const { data: tokenData, isLoading: tokenLoading, error: tokenError } = useTerminalToken(instanceId, 'read')

  return (
    <div className="space-y-4">
      {/* 指标卡片 */}
      <div className="grid grid-cols-3 gap-4">
        <div className="border rounded-lg p-3">
          <p className="text-xs text-muted-foreground">TPS</p>
          <p className="text-xl font-bold mt-1">{isLoading ? '--' : (metrics?.tps ?? '--')}</p>
        </div>
        <div className="border rounded-lg p-3">
          <p className="text-xs text-muted-foreground">在线玩家</p>
          <p className="text-xl font-bold mt-1">{isLoading ? '--' : (metrics?.onlinePlayers ?? '--')}</p>
        </div>
        <div className="border rounded-lg p-3">
          <p className="text-xs text-muted-foreground">内存</p>
          <p className="text-xl font-bold mt-1">
            {isLoading ? '--' : metrics?.memoryMb ? `${metrics.memoryMb} MB` : '--'}
          </p>
        </div>
      </div>

      {/* 只读终端 */}
      {tokenError ? (
        <div className="border rounded-lg p-4 bg-[#1a1b26] min-h-[400px] flex items-center justify-center">
          <p className="text-muted-foreground text-sm">无法获取终端 token，请确认实例正在运行</p>
        </div>
      ) : tokenLoading ? (
        <div className="border rounded-lg p-4 bg-[#1a1b26] min-h-[400px] flex items-center justify-center">
          <p className="text-muted-foreground text-sm">连接中...</p>
        </div>
      ) : (
        <TerminalComponent
          instanceId={String(instanceId)}
          wsUrl={tokenData?.wsUrl}
          token={tokenData?.token}
          readOnly
        />
      )}
    </div>
  )
}

function TerminalTab({ instanceId }: { instanceId: number }) {
  const { data: tokenData, isLoading, error } = useTerminalToken(instanceId, 'write')

  if (error) {
    return (
      <div className="border rounded-lg p-4 bg-[#1a1b26] min-h-[400px] flex items-center justify-center">
        <div className="text-center">
          <p className="text-muted-foreground text-sm mb-2">无法获取终端 token</p>
          <p className="text-muted-foreground text-xs">请确认实例正在运行且有终端权限</p>
        </div>
      </div>
    )
  }

  if (isLoading) {
    return (
      <div className="border rounded-lg p-4 bg-[#1a1b26] min-h-[400px] flex items-center justify-center">
        <p className="text-muted-foreground text-sm">正在建立终端连接...</p>
      </div>
    )
  }

  return (
    <TerminalComponent
      instanceId={String(instanceId)}
      wsUrl={tokenData?.wsUrl}
      token={tokenData?.token}
    />
  )
}

function FilesTab({ instanceId }: { instanceId: number }) {
  return <FileBrowser instanceId={instanceId} />
}

function ConfigTab({
  instance,
}: {
  instance: { startCommand: string; workDir: string; autoStart: boolean; autoRestart: boolean }
}) {
  return (
    <div className="space-y-4 max-w-lg">
      <div>
        <label className="text-sm font-medium">启动命令</label>
        <p className="mt-1 p-2 bg-muted rounded text-sm font-mono">{instance.startCommand}</p>
      </div>
      <div>
        <label className="text-sm font-medium">工作目录</label>
        <p className="mt-1 p-2 bg-muted rounded text-sm font-mono">{instance.workDir || '默认'}</p>
      </div>
      <div className="flex gap-4">
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={instance.autoStart} readOnly />
          自动启动
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={instance.autoRestart} readOnly />
          崩溃自动重启
        </label>
      </div>
    </div>
  )
}

function BackupsTab({ instanceId: _instanceId }: { instanceId: number }) {
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
        <div className="border rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-muted/50">
              <tr>
                <th className="text-left p-3 font-medium">名称</th>
                <th className="text-left p-3 font-medium">状态</th>
                <th className="text-left p-3 font-medium">行为</th>
                <th className="text-left p-3 font-medium">服务器</th>
              </tr>
            </thead>
            <tbody>
              {bots.map((bot) => (
                <tr key={bot.id} className="border-t hover:bg-muted/30">
                  <td className="p-3 font-medium">{bot.name}</td>
                  <td className="p-3">
                    <span
                      className={
                        bot.status === 'connected' ? 'text-green-500' : 'text-gray-500'
                      }
                    >
                      {bot.status === 'connected' ? '● 已连接' : '○ 断开'}
                    </span>
                  </td>
                  <td className="p-3 text-muted-foreground">{bot.behavior}</td>
                  <td className="p-3 text-muted-foreground">
                    {bot.config.server}:{bot.config.port}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="border rounded-lg p-4 min-h-[200px]">
          <p className="text-muted-foreground">暂无关联 Bot</p>
        </div>
      )}
    </div>
  )
}
