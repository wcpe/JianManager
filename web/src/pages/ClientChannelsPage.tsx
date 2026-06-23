import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Copy, KeyRound, RefreshCw, Ban, ChevronLeft } from 'lucide-react'
import {
  useClientChannels,
  useClientChannel,
  useCreateClientChannel,
  useDeleteClientChannel,
  useCreateClientKey,
  useRotateClientKey,
  useRevokeClientKey,
  type ClientChannel,
  type ClientPullKey,
  type ClientKeyWithSecret,
} from '@/api/clientChannels'
import { Badge } from '@/components/ui/badge'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import DangerConfirm from '@/components/DangerConfirm'

type ErrResp = { response?: { data?: { message?: string } } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

/**
 * 客户端分发管理页（FR-086，见 ADR-022）。
 * 频道（每服一个）+ 拉取密钥（落库只存哈希、明文一次性返回）。仅平台管理员可用（后端 RBAC 强制）。
 * i18n（FR-016）+ 暗/亮色（FR-026，全程用主题 token）。
 */
export default function ClientChannelsPage() {
  const { t } = useTranslation()
  const { data: channels, isLoading } = useClientChannels()
  const [selected, setSelected] = useState<string | null>(null)

  if (selected) {
    return <ChannelDetail channelId={selected} onBack={() => setSelected(null)} />
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold">{t('clientChannels.title', '客户端分发')}</h1>
        <p className="text-sm text-muted-foreground mt-1 max-w-2xl">
          {t('clientChannels.subtitle', '管理客户端分发频道与拉取密钥。每服一个频道，密钥用于玩家侧更新器拉取。')}
        </p>
      </div>

      <CreateChannelForm />

      <div className="border rounded-lg overflow-hidden">
        <table className="w-full">
          <thead className="bg-muted">
            <tr>
              <th className="p-3 text-left">{t('clientChannels.channelId', '频道标识')}</th>
              <th className="p-3 text-left">{t('common.name', '名称')}</th>
              <th className="p-3 text-left">{t('clientChannels.currentVersion', '当前版本')}</th>
              <th className="p-3 text-left">{t('clientChannels.keyCount', '密钥数')}</th>
              <th className="p-3 text-left">{t('common.actions', '操作')}</th>
            </tr>
          </thead>
          <tbody>
            {(channels ?? []).map((ch: ClientChannel) => (
              <tr key={ch.id} className="border-t">
                <td className="p-3 font-mono text-xs">{ch.channelId}</td>
                <td className="p-3">{ch.name}</td>
                <td className="p-3">{ch.currentVersion > 0 ? `v${ch.currentVersion}` : t('clientChannels.unpublished', '未发布')}</td>
                <td className="p-3">{ch.keyCount ?? 0}</td>
                <td className="p-3">
                  <button className="text-primary hover:underline" onClick={() => setSelected(ch.channelId)}>
                    {t('clientChannels.manageKeys', '管理密钥')}
                  </button>
                </td>
              </tr>
            ))}
            {(!channels || channels.length === 0) && !isLoading && (
              <tr>
                <td colSpan={5} className="p-3 text-center text-muted-foreground">
                  {t('clientChannels.empty', '暂无频道')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}

/** 创建频道内联表单。 */
function CreateChannelForm() {
  const { t } = useTranslation()
  const create = useCreateClientChannel()
  const [show, setShow] = useState(false)
  const [channelId, setChannelId] = useState('')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')

  const slugOk = /^[a-z0-9][a-z0-9-]{1,63}$/.test(channelId)
  const canSubmit = slugOk && name.trim() !== '' && !create.isPending

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!canSubmit) return
    try {
      await create.mutateAsync({ channelId, name, description })
      toast.success(t('clientChannels.created', '频道已创建'))
      setChannelId('')
      setName('')
      setDescription('')
      setShow(false)
    } catch (e) {
      toast.error(errMsg(e, t('clientChannels.createFailed', '创建频道失败')))
    }
  }

  if (!show) {
    return (
      <button
        className="px-4 py-2 bg-primary text-primary-foreground rounded-md hover:bg-primary/90"
        onClick={() => setShow(true)}
      >
        {t('clientChannels.addChannel', '新增频道')}
      </button>
    )
  }

  return (
    <form onSubmit={submit} className="border rounded-lg p-4 grid grid-cols-1 md:grid-cols-2 gap-3">
      <label className="flex flex-col gap-1 text-sm">
        {t('clientChannels.channelId', '频道标识')}
        <input
          className="p-2 border rounded bg-background font-mono aria-invalid:border-destructive"
          placeholder="skyblock-s1"
          aria-invalid={channelId !== '' && !slugOk}
          value={channelId}
          onChange={(e) => setChannelId(e.target.value)}
        />
        <span className="text-xs text-muted-foreground">{t('clientChannels.channelIdHint', '小写字母/数字/连字符，2-64 位，创建后不可改')}</span>
      </label>
      <label className="flex flex-col gap-1 text-sm">
        {t('common.name', '名称')}
        <input
          className="p-2 border rounded bg-background"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </label>
      <label className="flex flex-col gap-1 text-sm md:col-span-2">
        {t('clientChannels.description', '描述')}
        <input
          className="p-2 border rounded bg-background"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </label>
      <div className="md:col-span-2 flex gap-2 justify-end">
        <button type="button" className="px-4 py-2 border rounded-md hover:bg-accent" onClick={() => setShow(false)}>
          {t('common.cancel', '取消')}
        </button>
        <button
          type="submit"
          className="px-4 py-2 bg-primary text-primary-foreground rounded-md hover:bg-primary/90 disabled:opacity-50"
          disabled={!canSubmit}
        >
          {t('common.create', '创建')}
        </button>
      </div>
    </form>
  )
}

/** 频道详情：密钥列表 + 创建/轮换/吊销，附一次性明文展示弹窗。 */
function ChannelDetail({ channelId, onBack }: { channelId: string; onBack: () => void }) {
  const { t } = useTranslation()
  const { data: detail, isLoading } = useClientChannel(channelId)
  const del = useDeleteClientChannel()
  const createKey = useCreateClientKey()
  const rotateKey = useRotateClientKey()
  const revokeKey = useRevokeClientKey()

  const [keyName, setKeyName] = useState('')
  const [expiresAt, setExpiresAt] = useState('')
  const [secret, setSecret] = useState<ClientKeyWithSecret | null>(null)
  const [revokeTarget, setRevokeTarget] = useState<ClientPullKey | null>(null)
  const [rotateTarget, setRotateTarget] = useState<ClientPullKey | null>(null)
  const [deleteChannel, setDeleteChannel] = useState(false)

  const submitKey = async (e: FormEvent) => {
    e.preventDefault()
    if (keyName.trim() === '' || createKey.isPending) return
    try {
      const res = await createKey.mutateAsync({
        channelId,
        name: keyName,
        expiresAt: expiresAt ? new Date(expiresAt).toISOString() : undefined,
      })
      setSecret(res)
      setKeyName('')
      setExpiresAt('')
    } catch (e) {
      toast.error(errMsg(e, t('clientChannels.keyCreateFailed', '创建密钥失败')))
    }
  }

  const doRotate = async (key: ClientPullKey) => {
    setRotateTarget(null)
    try {
      const res = await rotateKey.mutateAsync({ channelId, keyId: key.id })
      setSecret(res)
      toast.success(t('clientChannels.rotated', '密钥已轮换'))
    } catch (e) {
      toast.error(errMsg(e, t('clientChannels.rotateFailed', '轮换密钥失败')))
    }
  }

  const doRevoke = (key: ClientPullKey) => {
    setRevokeTarget(null)
    revokeKey.mutate(
      { channelId, keyId: key.id },
      {
        onSuccess: () => toast.success(t('clientChannels.revoked', '密钥已吊销')),
        onError: (e) => toast.error(errMsg(e, t('clientChannels.revokeFailed', '吊销密钥失败'))),
      },
    )
  }

  const doDeleteChannel = () => {
    setDeleteChannel(false)
    del.mutate(channelId, {
      onSuccess: () => {
        toast.success(t('clientChannels.channelDeleted', '频道已删除'))
        onBack()
      },
      onError: (e) => toast.error(errMsg(e, t('clientChannels.deleteFailed', '删除频道失败'))),
    })
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <div className="flex items-center gap-2">
          <button className="text-muted-foreground hover:text-foreground" onClick={onBack} aria-label={t('common.back', '返回')}>
            <ChevronLeft className="size-5" />
          </button>
          <div>
            <h1 className="text-2xl font-bold flex items-center gap-2">
              <KeyRound className="size-5" /> {detail?.name ?? channelId}
            </h1>
            <p className="text-xs text-muted-foreground font-mono mt-1">{channelId}</p>
          </div>
        </div>
        <button className="text-destructive hover:underline text-sm" onClick={() => setDeleteChannel(true)}>
          {t('clientChannels.deleteChannel', '删除频道')}
        </button>
      </div>

      <form onSubmit={submitKey} className="border rounded-lg p-4 flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1 text-sm flex-1 min-w-[12rem]">
          {t('clientChannels.keyName', '密钥名称')}
          <input
            className="p-2 border rounded bg-background"
            placeholder={t('clientChannels.keyNamePlaceholder', '如：正式包 / 灰度')}
            value={keyName}
            onChange={(e) => setKeyName(e.target.value)}
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
        </label>
        <button
          type="submit"
          className="px-4 py-2 bg-primary text-primary-foreground rounded-md hover:bg-primary/90 disabled:opacity-50"
          disabled={keyName.trim() === '' || createKey.isPending}
        >
          {t('clientChannels.createKey', '创建密钥')}
        </button>
      </form>

      <div className="border rounded-lg overflow-hidden">
        <table className="w-full">
          <thead className="bg-muted">
            <tr>
              <th className="p-3 text-left">{t('common.name', '名称')}</th>
              <th className="p-3 text-left">{t('clientChannels.keyPrefix', '前缀')}</th>
              <th className="p-3 text-left">{t('common.status', '状态')}</th>
              <th className="p-3 text-left">{t('clientChannels.expiresAt', '过期时间')}</th>
              <th className="p-3 text-left">{t('clientChannels.lastUsed', '最近使用')}</th>
              <th className="p-3 text-left">{t('common.actions', '操作')}</th>
            </tr>
          </thead>
          <tbody>
            {(detail?.keys ?? []).map((k) => (
              <tr key={k.id} className="border-t">
                <td className="p-3">{k.name}</td>
                <td className="p-3 font-mono text-xs">{k.keyPrefix}…</td>
                <td className="p-3">
                  {k.revoked ? (
                    <Badge variant="outline" className="text-destructive border-destructive/40">{t('clientChannels.statusRevoked', '已吊销')}</Badge>
                  ) : (
                    <Badge variant="outline">{t('clientChannels.statusActive', '有效')}</Badge>
                  )}
                </td>
                <td className="p-3 text-xs">{k.expiresAt ? new Date(k.expiresAt).toLocaleString() : '-'}</td>
                <td className="p-3 text-xs">{k.lastUsedAt ? new Date(k.lastUsedAt).toLocaleString() : '-'}</td>
                <td className="p-3">
                  <div className="flex gap-3">
                    <button
                      className="text-primary hover:underline inline-flex items-center gap-1 disabled:opacity-40"
                      onClick={() => setRotateTarget(k)}
                      disabled={k.revoked}
                    >
                      <RefreshCw className="size-3.5" /> {t('clientChannels.rotate', '轮换')}
                    </button>
                    <button
                      className="text-destructive hover:underline inline-flex items-center gap-1 disabled:opacity-40"
                      onClick={() => setRevokeTarget(k)}
                      disabled={k.revoked}
                    >
                      <Ban className="size-3.5" /> {t('clientChannels.revoke', '吊销')}
                    </button>
                  </div>
                </td>
              </tr>
            ))}
            {(!detail?.keys || detail.keys.length === 0) && !isLoading && (
              <tr>
                <td colSpan={6} className="p-3 text-center text-muted-foreground">
                  {t('clientChannels.noKeys', '暂无密钥')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <SecretDialog secret={secret} onClose={() => setSecret(null)} />

      <DangerConfirm
        open={rotateTarget !== null}
        title={t('clientChannels.rotateConfirm', '确定轮换此密钥？')}
        description={t('clientChannels.rotateConfirmDesc', '轮换后旧密钥立即失效，已分发的客户端需更新为新密钥。')}
        scope="platform"
        confirmLabel={t('clientChannels.rotate', '轮换')}
        onConfirm={() => rotateTarget && doRotate(rotateTarget)}
        onCancel={() => setRotateTarget(null)}
      />

      <DangerConfirm
        open={revokeTarget !== null}
        title={t('clientChannels.revokeConfirm', '确定吊销此密钥？')}
        description={t('clientChannels.revokeConfirmDesc', '吊销后使用此密钥的客户端将无法再拉取更新。')}
        scope="platform"
        confirmLabel={t('clientChannels.revoke', '吊销')}
        onConfirm={() => revokeTarget && doRevoke(revokeTarget)}
        onCancel={() => setRevokeTarget(null)}
      />

      <DangerConfirm
        open={deleteChannel}
        title={t('clientChannels.deleteChannelConfirm', '确定删除此频道？')}
        description={t('clientChannels.deleteChannelDesc', '将连同其全部拉取密钥一并删除，不可恢复。')}
        scope="platform"
        confirmText={channelId}
        confirmLabel={t('common.delete', '删除')}
        onConfirm={doDeleteChannel}
        onCancel={() => setDeleteChannel(false)}
      />
    </div>
  )
}

/** 一次性明文展示弹窗：仅创建/轮换后可见，提示复制保存。 */
function SecretDialog({ secret, onClose }: { secret: ClientKeyWithSecret | null; onClose: () => void }) {
  const { t } = useTranslation()

  const copy = async () => {
    if (!secret) return
    try {
      await navigator.clipboard.writeText(secret.key)
      toast.success(t('clientChannels.copied', '已复制到剪贴板'))
    } catch {
      toast.error(t('clientChannels.copyFailed', '复制失败，请手动选择复制'))
    }
  }

  return (
    <Dialog open={secret !== null} onOpenChange={(v: boolean) => { if (!v) onClose() }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('clientChannels.secretTitle', '拉取密钥（仅此一次）')}</DialogTitle>
          <DialogDescription>
            {t('clientChannels.secretDesc', '请立即复制保存。关闭后将无法再次查看完整密钥。')}
          </DialogDescription>
        </DialogHeader>
        <div className="flex items-center gap-2 rounded-md border bg-muted/50 p-3">
          <code className="flex-1 break-all font-mono text-sm">{secret?.key}</code>
          <Button variant="outline" size="sm" onClick={copy} className="shrink-0">
            <Copy className="size-4" /> {t('clientChannels.copy', '复制')}
          </Button>
        </div>
        <DialogFooter>
          <Button onClick={onClose}>{t('common.close', '关闭')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
