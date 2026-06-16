import { useState, type FormEvent } from 'react'
import { useBots, useCreateBot, useDeleteBot, useSetBotBehavior, type BotInfo } from '@/api/bots'
import { useInstances } from '@/api/instances'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

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
        <Button onClick={() => setShowCreate(true)}>+ 创建 Bot</Button>
      </div>

      <CreateBotDialog open={showCreate} onOpenChange={setShowCreate} />

      {isLoading ? (
        <p className="text-muted-foreground">加载中...</p>
      ) : (
        <div className="border rounded-lg">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>名称</TableHead>
                <TableHead>实例</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>行为</TableHead>
                <TableHead>服务器</TableHead>
                <TableHead>操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
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
                <TableRow>
                  <TableCell colSpan={6} className="text-center text-muted-foreground">
                    暂无 Bot
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

function BotRow({ bot, onDelete }: { bot: BotInfo; onDelete: (id: number) => void }) {
  const setBehavior = useSetBotBehavior()
  const st = statusConfig[bot.status] || statusConfig.disconnected

  return (
    <TableRow>
      <TableCell className="font-medium">{bot.name}</TableCell>
      <TableCell className="text-muted-foreground">#{bot.instanceId}</TableCell>
      <TableCell>
        <span className={st.color}>{st.text}</span>
      </TableCell>
      <TableCell>
        <Select
          value={bot.behavior}
          onValueChange={(value) => setBehavior.mutate({ id: bot.id, behavior: value })}
        >
          <SelectTrigger className="w-[100px] h-8">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {behaviorOptions.map((opt) => (
              <SelectItem key={opt.value} value={opt.value}>
                {opt.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </TableCell>
      <TableCell className="text-muted-foreground">
        {bot.config.server}:{bot.config.port}
      </TableCell>
      <TableCell>
        <Button
          variant="ghost"
          size="xs"
          onClick={() => onDelete(bot.id)}
          className="text-red-600 hover:text-red-700"
        >
          删除
        </Button>
      </TableCell>
    </TableRow>
  )
}

interface CreateBotDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

function CreateBotDialog({ open, onOpenChange }: CreateBotDialogProps) {
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
          onOpenChange(false)
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

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>创建 Bot</DialogTitle>
        </DialogHeader>

        {error && (
          <div className="p-2 text-sm text-destructive bg-destructive/10 rounded">
            {error}
          </div>
        )}

        <form onSubmit={handleSubmit} className="space-y-3">
          <div className="space-y-1">
            <Label>名称</Label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="GuardBot"
              required
            />
          </div>

          <div className="space-y-1">
            <Label>实例</Label>
            <Select value={instanceId} onValueChange={setInstanceId} required>
              <SelectTrigger className="w-full">
                <SelectValue placeholder="选择实例" />
              </SelectTrigger>
              <SelectContent>
                {instances?.map((inst) => (
                  <SelectItem key={inst.id} value={String(inst.id)}>
                    {inst.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="grid grid-cols-3 gap-3">
            <div className="col-span-2 space-y-1">
              <Label>服务器地址</Label>
              <Input
                value={server}
                onChange={(e) => setServer(e.target.value)}
                placeholder="mc.example.com"
                required
              />
            </div>
            <div className="space-y-1">
              <Label>端口</Label>
              <Input
                value={port}
                onChange={(e) => setPort(e.target.value)}
                type="number"
                required
              />
            </div>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1">
              <Label>认证方式</Label>
              <Select value={auth} onValueChange={setAuth}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="offline">离线</SelectItem>
                  <SelectItem value="microsoft">Microsoft</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1">
              <Label>初始行为</Label>
              <Select value={behavior} onValueChange={setBehavior}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {behaviorOptions.map((opt) => (
                    <SelectItem key={opt.value} value={opt.value}>
                      {opt.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                onOpenChange(false)
                resetForm()
              }}
            >
              取消
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending ? '创建中...' : '创建'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
