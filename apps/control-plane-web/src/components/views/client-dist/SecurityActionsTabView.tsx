/**
 * @file SecurityActionsTabView：安全侧「处置（封禁与降级）」Tab 的受控视图
 *       （顶部动作按钮 + 三个处置模态 + 全宽处置流水表）。
 *       处置流水的取数与筛选条件、以及四个候选来源（频道列表 / IP 建议 / 密钥列表 / 频道安全摘要）
 *       均由应用容器取数经 props 注入；三个写入动作与行内解封经 onXxx 上报，
 *       成功/失败文案与 toast 归应用侧。
 * @input lib/client-dist-security-contracts（ClientProtectionAction/SecurityTargetType/ProtectionActionStatus/
 *        KeySecurityState/ChannelProtectionMode/BlockIPRequest/SetKeyStateRequest/SetChannelProtectionRequest/
 *        ClientChannelSecuritySummary）、lib/client-channel-types（ClientChannel/ClientPullKey）、
 *        views/client-dist/security-format（EmptyState/fmtTime/statusVariant/SECURITY_EMPTY）、
 *        Combobox/Badge/Button/Input/Panel/Select/Table/Textarea/Dialog 原语、lucide 图标、翻译上下文
 * @output SecurityActionsTabView、SecurityActionsTabViewProps
 * @sync apps/control-plane-web/src/components/client-dist/SecurityActionsTab.tsx
 * @since FR-502（组件受控化迁包；原 FR-430 / ADR-088 处置 Tab，含 S2.1 常驻表单改模态）
 */
import { useEffect, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Ban, Gauge, RadioTower } from 'lucide-react'
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
import type {
  BlockIPRequest,
  ChannelProtectionMode,
  ClientChannelSecuritySummary,
  ClientProtectionAction,
  KeySecurityState,
  ProtectionActionStatus,
  SecurityTargetType,
  SetChannelProtectionRequest,
  SetKeyStateRequest,
} from '@jianmanager/ui/lib/client-dist-security-contracts'
import type { ClientChannel, ClientPullKey } from '@jianmanager/ui/lib/client-channel-types'
import { EmptyState, SECURITY_EMPTY as EMPTY, fmtTime, statusVariant } from './security-format'

/** 当前打开的处置模态（`null` = 全部关闭）。 */
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

/**
 * 处置 Tab 的注入契约（受控视图，ADR-097 a+b 双范式）。
 *
 * 受控边界：
 * - **归容器（会触发重新取数）**：处置流水数据与加载/错误态，以及三个筛选条件
 *   （`filterTarget`/`filterStatus`/`keyword`）与路由频道 `defaultChannelId`。
 * - **留包内（纯 UI 状态）**：三个模态的开合（`dialog`）与全部表单草稿
 *   （封禁 IP 的 ip/频道/时长/理由，改 key 态的频道/keyId/目标态/理由，
 *   频道防护的频道/模式/重试秒数/理由）。
 * - **两个模态的频道选择**（改 key 态与频道防护）各自决定密钥列表、频道安全摘要两路取数的 key，
 *   但同属表单草稿：真源留在视图，仅在变化时经 `onKeyChannelChange` / `onProtectionChannelChange`
 *   上报，容器只镜像该值用于取数（与 `SecurityProfilesTabView` 的详情 id 同范式）。
 *
 * 写入契约（b 范式）：所有写动作经 `onXxx` 上报请求参数，返回是否成功；
 * 成功/失败文案与 toast 属应用侧决策。模态关闭时机由本视图按返回值决定
 * （成功才关闭、失败保留输入便于重试），行内解封与「清除保护」不关闭模态。
 * 三个处置动作的确认门禁（若有）属应用侧容器职责，视图不在提交前追加或放宽任何门禁。
 */
export interface SecurityActionsTabViewProps {
  // ---- 处置流水（容器调 useClientDistSecurityActions）----
  /** 处置流水行（空数组即空态）。 */
  actions: ClientProtectionAction[]
  /** 流水取数中；仅在无行时展示「加载中」文案。 */
  isLoading: boolean
  /** 流水取数失败；判定优先于空态。 */
  isError: boolean

  // ---- 受控筛选（触发重新取数，故归容器）----
  /** 目标类型筛选（`''` = 全部）。 */
  filterTarget: '' | SecurityTargetType
  /** 目标类型筛选变更上报。 */
  onFilterTargetChange: (value: '' | SecurityTargetType) => void
  /** 状态筛选（`''` = 全部）。 */
  filterStatus: '' | ProtectionActionStatus
  /** 状态筛选变更上报。 */
  onFilterStatusChange: (value: '' | ProtectionActionStatus) => void
  /** 关键字筛选（服务端 `q`）。 */
  keyword: string
  /** 关键字筛选变更上报。 */
  onKeywordChange: (value: string) => void

  // ---- 候选注入（视图不取数）----
  /** 路由当前频道；作为三个模态「打开即重置」的默认频道（与容器取数无关）。 */
  defaultChannelId?: string
  /** 频道下拉候选（容器调 useClientChannels）。 */
  channels: ClientChannel[]
  /** 封禁 IP 的可手输建议项（容器由近窗事件与流水汇总）。 */
  ipSuggestions: string[]
  /**
   * 改 key 态模态选中频道的上报（含「打开即重置为默认频道」）。
   * 频道选择的真源在视图内部（表单草稿），容器只镜像该值以取密钥列表；
   * 容器宜用 `useCallback` 稳定引用（引用变化只会重复上报同一值，无副作用）。
   */
  onKeyChannelChange: (channelId: string) => void
  /** 该频道的密钥候选（容器调 useClientChannel(镜像频道)）。 */
  keys: ClientPullKey[]
  /**
   * 频道防护模态选中频道的上报（含「打开即重置为默认频道」）。
   * 同 `onKeyChannelChange`：真源在视图，容器只镜像以取安全摘要。
   */
  onProtectionChannelChange: (channelId: string) => void
  /** 该频道的安全摘要（容器调 useClientChannelSecuritySummary(镜像频道)）。 */
  protectionSummary?: ClientChannelSecuritySummary

  // ---- 写动作（b 范式，toast 归容器）----
  /** 封禁 IP 在途（禁用提交，避免重复提交）。 */
  blockIpPending?: boolean
  /** 封禁 IP：上报请求体，返回是否成功（成功则由视图清空输入并关闭模态）。 */
  onBlockIp: (body: BlockIPRequest) => Promise<boolean>
  /** 改 key 态在途。 */
  keyStatePending?: boolean
  /** 改 key 态：上报密钥 id 与请求体，返回是否成功（成功则关闭模态）。 */
  onSetKeyState: (keyId: string, body: SetKeyStateRequest) => Promise<boolean>
  /** 频道防护设置在途。 */
  protectionPending?: boolean
  /** 频道防护：上报频道与请求体，返回是否成功（成功则关闭模态）。 */
  onSetProtection: (channelId: string, body: SetChannelProtectionRequest) => Promise<boolean>
  /** 清除频道防护在途。 */
  clearProtectionPending?: boolean
  /** 清除频道防护（模态保持打开，与提交不同）。 */
  onClearProtection: (channelId: string) => Promise<boolean>
  /** 行内解封在途。 */
  unblockPending?: boolean
  /** 取消一条封禁（行内「解封」，无模态）。 */
  onUnblock: (action: ClientProtectionAction) => Promise<boolean>
}

/** 安全侧「封禁与降级」：顶部动作按钮 + 三模态 + 全宽动作流水（处置入口统一，候选可下拉）。 */
export function SecurityActionsTabView({
  actions,
  isLoading,
  isError,
  filterTarget,
  onFilterTargetChange,
  filterStatus,
  onFilterStatusChange,
  keyword,
  onKeywordChange,
  defaultChannelId,
  channels,
  ipSuggestions,
  onKeyChannelChange,
  keys,
  onProtectionChannelChange,
  protectionSummary,
  blockIpPending = false,
  onBlockIp,
  keyStatePending = false,
  onSetKeyState,
  protectionPending = false,
  onSetProtection,
  clearProtectionPending = false,
  onClearProtection,
  unblockPending = false,
  onUnblock,
}: SecurityActionsTabViewProps) {
  const { t } = useTranslation()
  // 三个模态的开合：纯 UI 状态，留在包内。
  const [dialog, setDialog] = useState<ActionsDialog>(null)

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
          <Select value={filterTarget || 'all'} onValueChange={(v) => onFilterTargetChange(v === 'all' ? '' : (v as SecurityTargetType))}>
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
          <Select value={filterStatus || 'all'} onValueChange={(v) => onFilterStatusChange(v === 'all' ? '' : (v as ProtectionActionStatus))}>
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
            onChange={(e) => onKeywordChange(e.target.value)}
          />
        </div>
      </div>

      <ActionsTable
        actions={actions}
        isLoading={isLoading}
        isError={isError}
        unblockPending={unblockPending}
        onUnblock={onUnblock}
      />

      <BlockIpDialog
        open={dialog === 'block-ip'}
        onOpenChange={(v) => setDialog(v ? 'block-ip' : null)}
        defaultChannelId={defaultChannelId}
        channels={channels}
        ipSuggestions={ipSuggestions}
        pending={blockIpPending}
        onSubmit={onBlockIp}
      />
      <KeyStateDialog
        open={dialog === 'key-state'}
        onOpenChange={(v) => setDialog(v ? 'key-state' : null)}
        defaultChannelId={defaultChannelId}
        channels={channels}
        onChannelChange={onKeyChannelChange}
        keys={keys}
        pending={keyStatePending}
        onSubmit={onSetKeyState}
      />
      <ChannelProtectionDialog
        open={dialog === 'channel-protection'}
        onOpenChange={(v) => setDialog(v ? 'channel-protection' : null)}
        defaultChannelId={defaultChannelId}
        channels={channels}
        onChannelChange={onProtectionChannelChange}
        summary={protectionSummary}
        pending={protectionPending}
        clearPending={clearProtectionPending}
        onSubmit={onSetProtection}
        onClear={onClearProtection}
      />
    </div>
  )
}

/** 封禁 IP 模态（频道列表与 IP 建议由容器注入；提交结果决定是否关闭）。 */
function BlockIpDialog({
  open,
  onOpenChange,
  defaultChannelId,
  channels,
  ipSuggestions,
  pending,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  defaultChannelId?: string
  channels: ClientChannel[]
  ipSuggestions: string[]
  pending: boolean
  onSubmit: (body: BlockIPRequest) => Promise<boolean>
}) {
  const { t } = useTranslation()
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

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!ip.trim()) return
    const ok = await onSubmit({
      ip: ip.trim(),
      channelId: channelId || undefined,
      reason,
      durationMinutes: Number(durationMinutes) || 30,
    })
    // 成功才清空并关闭；失败保留输入便于重试。
    if (ok) {
      setIp('')
      onOpenChange(false)
    }
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
              <Button type="submit" disabled={pending || !ip.trim()}>
                {t('clientDistOps.actions.submitBlock')}
              </Button>
            </DialogFooter>
          </ScrollableDialogBody>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * 改 key 态模态。
 * 频道选择真源在本组件（表单草稿），变化时上报容器以取密钥候选；
 * 密钥/目标态/理由同为包内表单草稿。
 */
function KeyStateDialog({
  open,
  onOpenChange,
  defaultChannelId,
  channels,
  onChannelChange,
  keys,
  pending,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  defaultChannelId?: string
  channels: ClientChannel[]
  onChannelChange: (channelId: string) => void
  keys: ClientPullKey[]
  pending: boolean
  onSubmit: (keyId: string, body: SetKeyStateRequest) => Promise<boolean>
}) {
  const { t } = useTranslation()
  const [channelId, setChannelId] = useState('')
  const [keyId, setKeyId] = useState('')
  const [state, setState] = useState<KeySecurityState>('observe')
  const [reason, setReason] = useState(() => t('clientDistOps.actions.reasonKeyState'))
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

  // 频道变化统一在此上报容器取密钥候选：用户改选与「打开即重置为默认频道」两条路径
  // 都只改本地 state（后者在渲染期重置里改），故用 effect 收敛上报、避免两处分叉。
  // 容器回传的引用若不稳定只会重复上报同一值（setState 同值即 bail out），无副作用。
  useEffect(() => {
    onChannelChange(channelId)
  }, [channelId, onChannelChange])

  // keys 变化后 keyId 失效：渲染期校正，不进 effect。
  const keyIdValid = !keyId || keys.some((k) => String(k.id) === keyId)
  const effectiveKeyId = keyIdValid ? keyId : ''

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!effectiveKeyId) return
    const ok = await onSubmit(effectiveKeyId, { state, reason })
    if (ok) onOpenChange(false)
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
              <Button type="submit" disabled={pending || !effectiveKeyId}>
                {t('clientDistOps.actions.submitKeyState')}
              </Button>
            </DialogFooter>
          </ScrollableDialogBody>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * 频道防护模态。
 * 频道选择真源在本组件（表单草稿），变化时上报容器以取当前保护摘要；
 * 模式/重试秒数/理由同为包内表单草稿。
 * 「清除保护」不关闭模态，与「保存」的关闭语义不同。
 */
function ChannelProtectionDialog({
  open,
  onOpenChange,
  defaultChannelId,
  channels,
  onChannelChange,
  summary,
  pending,
  clearPending,
  onSubmit,
  onClear,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  defaultChannelId?: string
  channels: ClientChannel[]
  onChannelChange: (channelId: string) => void
  summary?: ClientChannelSecuritySummary
  pending: boolean
  clearPending: boolean
  onSubmit: (channelId: string, body: SetChannelProtectionRequest) => Promise<boolean>
  onClear: (channelId: string) => Promise<boolean>
}) {
  const { t } = useTranslation()
  const [channelId, setChannelId] = useState('')
  const [mode, setMode] = useState<ChannelProtectionMode>('retry_after')
  const [reason, setReason] = useState(() => t('clientDistOps.actions.reasonProtection'))
  const [retryAfterSeconds, setRetryAfterSeconds] = useState('60')
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

  // 频道变化统一在此上报容器取安全摘要（同 KeyStateDialog：改选与打开即重置都收敛到这里）。
  useEffect(() => {
    onChannelChange(channelId)
  }, [channelId, onChannelChange])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!channelId) return
    const ok = await onSubmit(channelId, {
      mode,
      reason,
      retryAfterSeconds: mode === 'retry_after' ? Number(retryAfterSeconds) || 60 : undefined,
    })
    if (ok) onOpenChange(false)
  }

  const clear = () => {
    if (!channelId) return
    void onClear(channelId)
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
            {summary ? (
              <p className="text-xs text-muted-foreground">
                {t('clientDistOps.actions.currentProtection', {
                  mode: summary.protectionMode || t('clientDistOps.actions.modeNone'),
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
              <Button type="button" variant="outline" className="text-destructive" disabled={clearPending || !channelId} onClick={clear}>
                {t('clientDistOps.actions.clearProtection')}
              </Button>
              <div className="flex gap-2">
                <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
                  {t('common.cancel')}
                </Button>
                <Button type="submit" disabled={pending || !channelId}>
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

/** 处置流水表（数据与加载/错误态由容器注入；行内解封为无模态写动作）。 */
function ActionsTable({
  actions,
  isLoading,
  isError,
  unblockPending,
  onUnblock,
}: {
  actions: ClientProtectionAction[]
  isLoading: boolean
  isError: boolean
  unblockPending: boolean
  onUnblock: (action: ClientProtectionAction) => Promise<boolean>
}) {
  const { t } = useTranslation()
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
                    <Button size="xs" variant="outline" disabled={unblockPending} onClick={() => void onUnblock(action)}>
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
