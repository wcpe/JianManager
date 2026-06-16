import { useState, type FormEvent } from 'react'
import { useBots, useCreateBot, useDeleteBot, useSetBotBehavior, type BotInfo } from '@/api/bots'
import { useInstances } from '@/api/instances'

const statusConfig: Record<string, { text: string; color: string }> = {
  connected: { text: '已连接', color: 'text-green-500' },
  disconnected: { text: '断开', color: 'text-gray-500' },
  connecting: { text: '连接中', color: 'text-yellow-500' },
  error: { text: '错误', color: 'text-red-500' },
}

const behaviorOptions = [
  { value: 'idle', label: '待机' },
  { value: 'guard', label: '警戒' },
  { value: 'follow', label: '跟随' },
  { value: 'patrol', label: '巡逻' },
]

export default function BotsPage() {
  const [showCreate, setShowCreate] = useState(false)
  const { data: bots, isLoading } = useBots()
  const del = useDeleteBot()

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-2xl font-bold">Bot 管理</h1>
        <button
          onClick={() => setShowCreate(true)}
          className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md hover:bg-primary/90"
        >
          + 创建 Bot
        </button>
      </div>

      <CreateBotDialog open={showCreate} onClose={() => setShowCreate(false)} />

      {isLoading ? (
        <p className="text-muted-foreground">加载中...</p>
      ) : (
        <div className="border rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-muted/50">
              <tr>
                <th className="text-left p-3 font-medium">名称</th>
                <th className="text-left p-3 font-medium">实例</th>
                <th className="text-left p-3 font-medium">状态</th>
                <th className="text-left p-3 font-medium">行为</th>
                <th className="text-left p-3 font-medium">服务器</th>
                <th className="text-left p-3 font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {bots?.map((bot) => (
                <BotRow
                  key={bot.id}
                  bot={bot}
                  onDelete={(id) => {
                    if (confirm('确定删除此 Bot？')) del.mutate(id)
                  }}
                />
              ))}
              {(!bots || bots.length === 0) && (
                <tr>
                  <td colSpan={6} className="p-6 text-center text-muted-foreground">
                    暂无 Bot
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

function BotRow({ bot, onDelete }: { bot: BotInfo; onDelete: (id: number) => void }) {
  const setBehavior = useSetBotBehavior()
  const st = statusConfig[bot.status] || statusConfig.disconnected

  return (
    <tr className="border-t hover:bg-muted/30">
      <td className="p-3 font-medium">{bot.name}</td>
      <td className="p-3 text-muted-foreground">#{bot.instanceId}</td>
      <td className="p-3">
        <span className={st.color}>● {st.text}</span>
      </td>
      <td className="p-3">
        <select
          value={bot.behavior}
          onChange={(e) => setBehavior.mutate({ id: bot.id, behavior: e.target.value })}
          className="px-2 py-1 border rounded bg-background text-sm"
        >
          {behaviorOptions.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {opt.label}
            </option>
          ))}
        </select>
      </td>
      <td className="p-3 text-muted-foreground">
        {bot.config.server}:{bot.config.port}
      </td>
      <td className="p-3">
        <button
          onClick={() => onDelete(bot.id)}
          className="px-2 py-1 text-xs bg-red-500/10 text-red-600 rounded hover:bg-red-500/20"
        >
          删除
        </button>
      </td>
    </tr>
  )
}

function CreateBotDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { data: instances } = useInstances()
  const create = useCreateBot()

  const [name, setName] = useState('')
  const [instanceId, setInstanceId] = useState('')
  const [server, setServer] = useState('')
  const [port, setPort] = useState('25565')
  const [auth, setAuth] = useState('offline')
  const [behavior, setBehavior] = useState('idle')
  const [error, setError] = useState('')

  const resetForm = () => {
    setName('')
    setInstanceId('')
    setServer('')
    setPort('25565')
    setAuth('offline')
    setBehavior('idle')
    setError('')
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    setError('')
    create.mutate(
      {
        instanceId: Number(instanceId),
        name,
        config: { server, port: Number(port), auth },
        behavior,
      },
      {
        onSuccess: () => {
          onClose()
          resetForm()
        },
        onError: (err: unknown) => {
          const msg =
            err instanceof Error && 'response' in err
              ? (err as { response?: { data?: { message?: string } } }).response?.data?.message
              : undefined
          setError(msg || '创建失败')
        },
      },
    )
  }

  if (!open) return null

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div className="bg-background border rounded-lg p-6 w-full max-w-md shadow-lg">
        <h2 className="text-lg font-bold mb-4">创建 Bot</h2>

        {error && (
          <div className="mb-3 p-2 text-sm text-destructive bg-destructive/10 rounded">
            {error}
          </div>
        )}

        <form onSubmit={handleSubmit} className="space-y-3">
          <div>
            <label className="text-sm font-medium">名称</label>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              placeholder="GuardBot"
              required
            />
          </div>

          <div>
            <label className="text-sm font-medium">实例</label>
            <select
              value={instanceId}
              onChange={(e) => setInstanceId(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              required
            >
              <option value="">选择实例</option>
              {instances?.map((inst) => (
                <option key={inst.id} value={inst.id}>
                  {inst.name}
                </option>
              ))}
            </select>
          </div>

          <div className="grid grid-cols-3 gap-3">
            <div className="col-span-2">
              <label className="text-sm font-medium">服务器地址</label>
              <input
                value={server}
                onChange={(e) => setServer(e.target.value)}
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
                placeholder="mc.example.com"
                required
              />
            </div>
            <div>
              <label className="text-sm font-medium">端口</label>
              <input
                value={port}
                onChange={(e) => setPort(e.target.value)}
                type="number"
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
                required
              />
            </div>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="text-sm font-medium">认证方式</label>
              <select
                value={auth}
                onChange={(e) => setAuth(e.target.value)}
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              >
                <option value="offline">离线</option>
                <option value="microsoft">Microsoft</option>
              </select>
            </div>
            <div>
              <label className="text-sm font-medium">初始行为</label>
              <select
                value={behavior}
                onChange={(e) => setBehavior(e.target.value)}
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              >
                {behaviorOptions.map((opt) => (
                  <option key={opt.value} value={opt.value}>
                    {opt.label}
                  </option>
                ))}
              </select>
            </div>
          </div>

          <div className="flex justify-end gap-2 pt-2">
            <button
              type="button"
              onClick={() => {
                onClose()
                resetForm()
              }}
              className="px-4 py-2 text-sm border rounded-md hover:bg-accent"
            >
              取消
            </button>
            <button
              type="submit"
              disabled={create.isPending}
              className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50"
            >
              {create.isPending ? '创建中...' : '创建'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
