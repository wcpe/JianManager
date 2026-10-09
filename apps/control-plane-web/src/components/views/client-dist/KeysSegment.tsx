import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Ban, Eye, Pencil, Plus } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import DangerConfirm from '@/components/views/DangerConfirm'
import type { ClientKeyWithSecret, ClientPullKey } from '@/lib/client-dist/client-channel-types'
import { CreateKeyDialog, EditKeyDialog } from '@/components/views/client-dist/KeyEditDialogs'
import type { CreateKeyBody, UpdateKeyBody } from '@/components/views/client-dist/KeyEditDialogs'
import { RevealDialog, SecretDialog } from '@/components/views/client-dist/KeySecretDialogs'

/** 「即将过期」判定窗口：7 天。 */
const KEY_EXPIRING_SOON_MS = 7 * 24 * 60 * 60 * 1000

/** 过期时间展示；空值回落 neverLabel。 */
const formatKeyExpiresAt = (value: string | null, neverLabel: string) =>
  value ? new Date(value).toLocaleString() : neverLabel

/** 密钥有效期状态：none（无期限）/ expired / expiring（7 天内）/ active。 */
const keyExpiryState = (value: string | null, now = Date.now()): 'none' | 'expired' | 'expiring' | 'active' => {
  if (!value) return 'none'
  const expiresAt = new Date(value).getTime()
  if (Number.isNaN(expiresAt)) return 'none'
  if (expiresAt <= now) return 'expired'
  if (expiresAt - now <= KEY_EXPIRING_SOON_MS) return 'expiring'
  return 'active'
}

export interface KeysSegmentProps {
  channelId: string
  keys: ClientPullKey[]
  loading: boolean
  createOpen: boolean
  onCreateOpenChange: (v: boolean) => void
  /** 查看明文（容器注入 mutation）；返回密钥名与一次性明文。 */
  onReveal: (key: ClientPullKey) => Promise<{ key: string }>
  /** 吊销密钥（容器注入 mutation）。 */
  onRevoke: (key: ClientPullKey) => Promise<void>
  /** 创建密钥（透传给内部创建模态）。 */
  onCreateKey: (body: CreateKeyBody) => Promise<ClientKeyWithSecret>
  /** 更新密钥（透传给内部编辑模态）。 */
  onUpdateKey: (body: UpdateKeyBody) => Promise<ClientKeyWithSecret>
  /** 创建/更新进行中（透传禁用态）。 */
  keyMutating?: boolean
  /** 查看明文进行中（透传禁用态）。 */
  revealing?: boolean
  /** 危险操作（吊销）是否放行：应用侧读角色等级后注入（组件库不持鉴权状态）。 */
  dangerAllowed?: boolean
  /** 结果回执（容器注入 toast；组件库不依赖 toast 实现）。 */
  onNotify?: (level: 'success' | 'error', message: string) => void
}

/** 密钥分段：列表 + 「创建密钥」模态 + 查看/编辑/吊销（DangerConfirm）+ 一次性明文弹窗。 */
export function KeysSegment({
  channelId,
  keys,
  loading,
  createOpen,
  onCreateOpenChange,
  onReveal,
  onRevoke,
  onCreateKey,
  onUpdateKey,
  keyMutating = false,
  revealing = false,
  dangerAllowed,
  onNotify,
}: KeysSegmentProps) {
  const { t } = useTranslation()
  /** 结果回执统一出口（容器注入 toast）。 */
  const notify = (level: 'success' | 'error', message: string) => onNotify?.(level, message)

  const [secret, setSecret] = useState<ClientKeyWithSecret | null>(null)
  const [revokeTarget, setRevokeTarget] = useState<ClientPullKey | null>(null)
  const [editTarget, setEditTarget] = useState<ClientPullKey | null>(null)
  // 查看明文弹窗（FR-192）：保存当前查看到的密钥名 + 明文。
  const [revealed, setRevealed] = useState<{ name: string; key: string } | null>(null)

  const doReveal = async (key: ClientPullKey) => {
    if (!key.revealable) {
      notify('error', t('clientChannels.notRevealable'))
      return
    }
    try {
      const res = await onReveal(key)
      setRevealed({ name: key.name, key: res.key })
    } catch (e) {
      // 兜底：后端返 KEY_NOT_REVEALABLE（如并发改值/降级）也走不可找回提示。
      const code = (e as { response?: { data?: { error?: string } } })?.response?.data?.error
      if (code === 'KEY_NOT_REVEALABLE') notify('error', t('clientChannels.notRevealable'))
      else {
        const message =
          (e as { response?: { data?: { message?: string } } })?.response?.data?.message ||
          t('clientChannels.revealFailed', '查看密钥失败')
        notify('error', message)
      }
    }
  }

  const doRevoke = async (key: ClientPullKey) => {
    setRevokeTarget(null)
    try {
      await onRevoke(key)
      notify('success', t('clientChannels.revoked', '密钥已吊销'))
    } catch (e) {
      const message =
        (e as { response?: { data?: { message?: string } } })?.response?.data?.message ||
        t('clientChannels.revokeFailed', '吊销密钥失败')
      notify('error', message)
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <p className="text-sm text-muted-foreground max-w-2xl">
          {t(
            'clientChannels.keysSubtitleViewable',
            '拉取密钥以可逆加密存储，发出后永久使用、可随时查看明文并复制到 jm-updater.json。可编辑密钥值（改值会使持旧值的已分发客户端失效）。',
          )}
        </p>
        <Button onClick={() => onCreateOpenChange(true)} className="shrink-0">
          <Plus className="size-4" /> {t('clientChannels.createKey', '创建密钥')}
        </Button>
      </div>

      <div className="overflow-hidden rounded-lg border">
        <Table>
          <TableHeader className="bg-muted/50">
            <TableRow>
              <TableHead>{t('common.name', '名称')}</TableHead>
              <TableHead>{t('clientChannels.keyPrefix', '前缀')}</TableHead>
              <TableHead>{t('common.status', '状态')}</TableHead>
              <TableHead>{t('clientChannels.expiresAt', '过期时间')}</TableHead>
              <TableHead>{t('clientChannels.lastUsed', '最近使用')}</TableHead>
              <TableHead className="text-right">{t('common.actions', '操作')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {keys.map((k) => {
              const expiryState = keyExpiryState(k.expiresAt)
              return (
                <TableRow key={k.id}>
                  <TableCell className="font-medium">{k.name}</TableCell>
                  <TableCell className="font-mono text-xs">{k.keyPrefix}…</TableCell>
                  <TableCell>
                    {k.revoked ? (
                      <Badge variant="outline" className="border-destructive/40 text-destructive">
                        {t('clientChannels.statusRevoked', '已吊销')}
                      </Badge>
                    ) : (
                      <Badge variant="outline">{t('clientChannels.statusActive', '有效')}</Badge>
                    )}
                  </TableCell>
                  <TableCell className="text-xs">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span>{formatKeyExpiresAt(k.expiresAt, t('clientChannels.neverExpires', '永不过期'))}</span>
                      {expiryState === 'expired' && (
                        <Badge variant="outline" className="border-status-danger/50 bg-status-danger/10 text-status-danger">
                          {t('clientChannels.expired', '已过期')}
                        </Badge>
                      )}
                      {expiryState === 'expiring' && (
                        <Badge variant="outline" className="border-status-warning/50 bg-status-warning/10 text-status-warning">
                          {t('clientChannels.expiringSoon', '即将过期')}
                        </Badge>
                      )}
                    </div>
                  </TableCell>
                  <TableCell className="text-xs">{k.lastUsedAt ? new Date(k.lastUsedAt).toLocaleString() : '-'}</TableCell>
                  <TableCell>
                    <div className="flex justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="xs"
                        onClick={() => doReveal(k)}
                        disabled={!k.revealable || revealing}
                        title={k.revealable ? undefined : t('clientChannels.notRevealable')}
                      >
                        <Eye className="size-3.5" /> {t('clientChannels.reveal', '查看')}
                      </Button>
                      <Button variant="ghost" size="xs" onClick={() => setEditTarget(k)} disabled={k.revoked}>
                        <Pencil className="size-3.5" /> {t('common.edit', '编辑')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        className="text-status-danger hover:text-status-danger"
                        onClick={() => setRevokeTarget(k)}
                        disabled={k.revoked}
                      >
                        <Ban className="size-3.5" /> {t('clientChannels.revoke', '吊销')}
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              )
            })}
            {keys.length === 0 && !loading && (
              <TableRow>
                <TableCell colSpan={6} className="h-16 text-center text-muted-foreground">
                  {t('clientChannels.noKeys', '暂无密钥')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>

      <CreateKeyDialog
        channelId={channelId}
        open={createOpen}
        onOpenChange={onCreateOpenChange}
        onCreated={(s) => setSecret(s)}
        onCreate={onCreateKey}
        submitting={keyMutating}
        onNotify={onNotify}
      />

      <EditKeyDialog
        channelId={channelId}
        target={editTarget}
        onOpenChange={(v) => !v && setEditTarget(null)}
        onUpdated={(s) => {
          // 改了值才回显新明文弹窗（后端 key 非空表示改了值）；仅改名不弹。
          if (s.key) setSecret(s)
        }}
        onUpdate={onUpdateKey}
        submitting={keyMutating}
        onNotify={onNotify}
      />

      <SecretDialog secret={secret} onClose={() => setSecret(null)} onNotify={onNotify} />

      <RevealDialog revealed={revealed} onClose={() => setRevealed(null)} onNotify={onNotify} />

      <DangerConfirm
        open={revokeTarget !== null}
        title={t('clientChannels.revokeConfirm', '确定吊销此密钥？')}
        description={t(
          'clientChannels.revokeConfirmDesc',
          '吊销不可恢复：使用此密钥的已分发客户端将无法再更新（拉取 manifest/制品一律被拒）。仅在确认该密钥不再服务于任何已发出的整合包时吊销。',
        )}
        scope="platform"
        allowed={dangerAllowed}
        confirmLabel={t('clientChannels.revoke', '吊销')}
        onConfirm={() => revokeTarget && doRevoke(revokeTarget)}
        onCancel={() => setRevokeTarget(null)}
      />
    </div>
  )
}

