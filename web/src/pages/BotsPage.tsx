import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useBots, useCreateBot, useDeleteBot, useSetBotBehavior, type BotInfo } from '@/api/bots'
import { useInstances } from '@/api/instances'
import ConfirmDialog from '@/components/ConfirmDialog'
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

export default function BotsPage() {
  const { t } = useTranslation()
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<number | null>(null)
  const { data: bots, isLoading } = useBots()
  const del = useDeleteBot()

  const statusConfig: Record<string, { text: string; color: string }> = {
    connected: { text: t('bots.connected'), color: 'text-green-500' },
    disconnected: { text: t('bots.disconnected'), color: 'text-gray-500' },
    connecting: { text: t('bots.connecting'), color: 'text-yellow-500' },
    error: { text: t('bots.error'), color: 'text-red-500' },
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-2xl font-bold">{t('bots.title')}</h1>
        <Button onClick={() => setShowCreate(true)}>+ {t('bots.createBot')}</Button>
      </div>

      <CreateBotDialog open={showCreate} onOpenChange={setShowCreate} />

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <div className="border rounded-lg">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead>{t('bots.name')}</TableHead>
                <TableHead>{t('bots.instance')}</TableHead>
                <TableHead>{t('bots.status')}</TableHead>
                <TableHead>{t('bots.behavior')}</TableHead>
                <TableHead>{t('bots.server')}</TableHead>
                <TableHead>{t('bots.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {bots?.map((bot) => (
                <BotRow
                  key={bot.id}
                  bot={bot}
                  statusConfig={statusConfig}
                  onDelete={(id) => setDeleteTarget(id)}
                />
              ))}
              {(!bots || bots.length === 0) && (
                <TableRow>
                  <TableCell colSpan={6} className="text-center text-muted-foreground">
                    {t('bots.empty')}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      )}

      <ConfirmDialog
        open={deleteTarget !== null}
        title={t('bots.deleteConfirm')}
        description="此操作不可撤销。"
        confirmLabel={t('common.delete')}
        variant="destructive"
        onConfirm={() => { if (deleteTarget) del.mutate(deleteTarget); setDeleteTarget(null) }}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

function BotRow({ bot, statusConfig, onDelete }: { bot: BotInfo; statusConfig: Record<string, { text: string; color: string }>; onDelete: (id: number) => void }) {
  const { t } = useTranslation()
  const setBehavior = useSetBotBehavior()
  const st = statusConfig[bot.status] || statusConfig.disconnected

  const behaviorOptions = [
    { value: 'idle', label: t('bots.idle') },
    { value: 'guard', label: t('bots.guard') },
    { value: 'follow', label: t('bots.follow') },
    { value: 'patrol', label: t('bots.patrol') },
  ]

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
          {t('common.delete')}
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
  const { t } = useTranslation()
  const { data: instances } = useInstances()
  const create = useCreateBot()

  const [name, setName] = useState('')
  const [instanceId, setInstanceId] = useState('')
  const [server, setServer] = useState('')
  const [port, setPort] = useState('25565')
  const [auth, setAuth] = useState('offline')
  const [behavior, setBehavior] = useState('idle')
  const [error, setError] = useState('')

  const behaviorOptions = [
    { value: 'idle', label: t('bots.idle') },
    { value: 'guard', label: t('bots.guard') },
    { value: 'follow', label: t('bots.follow') },
    { value: 'patrol', label: t('bots.patrol') },
  ]

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
          <div className="p-2 text-sm text-destructive bg-destructive/10 rounded">
            {error}
          </div>
        )}

        <form onSubmit={handleSubmit} className="space-y-3">
          <div className="space-y-1">
            <Label>{t('bots.name')}</Label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="GuardBot"
              required
            />
          </div>

          <div className="space-y-1">
            <Label>{t('bots.instance')}</Label>
            <Select value={instanceId} onValueChange={setInstanceId} required>
              <SelectTrigger className="w-full">
                <SelectValue placeholder={t('bots.selectInstance')} />
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
              <Label>{t('bots.serverAddr')}</Label>
              <Input
                value={server}
                onChange={(e) => setServer(e.target.value)}
                placeholder="mc.example.com"
                required
              />
            </div>
            <div className="space-y-1">
              <Label>{t('bots.port')}</Label>
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
