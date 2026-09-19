import { useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Ban, Gauge, RadioTower } from 'lucide-react'
import { toast } from 'sonner'
import {
  useBlockClientDistIP,
  useCancelClientDistIPBlock,
  useClearClientDistChannelProtection,
  useClientChannelSecuritySummary,
  useClientDistSecurityActions,
  useClientDistSecurityEvents,
  useSetClientDistChannelProtection,
  useSetClientDistKeyState,
  type ChannelProtectionMode,
  type ClientProtectionAction,
  type KeySecurityState,
  type ProtectionActionStatus,
  type SecurityTargetType,
} from '@/api/clientDistSecurity'
import { useClientChannel, useClientChannels } from '@/api/clientChannels'
import { Combobox } from '@jianmanager/ui/components/combobox'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@jianmanager/ui/components/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { Textarea } from '@jianmanager/ui/components/textarea'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { EmptyState, SECURITY_EMPTY as EMPTY, fmtTime, statusVariant, useSecurityQuery } from './security-shared'

/**
 * 安全侧「封禁与降级」：顶部动作按钮 + 三模态 + 全宽动作流水（处置入口统一，候选可下拉）。
 */

type ActionsDialog = 'block-ip' | 'key-state' | 'channel-protection' | null

const DURATION_PRESETS = [
  { value: '15', labelKey: 'clientDistOps.actions.dur15m' },
  { value: '30', labelKey: 'clientDistOps.actions.dur30m' },
  { value: '120', labelKey: 'clientDistOps.actions.dur2h' },
  { value: '1440', labelKey: 'clientDistOps.actions.dur24h' },
  { value: '10080', labelKey: 'clientDistOps.actions.dur7d' },
] as const

const KEY_STATE_OPTIONS: KeySecurityState[] = ['normal', 'observe', 'throttled', 'suspended', 'revoked']
const PROT_MODE_OPTIONS: ChannelProtectionMode[] = ['throttle', 'concurrency', 'queue', 'retry_after']

export function ActionsTab() {
  const { t } = useTranslation()
  const { query } = useSecurityQuery()
  const [dialog, setDialog] = useState<ActionsDialog>(null)
  const [filterTarget, setFilterTarget] = useState<'' | SecurityTargetType>('')
  const [filterStatus, setFilterStatus] = useState<'' | ProtectionActionStatus>('')
  const [keyword, setKeyword] = useState('')

  return (
    <div className="space-y-4" data-testid="ops-actions">
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" onClick={() => setDialog('block-ip')} data-testid="action-open-block-ip">
          <Ban className="size-4" />
          {t('clientDistOps.actions.blockIpTitle')}
        </Button>
        <Button size="sm" variant="outline" onClick={() => setDialog('key-state')} data-testid="action-open-key-state">
          <Gauge className="size-4" />
          {t('clientDistOps.actions.keyStateTitle')}
        </Button>
        <Button size="sm" variant="outline" onClick={() => setDialog('channel-protection')} data-testid="action-open-protection">
          <RadioTower className="size-4" />
          {t('clientDistOps.actions.protectionTitle')}
        </Button>
        <div className="ml-auto flex flex-wrap items-center gap-2">
          <Select value={filterTarget || 'all'} onValueChange={(v) => setFilterTarget(v === 'all' ? '' : (v as SecurityTargetType))}>
            <SelectTrigger size="sm" className="w-28" aria-label={t('clientDistOps.actions.colTarget')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t('clientDistOps.actions.filterAllTargets')}</SelectItem>
              <SelectItem value="ip">ip</SelectItem>
              <SelectItem value="key">key</SelectItem>
              <SelectItem value="channel">channel</SelectItem>
              <SelectItem value="machine">machine</SelectItem>
              <SelectItem value="player">player</SelectItem>
            </SelectContent>
          </Select>
          <Select value={filterStatus || 'all'} onValueChange={(v) => setFilterStatus(v === 'all' ? '' : (v as ProtectionActionStatus))}>
            <SelectTrigger size="sm" className="w-28" aria-label={t('clientDistOps.actions.colStatus')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t('clientDistOps.actions.filterAllStatus')}</SelectItem>
              <SelectItem value="active">active</SelectItem>
              <SelectItem value="expired">expired</SelectItem>
              <SelectItem value="canceled">canceled</SelectItem>
            </SelectContent>
          </Select>
          <Input
            className="h-8 w-40"
            placeholder={t('clientDistOps.actions.filterKeyword')}
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
          />
        </div>
      </div>

      <ActionsTable
        filterTarget={filterTarget}
        filterStatus={filterStatus}
        keyword={keyword}
        channelId={query.channelId}
      />

      <BlockIpDialog open={dialog === 'block-ip'} onOpenChange={(v) => setDialog(v ? 'block-ip' : null)} defaultChannelId={query.channelId} />
      <KeyStateDialog open={dialog === 'key-state'} onOpenChange={(v) => setDialog(v ? 'key-state' : null)} defaultChannelId={query.channelId} />
      <ChannelProtectionDialog
        open={dialog === 'channel-protection'}
        onOpenChange={(v) => setDialog(v ? 'channel-protection' : null)}
        defaultChannelId={query.channelId}
      />
    </div>
  )
}

/** 近窗事件 + 动作流水中出现过的 IP，供封禁模态下拉候选（可手输）。 */
function useIpSuggestions(limit = 50): string[] {
  const events = useClientDistSecurityEvents({ limit })
  const actions = useClientDistSecurityActions({ limit: 100 })
  return useMemo(() => {
    const set = new Set<string>()
    for (const e of events.data ?? []) if (e.ip) set.add(e.ip)
    for (const a of actions.data ?? []) if (a.targetType === 'ip' && a.targetValue) set.add(a.targetValue)
    return [...set]
  }, [events.data, actions.data])
}

function useChannelOptions() {
  const { data } = useClientChannels()
  return data ?? []
}

function BlockIpDialog({
  open,
  onOpenChange,
  defaultChannelId,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  defaultChannelId?: string
}) {
  const { t } = useTranslation()
  const channels = useChannelOptions()
  const ipSuggestions = useIpSuggestions()
  const blockIP = useBlockClientDistIP()
  const [ip, setIp] = useState('')
  const [channelId, setChannelId] = useState('')
  const [durationMinutes, setDurationMinutes] = useState('30')
  const [reason, setReason] = useState(() => t('clientDistOps.actions.reasonBlock'))
  const [prevOpen, setPrevOpen] = useState(open)
  const [prevDefaultChannelId, setPrevDefaultChannelId] = useState(defaultChannelId)

  // 渲染期重置（React 官方「props 变化时重置 state」范式），避免 effect 内同步 setState。
  if (open !== prevOpen || defaultChannelId !== prevDefaultChannelId) {
    setPrevOpen(open)
    setPrevDefaultChannelId(defaultChannelId)
    if (open) {
      setIp('')
      setReason(t('clientDistOps.actions.reasonBlock'))
      if (defaultChannelId) setChannelId(defaultChannelId)
    }
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!ip.trim()) return
    blockIP.mutate(
      {
        ip: ip.trim(),
        channelId: channelId || undefined,
        reason,
        durationMinutes: Number(durationMinutes) || 30,
      },
      {
        onSuccess: () => {
          toast.success(t('clientDistOps.actions.toastIpBlocked'))
          setIp('')
          onOpenChange(false)
        },
        onError: () => toast.error(t('clientDistOps.actions.toastIpBlockFailed')),
      },
    )
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={scrollableDialogContentClass}>
        <DialogHeader>
          <DialogTitle>{t('clientDistOps.actions.blockIpTitle')}</DialogTitle>
          <DialogDescription>{t('clientDistOps.actions.blockIpDesc')}</DialogDescription>
        </DialogHeader>
        <form id="ops-block-ip-form" onSubmit={submit}>
          <ScrollableDialogBody className="space-y-3">
            <div className="flex flex-col gap-1 text-sm">
              <span>{t('clientDistOps.actions.fieldIp')}</span>
              <Combobox
                options={ipSuggestions.map((x) => ({ value: x, label: x }))}
                value={ip}
                onChange={setIp}
                allowCustom
                placeholder={t('clientDistOps.actions.phIp')}
                className="w-full"
              />
              {ipSuggestions.length > 0 && (
                <span className="text-xs text-muted-foreground">
                  {t('clientDistOps.actions.ipSuggestHint', { count: ipSuggestions.length })}
                </span>
              )}
            </div>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientDistOps.actions.fieldChannelOptional')}
              <Select value={channelId || 'none'} onValueChange={(v) => setChannelId(v === 'none' ? '' : v)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">{t('clientDistOps.actions.channelNone')}</SelectItem>
                  {channels.map((c) => (
                    <SelectItem key={c.channelId} value={c.channelId}>
                      {c.name} · {c.channelId}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientDistOps.actions.fieldDuration')}
              <Select value={durationMinutes} onValueChange={setDurationMinutes}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {DURATION_PRESETS.map((d) => (
                    <SelectItem key={d.value} value={d.value}>
                      {t(d.labelKey)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientDistOps.actions.fieldReason')}
              <Textarea required value={reason} onChange={(e) => setReason(e.target.value)} />
            </label>
            {/* 提交放在 form 内，避免 Dialog 外 form= 关联在部分浏览器/Radix 焦点下不触发。 */}
            <DialogFooter className="pt-2">
              <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
                {t('common.cancel')}
              </Button>
              <Button type="submit" disabled={blockIP.isPending || !ip.trim()}>
                {t('clientDistOps.actions.submitBlock')}
              </Button>
            </DialogFooter>
          </ScrollableDialogBody>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function KeyStateDialog({
  open,
  onOpenChange,
  defaultChannelId,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  defaultChannelId?: string
}) {
  const { t } = useTranslation()
  const channels = useChannelOptions()
  const [channelId, setChannelId] = useState('')
  const [keyId, setKeyId] = useState('')
  const [state, setState] = useState<KeySecurityState>('observe')
  const [reason, setReason] = useState(() => t('clientDistOps.actions.reasonKeyState'))
  const setKeyState = useSetClientDistKeyState()
  const { data: channelDetail } = useClientChannel(channelId || null)
  const keys = channelDetail?.keys ?? []
  const [prevOpen, setPrevOpen] = useState(open)
  const [prevDefaultChannelId, setPrevDefaultChannelId] = useState(defaultChannelId)

  if (open !== prevOpen || defaultChannelId !== prevDefaultChannelId) {
    setPrevOpen(open)
    setPrevDefaultChannelId(defaultChannelId)
    if (open) {
      setReason(t('clientDistOps.actions.reasonKeyState'))
      setKeyId('')
      if (defaultChannelId) setChannelId(defaultChannelId)
    }
  }

  // keys 变化后 keyId 失效：渲染期校正，不进 effect。
  const keyIdValid = !keyId || keys.some((k) => String(k.id) === keyId)
  const effectiveKeyId = keyIdValid ? keyId : ''

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!effectiveKeyId) return
    setKeyState.mutate(
      { keyId: effectiveKeyId, body: { state, reason } },
      {
        onSuccess: () => {
          toast.success(t('clientDistOps.actions.toastKeyOk'))
          onOpenChange(false)
        },
        onError: () => toast.error(t('clientDistOps.actions.toastKeyFailed')),
      },
    )
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={scrollableDialogContentClass}>
        <DialogHeader>
          <DialogTitle>{t('clientDistOps.actions.keyStateTitle')}</DialogTitle>
          <DialogDescription>{t('clientDistOps.actions.keyStateDesc')}</DialogDescription>
        </DialogHeader>
        <form id="ops-key-state-form" onSubmit={submit}>
          <ScrollableDialogBody className="space-y-3">
            <label className="flex flex-col gap-1 text-sm">
              {t('clientDistOps.actions.fieldChannel')}
              <Select value={channelId || 'none'} onValueChange={(v) => setChannelId(v === 'none' ? '' : v)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none" disabled>
                    {t('clientDistOps.actions.selectChannelFirst')}
                  </SelectItem>
                  {channels.map((c) => (
                    <SelectItem key={c.channelId} value={c.channelId}>
                      {c.name} · {c.channelId}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientDistOps.actions.fieldKey')}
              <Select value={effectiveKeyId || 'none'} onValueChange={(v) => setKeyId(v === 'none' ? '' : v)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none" disabled>
                    {channelId ? t('clientDistOps.actions.selectKeyFirst') : t('clientDistOps.actions.selectChannelFirst')}
                  </SelectItem>
                  {keys.map((k) => (
                    <SelectItem key={k.id} value={String(k.id)}>
                      {k.name} · {k.keyPrefix}
                      {k.revoked ? ` · ${t('clientDistOps.actions.keyRevoked')}` : ''}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientDistOps.actions.fieldState')}
              <Select value={state} onValueChange={(v) => setState(v as KeySecurityState)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {KEY_STATE_OPTIONS.map((s) => (
                    <SelectItem key={s} value={s}>
                      {t(`clientDistOps.actions.state${s[0].toUpperCase()}${s.slice(1)}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientDistOps.actions.fieldReason')}
              <Textarea required value={reason} onChange={(e) => setReason(e.target.value)} />
            </label>
            <DialogFooter className="pt-2">
              <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
                {t('common.cancel')}
              </Button>
              <Button type="submit" disabled={setKeyState.isPending || !effectiveKeyId}>
                {t('clientDistOps.actions.submitKeyState')}
              </Button>
            </DialogFooter>
          </ScrollableDialogBody>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function ChannelProtectionDialog({
  open,
  onOpenChange,
  defaultChannelId,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  defaultChannelId?: string
}) {
  const { t } = useTranslation()
  const channels = useChannelOptions()
  const [channelId, setChannelId] = useState('')
  const [mode, setMode] = useState<ChannelProtectionMode>('retry_after')
  const [reason, setReason] = useState(() => t('clientDistOps.actions.reasonProtection'))
  const [retryAfterSeconds, setRetryAfterSeconds] = useState('60')
  const setProtection = useSetClientDistChannelProtection()
  const clearProtection = useClearClientDistChannelProtection()
  const summary = useClientChannelSecuritySummary(channelId || '')
  const [prevOpen, setPrevOpen] = useState(open)
  const [prevDefaultChannelId, setPrevDefaultChannelId] = useState(defaultChannelId)

  if (open !== prevOpen || defaultChannelId !== prevDefaultChannelId) {
    setPrevOpen(open)
    setPrevDefaultChannelId(defaultChannelId)
    if (open) {
      setReason(t('clientDistOps.actions.reasonProtection'))
      if (defaultChannelId) setChannelId(defaultChannelId)
    }
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!channelId) return
    setProtection.mutate(
      {
        channelId,
        body: {
          mode,
          reason,
          retryAfterSeconds: mode === 'retry_after' ? Number(retryAfterSeconds) || 60 : undefined,
        },
      },
      {
        onSuccess: () => {
          toast.success(t('clientDistOps.actions.toastProtOk'))
          onOpenChange(false)
        },
        onError: () => toast.error(t('clientDistOps.actions.toastProtFailed')),
      },
    )
  }

  const clear = () => {
    if (!channelId) return
    clearProtection.mutate(channelId, {
      onSuccess: () => toast.success(t('clientDistOps.actions.toastProtCleared')),
      onError: () => toast.error(t('clientDistOps.actions.toastProtClearFailed')),
    })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={scrollableDialogContentClass}>
        <DialogHeader>
          <DialogTitle>{t('clientDistOps.actions.protectionTitle')}</DialogTitle>
          <DialogDescription>{t('clientDistOps.actions.protectionDesc')}</DialogDescription>
        </DialogHeader>
        <form id="ops-protection-form" onSubmit={submit}>
          <ScrollableDialogBody className="space-y-3">
            <label className="flex flex-col gap-1 text-sm">
              {t('clientDistOps.actions.fieldChannel')}
              <Select value={channelId || 'none'} onValueChange={(v) => setChannelId(v === 'none' ? '' : v)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none" disabled>
                    {t('clientDistOps.actions.selectChannelFirst')}
                  </SelectItem>
                  {channels.map((c) => (
                    <SelectItem key={c.channelId} value={c.channelId}>
                      {c.name} · {c.channelId}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </label>
            {summary.data ? (
              <p className="text-xs text-muted-foreground">
                {t('clientDistOps.actions.currentProtection', {
                  mode: summary.data.protectionMode || t('clientDistOps.actions.modeNone'),
                })}
              </p>
            ) : null}
            <label className="flex flex-col gap-1 text-sm">
              {t('clientDistOps.actions.fieldMode')}
              <Select value={mode} onValueChange={(v) => setMode(v as ChannelProtectionMode)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {PROT_MODE_OPTIONS.map((m) => (
                    <SelectItem key={m} value={m}>
                      {t(`clientDistOps.actions.mode${m === 'retry_after' ? 'RetryAfter' : m[0].toUpperCase() + m.slice(1)}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </label>
            {mode === 'retry_after' ? (
              <label className="flex flex-col gap-1 text-sm">
                {t('clientDistOps.actions.phRetryAfter')}
                <Input min={1} type="number" value={retryAfterSeconds} onChange={(e) => setRetryAfterSeconds(e.target.value)} />
              </label>
            ) : null}
            <label className="flex flex-col gap-1 text-sm">
              {t('clientDistOps.actions.fieldReason')}
              <Textarea required value={reason} onChange={(e) => setReason(e.target.value)} />
            </label>
            <DialogFooter className="flex flex-wrap gap-2 pt-2 sm:justify-between">
              <Button type="button" variant="outline" className="text-destructive" disabled={clearProtection.isPending || !channelId} onClick={clear}>
                {t('clientDistOps.actions.clearProtection')}
              </Button>
              <div className="flex gap-2">
                <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
                  {t('common.cancel')}
                </Button>
                <Button type="submit" disabled={setProtection.isPending || !channelId}>
                  {t('clientDistOps.actions.submitProtection')}
                </Button>
              </div>
            </DialogFooter>
          </ScrollableDialogBody>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function ActionsTable({
  filterTarget,
  filterStatus,
  keyword,
  channelId,
}: {
  filterTarget: '' | SecurityTargetType
  filterStatus: '' | ProtectionActionStatus
  keyword: string
  channelId?: string
}) {
  const { t } = useTranslation()
  const { data, isError, isLoading } = useClientDistSecurityActions({
    limit: 200,
    targetType: filterTarget || undefined,
    status: filterStatus || undefined,
    q: keyword.trim() || undefined,
    channelId: channelId || undefined,
  })
  const cancel = useCancelClientDistIPBlock()
  const actions = data ?? []
  const cancelAction = (action: ClientProtectionAction) => {
    cancel.mutate(action.id, {
      onSuccess: () => toast.success(t('clientDistOps.actions.toastUnblocked')),
      onError: () => toast.error(t('clientDistOps.actions.toastUnblockFailed')),
    })
  }
  return (
    <Panel title={t('clientDistOps.actions.tableTitle')}>
      {isError ? (
        <EmptyState text={t('clientDistOps.actions.tableError')} />
      ) : actions.length === 0 ? (
        <EmptyState text={isLoading ? t('common.loading') : t('clientDistOps.actions.tableEmpty')} />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('clientDistOps.actions.colTarget')}</TableHead>
              <TableHead>{t('clientDistOps.actions.colAction')}</TableHead>
              <TableHead>{t('clientDistOps.actions.colStatus')}</TableHead>
              <TableHead>{t('clientDistOps.actions.colReason')}</TableHead>
              <TableHead>{t('clientDistOps.actions.colExpires')}</TableHead>
              <TableHead>{t('clientDistOps.actions.colSource')}</TableHead>
              <TableHead className="text-right">{t('common.actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {actions.map((action) => (
              <TableRow key={action.id}>
                <TableCell>
                  <div className="font-medium">{action.targetValue}</div>
                  <div className="text-xs text-muted-foreground">{action.targetType}</div>
                </TableCell>
                <TableCell>{action.action}</TableCell>
                <TableCell>
                  <Badge variant={statusVariant(action.status)}>{action.status}</Badge>
                </TableCell>
                <TableCell className="max-w-52 truncate">{action.reason || EMPTY}</TableCell>
                <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(action.expiresAt)}</TableCell>
                <TableCell>{action.auto ? t('clientDistOps.actions.auto') : t('clientDistOps.actions.manual')}</TableCell>
                <TableCell className="text-right">
                  {action.targetType === 'ip' && action.action === 'temp_block' && action.status === 'active' ? (
                    <Button size="xs" variant="outline" disabled={cancel.isPending} onClick={() => cancelAction(action)}>
                      {t('clientDistOps.actions.unblock')}
                    </Button>
                  ) : (
                    EMPTY
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </Panel>
  )
}
