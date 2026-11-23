import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  ScrollableDialogBody,
  scrollableDialogContentClass,
} from '@jianmanager/ui/components/scrollable-dialog'
import type { ClientKeyWithSecret, ClientPullKey } from '@/lib/client-dist/client-channel-types'

/**
 * ISO 时间串转 `<input type="datetime-local">` 可用的值；空值转空串。
 *
 * 【为什么不能直接 toISOString().slice(0,16)】
 * `<input type="datetime-local">` 的值是**本地时间**（不带时区），而 toISOString 输出的是 UTC。
 * 两者直接对接的结果是：UTC+8 用户看到框里写着 12:00（实际是 UTC 12:00 = 本地 20:00），
 * 不改动直接保存时又按本地 12:00 重新解析 → 存成 UTC 04:00，**过期时间莫名提前 8 小时**。
 * 负时区则相反（延长有效期），而且每编辑一次就再偏一次。
 *
 * 所以这里必须用本地时间分量拼字符串；保存侧（new Date(localValue)）本来就是按本地解析，
 * 两侧口径一致后，"只改名字不动时间"才是真的不改。
 */
const toDatetimeLocal = (value: string | null) => {
  if (!value) return ''
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 创建密钥请求体。 */
export interface CreateKeyBody {
  channelId: string
  name: string
  expiresAt?: string
  value?: string
}

/** 创建密钥模态的容器注入点。 */
export interface CreateKeyDialogProps {
  channelId: string
  open: boolean
  onOpenChange: (v: boolean) => void
  onCreated: (secret: ClientKeyWithSecret) => void
  /** 提交创建（容器注入 mutation）；返回含一次性明文的新密钥。 */
  onCreate: (body: CreateKeyBody) => Promise<ClientKeyWithSecret>
  /** 提交中（禁用提交按钮）。 */
  submitting?: boolean
  /** 结果回执（容器注入 toast；组件库不依赖 toast 实现）。 */
  onNotify?: (level: 'success' | 'error', message: string) => void
}

/** 更新密钥请求体。 */
export interface UpdateKeyBody {
  channelId: string
  keyId: number
  name: string
  value?: string
  expiresAt: string | null
}

/** 编辑密钥两件的容器注入点。 */
export interface EditKeyDialogProps {
  channelId: string
  target: ClientPullKey | null
  onOpenChange: (v: boolean) => void
  onUpdated: (secret: ClientKeyWithSecret) => void
  /** 提交更新（容器注入 mutation）；返回含新明文的结果。 */
  onUpdate: (body: UpdateKeyBody) => Promise<ClientKeyWithSecret>
  /** 提交中（禁用保存按钮）。 */
  submitting?: boolean
  /** 结果回执（容器注入 toast）。 */
  onNotify?: (level: 'success' | 'error', message: string) => void
}

/** 编辑表单入参：target 必非空（由外层壳保证）。 */
type EditKeyFormProps = Omit<EditKeyDialogProps, 'target'> & { target: ClientPullKey }
/** 创建拉取密钥模态（取代原内联常驻表单；内容自适应壳）。 */
export function CreateKeyDialog({
  channelId,
  open,
  onOpenChange,
  onCreated,
  onCreate,
  submitting = false,
  onNotify,
}: CreateKeyDialogProps) {
  const { t } = useTranslation()
  const [keyName, setKeyName] = useState('')
  const [expiresAt, setExpiresAt] = useState('')
  // 自定义密钥值（可空=自动生成）。：管理员可自控这把永久 key。
  const [customValue, setCustomValue] = useState('')

  const canSubmit = keyName.trim() !== '' && !submitting

  const reset = () => {
    setKeyName('')
    setExpiresAt('')
    setCustomValue('')
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!canSubmit) return
    try {
      const res = await onCreate({
        channelId,
        name: keyName,
        expiresAt: expiresAt ? new Date(expiresAt).toISOString() : undefined,
        value: customValue.trim() || undefined,
      })
      reset()
      onOpenChange(false)
      onCreated(res)
    } catch (err) {
      const message =
        (err as { response?: { data?: { message?: string } } })?.response?.data?.message ||
        t('clientChannels.keyCreateFailed', '创建密钥失败')
      onNotify?.('error', message)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v: boolean) => {
        if (!v) reset()
        onOpenChange(v)
      }}
    >
      <DialogContent className={cn(scrollableDialogContentClass, 'sm:max-w-md')}>
        <DialogHeader>
          <DialogTitle>{t('clientChannels.createKey', '创建密钥')}</DialogTitle>
          <DialogDescription>
            {t('clientChannels.createKeyDescViewable', '密钥发出后永久使用；创建后可随时查看明文。留空密钥值则自动生成。')}
          </DialogDescription>
        </DialogHeader>
        <form id="create-key-form" onSubmit={submit}>
          <ScrollableDialogBody className="space-y-3">
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.keyName', '密钥名称')}
              <input
                className="p-2 border rounded bg-background"
                placeholder={t('clientChannels.keyNamePlaceholder', '如：正式包 / 灰度')}
                value={keyName}
                onChange={(e) => setKeyName(e.target.value)}
                autoFocus
              />
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.keyValue', '密钥值（可选）')}
              <input
                className="p-2 border rounded bg-background font-mono"
                placeholder={t('clientChannels.keyValuePlaceholder', '留空自动生成；可填自定义值')}
                value={customValue}
                onChange={(e) => setCustomValue(e.target.value)}
              />
              <span className="text-xs text-muted-foreground">
                {t('clientChannels.keyValueHint', '自定义则用作明文；可随时查看/编辑。')}
              </span>
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.expiresAt', '过期时间（可选）')}
              <input
                type="datetime-local"
                className="p-2 border rounded bg-background"
                value={expiresAt}
                onChange={(e) => setExpiresAt(e.target.value)}
              />
              <span className="text-xs text-muted-foreground">
                {t('clientChannels.neverExpiresHint', '留空表示永不过期。')}
              </span>
            </label>
          </ScrollableDialogBody>
        </form>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel', '取消')}
          </Button>
          <Button type="submit" form="create-key-form" disabled={!canSubmit}>
            {t('clientChannels.createKey', '创建密钥')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** 
 * 编辑拉取密钥模态：改名 + 可选改值。
 * 改值会重算鉴权哈希、使持旧值的已分发客户端失效——表单内强警告。改值后回显新明文供复制。
 * 外层壳：target 存在时以 key=target.id 挂载内部表单，使其状态随目标重新初始化（避免 effect 改状态）。
 */
export function EditKeyDialog({
  channelId,
  target,
  onOpenChange,
  onUpdated,
  onUpdate,
  submitting = false,
  onNotify,
}: EditKeyDialogProps) {
  if (!target) return null
  return (
    <EditKeyForm
      key={target.id}
      channelId={channelId}
      target={target}
      onOpenChange={onOpenChange}
      onUpdated={onUpdated}
      onUpdate={onUpdate}
      submitting={submitting}
      onNotify={onNotify}
    />
  )
}

/** 编辑密钥表单（内部组件，target 非空；状态由 props 初始化，挂载即新表单）。 */
function EditKeyForm({
  channelId,
  target,
  onOpenChange,
  onUpdated,
  onUpdate,
  submitting = false,
  onNotify,
}: EditKeyFormProps) {
  const { t } = useTranslation()
  const [name, setName] = useState(target.name)
  // 值不回显既有明文，留空=不改值。
  const [value, setValue] = useState('')
  const [expiresAt, setExpiresAt] = useState(toDatetimeLocal(target.expiresAt))

  const canSubmit = name.trim() !== '' && !submitting

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!canSubmit) return
    try {
      const res = await onUpdate({
        channelId,
        keyId: target.id,
        name: name.trim(),
        value: value.trim() || undefined,
        expiresAt: expiresAt ? new Date(expiresAt).toISOString() : null,
      })
      onNotify?.('success', t('clientChannels.keyUpdated', '密钥已更新'))
      onOpenChange(false)
      onUpdated(res)
    } catch (err) {
      const message =
        (err as { response?: { data?: { message?: string } } })?.response?.data?.message ||
        t('clientChannels.keyUpdateFailed', '更新密钥失败')
      onNotify?.('error', message)
    }
  }

  return (
    <Dialog open onOpenChange={(v: boolean) => onOpenChange(v)}>
      <DialogContent className={cn(scrollableDialogContentClass, 'sm:max-w-md')}>
        <DialogHeader>
          <DialogTitle>{t('clientChannels.editKey', '编辑密钥')}</DialogTitle>
          <DialogDescription>
            {t('clientChannels.editKeyDesc', '修改名称或密钥值。留空密钥值仅改名；填入则改值。')}
          </DialogDescription>
        </DialogHeader>
        <form id="edit-key-form" onSubmit={submit}>
          <ScrollableDialogBody className="space-y-3">
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.keyName', '密钥名称')}
              <input
                className="p-2 border rounded bg-background"
                value={name}
                onChange={(e) => setName(e.target.value)}
                autoFocus
              />
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.keyNewValue', '新密钥值（可选）')}
              <input
                className="p-2 border rounded bg-background font-mono"
                placeholder={t('clientChannels.keyNewValuePlaceholder', '留空则不改值，仅改名')}
                value={value}
                onChange={(e) => setValue(e.target.value)}
              />
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.expiresAt', '过期时间（可选）')}
              <input
                type="datetime-local"
                className="p-2 border rounded bg-background"
                value={expiresAt}
                onChange={(e) => setExpiresAt(e.target.value)}
              />
              <span className="text-xs text-muted-foreground">
                {t('clientChannels.neverExpiresHint', '留空表示永不过期。')}
              </span>
            </label>
            {value.trim() !== '' && (
              // 【为什么不用 Alert】这是**输入联动提示**，不是页面级告警：它随「新密钥值」这一格非空即时出现、
              // 清空即消失，语义是"你正在改的这格有副作用"，紧贴该输入框才是正确位置。
              // 若换成 Alert destructive 档，底色从 bg-destructive/5 变成 bg-card、描边从红变灰，
              // 在弹窗表单内部会显得比周围输入框还淡，反而削弱提醒。
              // `/5` 淡底同样有意：它在整个编辑过程中持续可见（只要输入非空），满浓度红底会压过保存按钮。
              <p className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-xs text-destructive">
                {t(
                  'clientChannels.editKeyValueWarn',
                  '改值后旧值立即失效：持旧值的已分发客户端将无法再更新，需把新值下发给玩家。请确认确需更换。',
                )}
              </p>
            )}
          </ScrollableDialogBody>
        </form>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel', '取消')}
          </Button>
          <Button type="submit" form="edit-key-form" disabled={!canSubmit}>
            {t('common.save', '保存')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
