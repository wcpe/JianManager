import { useState } from 'react'
import { useParams } from 'react-router'
import { useInstance } from '@/api/instances'
import FileBrowser from '@/components/FileBrowser'

const tabs = ['控制台', '终端', '文件', '配置', '备份', 'Bot']

export default function InstanceDetailPage() {
  const { id } = useParams<{ id: string }>()
  const instanceId = Number(id)
  const { data: instance, isLoading } = useInstance(instanceId)
  const [activeTab, setActiveTab] = useState('控制台')

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
            状态: <span className={instance.status === 'RUNNING' ? 'text-green-500' : 'text-gray-500'}>
              {instance.status}
            </span>
            {' | '}类型: {instance.type}
            {' | '}启动方式: {instance.processType}
          </p>
        </div>
        <div className="flex gap-2">
          {instance.status === 'STOPPED' && (
            <button className="px-3 py-1.5 text-sm bg-green-500/10 text-green-600 rounded hover:bg-green-500/20">
              启动
            </button>
          )}
          {instance.status === 'RUNNING' && (
            <>
              <button className="px-3 py-1.5 text-sm bg-yellow-500/10 text-yellow-600 rounded hover:bg-yellow-500/20">
                停止
              </button>
              <button className="px-3 py-1.5 text-sm bg-blue-500/10 text-blue-600 rounded hover:bg-blue-500/20">
                重启
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
  return (
    <div className="border rounded-lg p-4 bg-[#1a1b26] text-[#a9b1d6] font-mono text-sm min-h-[400px]">
      <p className="text-muted-foreground">控制台输出（只读日志流）...</p>
      <p className="text-muted-foreground mt-2">实例 ID: {instanceId}</p>
    </div>
  )
}

function TerminalTab({ instanceId }: { instanceId: number }) {
  return (
    <div>
      <p className="text-sm text-muted-foreground mb-2">交互式终端（直连 Worker Node WebSocket）</p>
      <div className="border rounded-lg bg-[#1a1b26] p-4 min-h-[400px]">
        <p className="text-[#a9b1d6] font-mono text-sm">终端连接待建立...</p>
        <p className="text-[#a9b1d6] font-mono text-sm mt-1">实例 ID: {instanceId}</p>
      </div>
    </div>
  )
}

function FilesTab({ instanceId }: { instanceId: number }) {
  return <FileBrowser instanceId={instanceId} />
}

function ConfigTab({ instance }: { instance: { startCommand: string; workDir: string; autoStart: boolean; autoRestart: boolean } }) {
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

function BotsTab({ instanceId: _instanceId }: { instanceId: number }) {
  return (
    <div>
      <p className="text-sm text-muted-foreground mb-2">Bot 管理</p>
      <div className="border rounded-lg p-4 min-h-[200px]">
        <p className="text-muted-foreground">Bot 列表待加载...</p>
      </div>
    </div>
  )
}
