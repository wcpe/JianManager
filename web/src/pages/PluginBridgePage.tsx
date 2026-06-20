import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  usePluginConnections,
  usePluginEvents,
  useIssuePluginToken,
  type PluginToken,
} from '@/api/pluginBridge'
import { useInstances } from '@/api/instances'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

/**
 * 插件桥管理页（FR-103 / ADR-012）：展示已连插件的连接状态、最近事件流，
 * 并提供「为实例签发插件桥 token」入口（token 写入插件 config.yml）。
 * 玩家管理增强（FR-055）为后续 FR，本页只做桥的连接态与签发管理。
 */
export default function PluginBridgePage() {
  const { t } = useTranslation()
  const { data: connections } = usePluginConnections({ refetchInterval: 15000 })
  const events = usePluginEvents(50)
  const { data: instances } = useInstances()
  const issueToken = useIssuePluginToken()

  const [showIssue, setShowIssue] = useState(false)
  const [selectedInstance, setSelectedInstance] = useState<string>('')
  const [issued, setIssued] = useState<PluginToken | null>(null)

  const connectedCount = useMemo(
    () => (connections ?? []).filter((c) => c.connected).length,
    [connections],
  )

  const handleIssue = async () => {
    const id = Number(selectedInstance)
    if (!id) return
    try {
      const token = await issueToken.mutateAsync(id)
      setIssued(token)
      toast.success(t('pluginBridge.tokenIssued'))
    } catch {
      toast.error(t('pluginBridge.tokenIssueFailed'))
    }
  }

  const copy = (text: string) => {
    navigator.clipboard?.writeText(text)
    toast.success(t('common.copied', 'Copied'))
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">{t('pluginBridge.title')}</h1>
          <p className="text-sm text-muted-foreground">{t('pluginBridge.subtitle')}</p>
        </div>
        <Button
          onClick={() => {
            setIssued(null)
            setSelectedInstance('')
            setShowIssue(true)
          }}
        >
          {t('pluginBridge.issueToken')}
        </Button>
      </div>

      {/* 概览：已连接数 */}
      <div className="rounded-lg border p-4">
        <div className="text-sm text-muted-foreground">{t('pluginBridge.connectedCount')}</div>
        <div className="text-2xl font-bold">
          {connectedCount} / {(connections ?? []).length}
        </div>
      </div>

      {/* 已连插件列表 */}
      <div>
        <h2 className="mb-2 text-lg font-semibold">{t('pluginBridge.connectionsTitle')}</h2>
        <div className="rounded-lg border overflow-hidden">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('pluginBridge.instance')}</TableHead>
                <TableHead>{t('pluginBridge.status')}</TableHead>
                <TableHead>{t('pluginBridge.lastEvent')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(connections ?? []).map((c) => (
                <TableRow key={c.instanceUuid}>
                  <TableCell>{c.instanceName || c.instanceUuid}</TableCell>
                  <TableCell>
                    <Badge variant={c.connected ? 'default' : 'outline'}>
                      {c.connected ? t('pluginBridge.connected') : t('pluginBridge.disconnected')}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {c.lastEventAt ? new Date(c.lastEventAt * 1000).toLocaleString() : '-'}
                  </TableCell>
                </TableRow>
              ))}
              {(!connections || connections.length === 0) && (
                <TableRow>
                  <TableCell colSpan={3} className="text-center text-muted-foreground">
                    {t('pluginBridge.noConnections')}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      </div>

      {/* 最近事件流 */}
      <div>
        <h2 className="mb-2 text-lg font-semibold">{t('pluginBridge.eventsTitle')}</h2>
        <div className="max-h-80 overflow-auto rounded-lg border bg-muted/30 p-3 font-mono text-xs">
          {events.length === 0 && <div className="text-muted-foreground">{t('pluginBridge.noEvents')}</div>}
          {events.map((e, i) => (
            <div key={i} className="border-b border-border/40 py-1 last:border-0">
              <span className="text-muted-foreground">
                {e.timestamp ? new Date(e.timestamp * 1000).toLocaleTimeString() : ''}
              </span>{' '}
              <Badge variant="secondary" className="mx-1">
                {e.type}
              </Badge>
              <span className="text-muted-foreground">{e.instanceUuid.slice(0, 8)}</span>{' '}
              <span>{e.data}</span>
            </div>
          ))}
        </div>
      </div>

      {/* 签发 token 对话框 */}
      <Dialog open={showIssue} onOpenChange={setShowIssue}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('pluginBridge.issueToken')}</DialogTitle>
            <DialogDescription>{t('pluginBridge.issueHint')}</DialogDescription>
          </DialogHeader>

          {!issued ? (
            <div className="space-y-3">
              <Select value={selectedInstance} onValueChange={setSelectedInstance}>
                <SelectTrigger>
                  <SelectValue placeholder={t('pluginBridge.selectInstance')} />
                </SelectTrigger>
                <SelectContent>
                  {(instances ?? []).map((inst) => (
                    <SelectItem key={inst.id} value={String(inst.id)}>
                      {inst.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          ) : (
            <div className="space-y-3 text-sm">
              <FieldCopy label="wsUrl" value={issued.wsUrl} onCopy={copy} copyLabel={t('common.copy', 'Copy')} />
              <FieldCopy label="token" value={issued.token} onCopy={copy} copyLabel={t('common.copy', 'Copy')} />
              <FieldCopy
                label="instanceUuid"
                value={issued.instanceUuid}
                onCopy={copy}
                copyLabel={t('common.copy', 'Copy')}
              />
              <p className="text-muted-foreground">{t('pluginBridge.writeConfigHint')}</p>
            </div>
          )}

          <DialogFooter>
            {!issued ? (
              <Button onClick={handleIssue} disabled={!selectedInstance || issueToken.isPending}>
                {t('pluginBridge.issue')}
              </Button>
            ) : (
              <Button variant="outline" onClick={() => setShowIssue(false)}>
                {t('common.close', 'Close')}
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

/** token 字段 + 复制按钮的小行。 */
function FieldCopy({
  label,
  value,
  onCopy,
  copyLabel,
}: {
  label: string
  value: string
  onCopy: (v: string) => void
  copyLabel: string
}) {
  return (
    <div>
      <div className="mb-1 text-xs font-medium text-muted-foreground">{label}</div>
      <div className="flex items-center gap-2">
        <code className="flex-1 truncate rounded bg-muted px-2 py-1 text-xs">{value}</code>
        <Button size="sm" variant="outline" onClick={() => onCopy(value)}>
          {copyLabel}
        </Button>
      </div>
    </div>
  )
}
