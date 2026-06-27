import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  useCreateAlertChannel,
  useUpdateAlertChannel,
  type AlertChannelInfo,
  type ChannelConfig,
} from '@/api/alerts'
import { FieldLabel, FieldError } from '@/components/ui/field-label'
import { MODAL_OVERLAY, MODAL_PANEL } from '@/components/ui/scrollable-dialog'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  channelUsesURL,
  channelIsTelegram,
  channelIsEmail,
  channelIsInApp,
  isEnvRef,
} from './alert-helpers'

interface ChannelDialogProps {
  /** 编辑目标；null 表示创建。 */
  channel: AlertChannelInfo | null
  onClose: () => void
}

const CHANNEL_TYPES = ['inapp', 'webhook', 'email', 'dingtalk', 'wecom', 'feishu', 'discord', 'telegram'] as const

/** 解析后端 config JSON 串为对象，容错为空。 */
function parseConfig(raw: string | undefined): ChannelConfig {
  if (!raw) return {}
  try {
    return JSON.parse(raw) as ChannelConfig
  } catch {
    return {}
  }
}

/** 通知通道创建/编辑对话框（FR-085）。凭证字段强制 ${ENV} 引用并前端预校验。 */
export function ChannelDialog({ channel, onClose }: ChannelDialogProps) {
  const { t } = useTranslation()
  const create = useCreateAlertChannel()
  const update = useUpdateAlertChannel()
  const isEdit = !!channel
  const initialCfg = parseConfig(channel?.config)

  const [type, setType] = useState(channel?.type ?? 'inapp')
  const [name, setName] = useState(channel?.name ?? '')
  const [enabled, setEnabled] = useState(channel?.enabled ?? true)
  const [cfg, setCfg] = useState<ChannelConfig>(initialCfg)

  const nameError = name.trim() === '' ? t('validation.required') : ''

  // 凭证字段（URL/token/password）强制 ${ENV} 引用；非凭证字段（chatId/host/from/to）明文。
  const urlError =
    channelUsesURL(type) && (cfg.url ?? '').trim() !== '' && !isEnvRef(cfg.url ?? '') ? t('alerts.envRefRequired') : ''
  const urlMissing = channelUsesURL(type) && (cfg.url ?? '').trim() === '' ? t('validation.required') : ''
  const tokenError =
    channelIsTelegram(type) && (cfg.token ?? '').trim() !== '' && !isEnvRef(cfg.token ?? '') ? t('alerts.envRefRequired') : ''
  const passwordError =
    channelIsEmail(type) && (cfg.password ?? '').trim() !== '' && !isEnvRef(cfg.password ?? '') ? t('alerts.envRefRequired') : ''

  const hasError = !!(nameError || urlError || urlMissing || tokenError || passwordError)

  const set = (patch: Partial<ChannelConfig>) => setCfg((c) => ({ ...c, ...patch }))

  const handleSubmit = async () => {
    if (hasError) return
    const body = { name, type, enabled, config: cfg }
    if (isEdit && channel) {
      await update.mutateAsync({ id: channel.id, ...body })
    } else {
      await create.mutateAsync(body)
    }
    onClose()
  }

  return (
    <div className={MODAL_OVERLAY} onClick={onClose}>
      <div className={`${MODAL_PANEL} max-w-md space-y-3`} onClick={(e) => e.stopPropagation()}>
        <h2 className="text-lg font-bold">{isEdit ? t('alerts.editChannel') : t('alerts.createChannel')}</h2>

        <div>
          <FieldLabel required>{t('alerts.channelName')}</FieldLabel>
          <input
            className="w-full mt-1 p-2 border rounded aria-invalid:border-destructive"
            value={name}
            aria-invalid={!!nameError}
            onChange={(e) => setName(e.target.value)}
          />
          <FieldError error={nameError} />
        </div>

        <div>
          <FieldLabel>{t('alerts.channelType')}</FieldLabel>
          <Select value={type} onValueChange={(v) => setType(v)}>
            <SelectTrigger className="w-full mt-1">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {CHANNEL_TYPES.map((ct) => (
                <SelectItem key={ct} value={ct}>
                  {t(`alerts.channel_${ct}`, ct)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        {channelIsInApp(type) && <p className="text-sm text-muted-foreground">{t('alerts.inappHint')}</p>}

        {channelUsesURL(type) && (
          <div>
            <FieldLabel required>{t('alerts.channelUrl')}</FieldLabel>
            <input
              className="w-full mt-1 p-2 border rounded aria-invalid:border-destructive font-mono text-sm"
              placeholder="${JM_DINGTALK_WEBHOOK}"
              value={cfg.url ?? ''}
              aria-invalid={!!(urlError || urlMissing)}
              onChange={(e) => set({ url: e.target.value })}
            />
            <FieldError error={urlError || urlMissing} />
            <p className="text-xs text-muted-foreground mt-1">{t('alerts.envRefHint')}</p>
          </div>
        )}

        {channelIsTelegram(type) && (
          <>
            <div>
              <FieldLabel required>{t('alerts.telegramToken')}</FieldLabel>
              <input
                className="w-full mt-1 p-2 border rounded aria-invalid:border-destructive font-mono text-sm"
                placeholder="${JM_TELEGRAM_TOKEN}"
                value={cfg.token ?? ''}
                aria-invalid={!!tokenError}
                onChange={(e) => set({ token: e.target.value })}
              />
              <FieldError error={tokenError} />
              <p className="text-xs text-muted-foreground mt-1">{t('alerts.envRefHint')}</p>
            </div>
            <div>
              <FieldLabel required>{t('alerts.telegramChatId')}</FieldLabel>
              <input className="w-full mt-1 p-2 border rounded" value={cfg.chatId ?? ''} onChange={(e) => set({ chatId: e.target.value })} />
            </div>
          </>
        )}

        {channelIsEmail(type) && (
          <>
            <div className="grid grid-cols-3 gap-2">
              <div className="col-span-2">
                <FieldLabel required>{t('alerts.smtpHost')}</FieldLabel>
                <input className="w-full mt-1 p-2 border rounded" value={cfg.host ?? ''} onChange={(e) => set({ host: e.target.value })} />
              </div>
              <div>
                <FieldLabel required>{t('alerts.smtpPort')}</FieldLabel>
                <input type="number" className="w-full mt-1 p-2 border rounded" placeholder="587" value={cfg.port ?? ''} onChange={(e) => set({ port: Number(e.target.value) })} />
              </div>
            </div>
            <div>
              <FieldLabel>{t('alerts.smtpUsername')}</FieldLabel>
              <input className="w-full mt-1 p-2 border rounded" value={cfg.username ?? ''} onChange={(e) => set({ username: e.target.value })} />
            </div>
            <div>
              <FieldLabel>{t('alerts.smtpPassword')}</FieldLabel>
              <input
                className="w-full mt-1 p-2 border rounded aria-invalid:border-destructive font-mono text-sm"
                placeholder="${JM_SMTP_PASSWORD}"
                value={cfg.password ?? ''}
                aria-invalid={!!passwordError}
                onChange={(e) => set({ password: e.target.value })}
              />
              <FieldError error={passwordError} />
              <p className="text-xs text-muted-foreground mt-1">{t('alerts.envRefHint')}</p>
            </div>
            <div className="grid grid-cols-2 gap-2">
              <div>
                <FieldLabel>{t('alerts.smtpFrom')}</FieldLabel>
                <input className="w-full mt-1 p-2 border rounded" value={cfg.from ?? ''} onChange={(e) => set({ from: e.target.value })} />
              </div>
              <div>
                <FieldLabel required>{t('alerts.smtpTo')}</FieldLabel>
                <input className="w-full mt-1 p-2 border rounded" placeholder="a@x.com, b@x.com" value={cfg.to ?? ''} onChange={(e) => set({ to: e.target.value })} />
              </div>
            </div>
          </>
        )}

        <label className="flex items-center gap-2 text-sm">
          <Checkbox checked={enabled} onCheckedChange={(v) => setEnabled(v === true)} aria-label={t('alerts.enabled')} />
          {t('alerts.enabled')}
        </label>

        <div className="flex gap-2 pt-2">
          <button
            className="px-4 py-2 bg-primary text-primary-foreground rounded-md disabled:opacity-50"
            disabled={hasError || create.isPending || update.isPending}
            onClick={handleSubmit}
          >
            {t('common.save')}
          </button>
          <button className="px-4 py-2 border rounded-md" onClick={onClose}>
            {t('common.cancel')}
          </button>
        </div>
      </div>
    </div>
  )
}
