import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { KeyRound, QrCode, Send, SlidersHorizontal } from 'lucide-react'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  ScrollableDialogBody,
  scrollableDialogContentClass,
} from '@jianmanager/ui/components/scrollable-dialog'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import type {
  AlertChannelInfo,
  ChannelConfig,
  ChannelSubmitBody,
  QQBindFill,
} from '@/lib/alert-contracts'
import {
  channelUsesURL,
  channelIsTelegram,
  channelIsEmail,
  channelIsInApp,
  channelIsQQ,
  isQQTargetType,
  isEnvRef,
} from '@/lib/alert-helpers'

/** 扫码绑定弹窗的渲染参数（应用侧提供具体实现，如 QQBindDialog）。 */
export interface ChannelBindDialogArgs {
  /** 关闭绑定弹窗。 */
  onClose: () => void
  /** 扫码成功回填：由本视图写入表单并触发 onBindFilled 提示。 */
  onBound: (fill: QQBindFill) => void
}

export interface ChannelDialogViewProps {
  /** 编辑目标；null 表示创建。 */
  channel: AlertChannelInfo | null
  onClose: () => void
  /** 提交回调：容器负责调用创建/更新 mutation。 */
  onSubmit: (body: ChannelSubmitBody) => Promise<void> | void
  /** 提交中（禁用保存按钮）。 */
  submitting?: boolean
  /** QQ 扫码绑定弹窗插槽。 */
  renderBindDialog?: (args: ChannelBindDialogArgs) => ReactNode
  /** 扫码回填完成后的提示钩子（容器注入 toast）。 */
  onBindFilled?: () => void
}

const CHANNEL_TYPES = ['inapp', 'webhook', 'email', 'dingtalk', 'wecom', 'feishu', 'discord', 'telegram', 'qq'] as const

/** 解析后端 config JSON 串为对象，容错为空。 */
function parseConfig(raw: string | undefined): ChannelConfig {
  if (!raw) return {}
  try {
    return JSON.parse(raw) as ChannelConfig
  } catch {
    return {}
  }
}

/**
 * 通知通道创建/编辑对话框（FR-085 / FR-494）。
 *
 * 凭证字段强制 ${ENV} 引用并前端预校验；QQ 通道另带「扫码绑定」——扫一次码即自动
 * 填入 AppID / AppSecret 引用名 / 目标 user_openid，手工填写路径始终保留作为兜底
 * （扫码依赖 q.qq.com 的非文档化 lite 接口，平台调整时会失效）。
 * 字段一律走 @jianmanager/ui 组件（Input / Panel / Select / FieldLabel），不手搓原生样式。
 */
export function ChannelDialogView({
  channel,
  onClose,
  onSubmit,
  submitting = false,
  renderBindDialog,
  onBindFilled,
}: ChannelDialogViewProps) {
  const { t } = useTranslation()
  const isEdit = !!channel
  const initialCfg = parseConfig(channel?.config)

  const [type, setType] = useState(channel?.type ?? 'inapp')
  const [name, setName] = useState(channel?.name ?? '')
  const [enabled, setEnabled] = useState(channel?.enabled ?? true)
  const [cfg, setCfg] = useState<ChannelConfig>(initialCfg)
  /** 扫码绑定弹窗开关（QQ 通道专用）。 */
  const [bindOpen, setBindOpen] = useState(false)
  /** 本次会话是否已扫码取得凭证——仅用于提示「已获取，请检查后保存」。 */
  const [bindFilled, setBindFilled] = useState(false)

  const nameError = name.trim() === '' ? t('validation.required') : ''

  // 凭证字段（URL/token/password）强制 ${ENV} 引用；非凭证字段（chatId/host/from/to）明文。
  const urlError =
    channelUsesURL(type) && (cfg.url ?? '').trim() !== '' && !isEnvRef(cfg.url ?? '') ? t('alerts.envRefRequired') : ''
  const urlMissing = channelUsesURL(type) && (cfg.url ?? '').trim() === '' ? t('validation.required') : ''
  const tokenMissing = channelIsTelegram(type) && (cfg.token ?? '').trim() === '' ? t('validation.required') : ''
  const tokenError =
    channelIsTelegram(type) && (cfg.token ?? '').trim() !== '' && !isEnvRef(cfg.token ?? '') ? t('alerts.envRefRequired') : ''
  const chatMissing = channelIsTelegram(type) && (cfg.chatId ?? '').trim() === '' ? t('validation.required') : ''
  const hostMissing = channelIsEmail(type) && (cfg.host ?? '').trim() === '' ? t('validation.required') : ''
  const portMissing = channelIsEmail(type) && !cfg.port ? t('validation.required') : ''
  const toMissing = channelIsEmail(type) && (cfg.to ?? '').trim() === '' ? t('validation.required') : ''
  const passwordError =
    channelIsEmail(type) && (cfg.password ?? '').trim() !== '' && !isEnvRef(cfg.password ?? '') ? t('alerts.envRefRequired') : ''

  // QQ 通道（FR-494）：appId/targetId 明文必填；appSecret 强制 ${ENV} 引用（扫码填的是引用名）。
  const appIdMissing = channelIsQQ(type) && (cfg.appId ?? '').trim() === '' ? t('validation.required') : ''
  const appSecretMissing =
    channelIsQQ(type) && (cfg.appSecret ?? '').trim() === '' ? t('validation.required') : ''
  const appSecretError =
    channelIsQQ(type) && (cfg.appSecret ?? '').trim() !== '' && !isEnvRef(cfg.appSecret ?? '')
      ? t('alerts.envRefRequired')
      : ''
  const openidMissing = channelIsQQ(type) && (cfg.targetId ?? '').trim() === '' ? t('validation.required') : ''
  // targetType 已固定为单聊（不再暴露控件，提交时一律写 c2c）。群主动消息被 QQ 平台拒绝
  // （40034105，见 FR-494 spec §6），故存量 group 通道只作提示：保存后自动纠正为单聊。
  // 不作为错误拦截——该字段没有可编辑控件，拦截等于把用户锁死在这条通道上。
  const legacyGroupTarget = channelIsQQ(type) && !!cfg.targetType && !isQQTargetType(cfg.targetType)

  const hasError = !!(
    nameError ||
    urlError ||
    urlMissing ||
    tokenMissing ||
    tokenError ||
    chatMissing ||
    hostMissing ||
    portMissing ||
    toMissing ||
    passwordError ||
    appIdMissing ||
    appSecretMissing ||
    appSecretError ||
    openidMissing
  )

  const set = (patch: Partial<ChannelConfig>) => setCfg((c) => ({ ...c, ...patch }))

  /** 扫码绑定回填：appSecret 填后端给的**引用名**（secretEnv），明文密钥永不经过前端。 */
  const handleBound = (result: QQBindFill) => {
    const { appId, secretEnv, userOpenid } = result
    set({ appId, appSecret: secretEnv, targetId: userOpenid, targetType: 'c2c' })
    setBindOpen(false)
    setBindFilled(true)
    onBindFilled?.()
  }

  const handleSubmit = async () => {
    if (hasError) return
    // qq 的 targetType 固定单聊：不暴露控件也不接受存量 group 值，提交前一律归一到 c2c。
    const config = channelIsQQ(type) ? { ...cfg, targetType: 'c2c' } : cfg
    await onSubmit({ name, type, enabled, config })
    onClose()
  }

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose() }}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-md`}>
        <DialogHeader>
          <DialogTitle>{isEdit ? t('alerts.editChannel') : t('alerts.createChannel')}</DialogTitle>
        </DialogHeader>

        <ScrollableDialogBody className="space-y-3">
          <div>
            <FieldLabel required>{t('alerts.channelName')}</FieldLabel>
            <Input
              className="mt-1"
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
              <Input
                className="mt-1 font-mono text-sm"
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
                <Input
                  className="mt-1 font-mono text-sm"
                  placeholder="${JM_TELEGRAM_TOKEN}"
                  value={cfg.token ?? ''}
                  aria-invalid={!!(tokenMissing || tokenError)}
                  onChange={(e) => set({ token: e.target.value })}
                />
                <FieldError error={tokenMissing || tokenError} />
                <p className="text-xs text-muted-foreground mt-1">{t('alerts.envRefHint')}</p>
              </div>
              <div>
                <FieldLabel required>{t('alerts.telegramChatId')}</FieldLabel>
                <Input
                  className="mt-1"
                  aria-invalid={!!chatMissing}
                  value={cfg.chatId ?? ''}
                  onChange={(e) => set({ chatId: e.target.value })}
                />
                <FieldError error={chatMissing} />
              </div>
            </>
          )}

          {channelIsEmail(type) && (
            <>
              <div className="grid grid-cols-3 gap-2">
                <div className="col-span-2">
                  <FieldLabel required>{t('alerts.smtpHost')}</FieldLabel>
                  <Input
                    className="mt-1"
                    aria-invalid={!!hostMissing}
                    value={cfg.host ?? ''}
                    onChange={(e) => set({ host: e.target.value })}
                  />
                  <FieldError error={hostMissing} />
                </div>
                <div>
                  <FieldLabel required>{t('alerts.smtpPort')}</FieldLabel>
                  <Input
                    type="number"
                    className="mt-1"
                    aria-invalid={!!portMissing}
                    placeholder="587"
                    value={cfg.port ?? ''}
                    onChange={(e) => set({ port: Number(e.target.value) })}
                  />
                  <FieldError error={portMissing} />
                </div>
              </div>
              <div>
                <FieldLabel>{t('alerts.smtpUsername')}</FieldLabel>
                <Input className="mt-1" value={cfg.username ?? ''} onChange={(e) => set({ username: e.target.value })} />
              </div>
              <div>
                <FieldLabel>{t('alerts.smtpPassword')}</FieldLabel>
                <Input
                  className="mt-1 font-mono text-sm"
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
                  <Input className="mt-1" value={cfg.from ?? ''} onChange={(e) => set({ from: e.target.value })} />
                </div>
                <div>
                  <FieldLabel required>{t('alerts.smtpTo')}</FieldLabel>
                  <Input
                    className="mt-1"
                    aria-invalid={!!toMissing}
                    placeholder="a@x.com, b@x.com"
                    value={cfg.to ?? ''}
                    onChange={(e) => set({ to: e.target.value })}
                  />
                  <FieldError error={toMissing} />
                </div>
              </div>
            </>
          )}

          {channelIsQQ(type) && (
            <>
              <p className="text-sm text-muted-foreground">{t('alerts.qqHint')}</p>

              {/* 分区一：机器人凭证——扫码一次即可全部拿到，故把入口放在该区标题栏。 */}
              <Panel
                title={t('alerts.qqSectionCredential')}
                icon={<KeyRound className="size-3.5" />}
                actions={
                  <Button type="button" variant="outline" size="xs" onClick={() => setBindOpen(true)}>
                    <QrCode className="size-3.5" />
                    {t('alerts.qqBind')}
                  </Button>
                }
                bodyClassName="space-y-3 p-3"
              >
                <p className="text-xs text-muted-foreground">{t('alerts.qqBindHint')}</p>
                <div className="grid gap-3 sm:grid-cols-2">
                  <div>
                    <FieldLabel required>{t('alerts.qqAppId')}</FieldLabel>
                    <Input
                      className="mt-1 font-mono text-sm"
                      aria-invalid={!!appIdMissing}
                      value={cfg.appId ?? ''}
                      onChange={(e) => set({ appId: e.target.value })}
                    />
                    <FieldError error={appIdMissing} />
                  </div>
                  <div>
                    <FieldLabel required>{t('alerts.qqAppSecret')}</FieldLabel>
                    <Input
                      className="mt-1 font-mono text-sm"
                      placeholder="${QQ-102000001}"
                      value={cfg.appSecret ?? ''}
                      aria-invalid={!!(appSecretMissing || appSecretError)}
                      onChange={(e) => {
                        const v = e.target.value
                        set({ appSecret: v })
                      }}
                    />
                    <FieldError error={appSecretMissing || appSecretError} />
                  </div>
                </div>
                {bindFilled && (
                  <p className="text-xs text-emerald-600 dark:text-emerald-400" role="status">
                    {t('alerts.qqBindDone')}
                  </p>
                )}
              </Panel>

              {/* 分区二：投递目标——目标类型固定单聊（群主动消息被平台拒绝），故只读展示。 */}
              <Panel
                title={t('alerts.qqSectionTarget')}
                icon={<Send className="size-3.5" />}
                bodyClassName="space-y-3 p-3"
              >
                <div>
                  <FieldLabel>{t('alerts.qqTargetType')}</FieldLabel>
                  <p className="mt-1 text-sm text-muted-foreground">{t('alerts.qqTargetC2cOnly')}</p>
                  {legacyGroupTarget && (
                    <p className="text-xs text-amber-600 dark:text-amber-500 mt-1">{t('alerts.qqLegacyGroup')}</p>
                  )}
                </div>
                <div>
                  <FieldLabel required>{t('alerts.qqTargetId')}</FieldLabel>
                  <Input
                    className="mt-1 font-mono text-sm"
                    aria-invalid={!!openidMissing}
                    value={cfg.targetId ?? ''}
                    onChange={(e) => set({ targetId: e.target.value })}
                  />
                  <FieldError error={openidMissing} />
                  <p className="text-xs text-muted-foreground mt-1">{t('alerts.qqTargetIdHint')}</p>
                </div>
              </Panel>

              {/* 分区三：高级——API 根地址留空即取后端默认值，多数场景无需改动。 */}
              <Panel
                title={t('alerts.qqSectionAdvanced')}
                icon={<SlidersHorizontal className="size-3.5" />}
                bodyClassName="p-3"
              >
                <FieldLabel>{t('alerts.qqBaseUrl')}</FieldLabel>
                <Input
                  className="mt-1 font-mono text-sm"
                  placeholder="https://api.bot.qq.com"
                  value={cfg.baseUrl ?? ''}
                  onChange={(e) => set({ baseUrl: e.target.value })}
                />
                <p className="text-xs text-muted-foreground mt-1">{t('alerts.qqBaseUrlHint')}</p>
              </Panel>
            </>
          )}

          <label className="flex items-center gap-2 text-sm">
            <Checkbox checked={enabled} onCheckedChange={(v) => setEnabled(v === true)} aria-label={t('alerts.enabled')} />
            {t('alerts.enabled')}
          </label>
        </ScrollableDialogBody>

        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            type="button"
            disabled={hasError || submitting}
            onClick={handleSubmit}
          >
            {t('common.save')}
          </Button>
        </DialogFooter>
      </DialogContent>

      {/* 扫码绑定弹窗：嵌在通道对话框内，扫码结果直接回填本表单，用户不必去别处抄值。 */}
      {bindOpen && renderBindDialog?.({ onClose: () => setBindOpen(false), onBound: handleBound })}
    </Dialog>
  )
}
