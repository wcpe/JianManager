import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useCreateBot } from '@/api/bots'
import { useInstance } from '@/api/instances'
import { useNode } from '@/api/nodes'
import { suggestBotServer } from './bot-list'
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

/**
 * 控制台 Bot 段「新建 Bot」对话框（FR-039）。
 * 实例 id 由当前工作区实例预填（不可改），连接地址用「所在节点 host + 默认端口」预填且可改。
 */
interface CreateBotDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 当前工作区打开的实例 id，新建 Bot 归属于它 */
  instanceId: number
}

export default function CreateBotDialog({ open, onOpenChange, instanceId }: CreateBotDialogProps) {
  const { t } = useTranslation()
  const create = useCreateBot()
  const { data: instance } = useInstance(instanceId)
  const { data: node } = useNode(instance?.nodeId ?? 0)
  const suggested = suggestBotServer(node, instance?.serverPort)

  const [name, setName] = useState('')
  const [auth, setAuth] = useState('offline')
  const [behavior, setBehavior] = useState('idle')
  const [error, setError] = useState('')
  // server/port 用户覆盖值：null 表示未改，跟随节点 host 建议值显示（避免在 effect 里同步 props）
  const [serverOverride, setServerOverride] = useState<string | null>(null)
  const [portOverride, setPortOverride] = useState<string | null>(null)

  const server = serverOverride ?? suggested.server
  const port = portOverride ?? String(suggested.port)

  const behaviorOptions = [
    { value: 'idle', label: t('bots.idle') },
    { value: 'guard', label: t('bots.guard') },
    { value: 'follow', label: t('bots.follow') },
    { value: 'patrol', label: t('bots.patrol') },
  ]

  const resetForm = () => {
    setName('')
    setAuth('offline')
    setBehavior('idle')
    setError('')
    setServerOverride(null)
    setPortOverride(null)
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    setError('')
    create.mutate(
      {
        instanceId,
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
          setError(msg || t('bots.createFailed'))
        },
      },
    )
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t('bots.createBot')}</DialogTitle>
        </DialogHeader>

        {error && (
          <div className="p-2 text-sm text-destructive bg-destructive/10 rounded">{error}</div>
        )}

        <form onSubmit={handleSubmit} className="space-y-3">
          <div className="space-y-1">
            <Label>{t('bots.instance')}</Label>
            <Input value={instance?.name ?? `#${instanceId}`} disabled readOnly />
          </div>

          <div className="space-y-1">
            <Label>{t('bots.name')}</Label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="GuardBot"
              required
            />
          </div>

          <div className="grid grid-cols-3 gap-3">
            <div className="col-span-2 space-y-1">
              <Label>{t('bots.serverAddr')}</Label>
              <Input
                value={server}
                onChange={(e) => setServerOverride(e.target.value)}
                placeholder="mc.example.com"
                required
              />
            </div>
            <div className="space-y-1">
              <Label>{t('bots.port')}</Label>
              <Input
                value={port}
                onChange={(e) => setPortOverride(e.target.value)}
                type="number"
                required
              />
            </div>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1">
              <Label>{t('bots.authMethod')}</Label>
              <Select value={auth} onValueChange={setAuth}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="offline">{t('bots.offline')}</SelectItem>
                  <SelectItem value="microsoft">{t('bots.microsoft')}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1">
              <Label>{t('bots.initialBehavior')}</Label>
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
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending ? t('common.creating') : t('common.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
