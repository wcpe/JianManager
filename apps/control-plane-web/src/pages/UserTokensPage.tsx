import { useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Copy, KeyRound, Plus, Terminal } from 'lucide-react'
import {
  useUserTokens,
  useCreateUserToken,
  useRevokeUserToken,
  userTokenStatus,
  errMsg,
  type UserTokenInfo,
  type CreateUserTokenPayload,
} from '@/api/userTokens'
import { copyToClipboard } from '@/lib/shared/clipboard'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import { Panel } from '@jianmanager/ui/components/panel'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Label } from '@jianmanager/ui/components/label'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'

/**
 * 我的令牌（FR-563 / ADR-112）。
 *
 * 与「Agent Token」页的区别：这里是**用户自助**的人类凭证（`jmut_`），
 * 任何登录用户都能看到自己名下的令牌并吊销；Agent Token 那页仅平台管理员可见。
 *
 * 明文只在创建成功后展示一次，关闭对话框即不可再查看——这是后端的硬约束，
 * 前端用一次性的 state 承载，不做持久化。
 */
export default function UserTokensPage() {
  const { t } = useTranslation()
  const listQ = useUserTokens()
  const createM = useCreateUserToken()
  const revokeM = useRevokeUserToken()

  const [showCreate, setShowCreate] = useState(false)
  const [revokeTarget, setRevokeTarget] = useState<UserTokenInfo | null>(null)
  /** 创建成功后的一次性明文；关闭对话框时清空。 */
  const [createdPlaintext, setCreatedPlaintext] = useState<string>('')

  const tokens = useMemo(() => listQ.data ?? [], [listQ.data])

  function handleCreate(payload: CreateUserTokenPayload) {
    createM.mutate(payload, {
      onSuccess: (res) => {
        setCreatedPlaintext(res.plaintext)
        toast.success(t('userTokens.created'))
      },
      onError: (e) => {
        toast.error(errMsg(e, t('userTokens.createFailed')))
      },
    })
  }

  function handleRevoke() {
    if (!revokeTarget) return
    revokeM.mutate(revokeTarget.id, {
      onSuccess: () => {
        toast.success(t('userTokens.revoked'))
        setRevokeTarget(null)
      },
      onError: (e) => {
        toast.error(errMsg(e, t('userTokens.revokeFailed')))
      },
    })
  }

  function closeCreate() {
    setShowCreate(false)
    setCreatedPlaintext('')
  }

  return (
    <div data-page="user-tokens" className="jm-page-stack space-y-4">
      <div className="jm-page-header">
        <div>
          <h1 className="jm-page-title">{t('userTokens.title')}</h1>
          <p className="jm-page-subtitle">{t('userTokens.subtitle')}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => setShowCreate(true)}>
            <Terminal className="size-4" /> {t('userTokens.cliHint')}
          </Button>
          <Button onClick={() => setShowCreate(true)}>
            <Plus className="size-4" /> {t('userTokens.create')}
          </Button>
        </div>
      </div>

      {listQ.isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : listQ.isError ? (
        <Panel>
          <p className="py-6 text-center text-sm text-muted-foreground">
            {errMsg(listQ.error, t('userTokens.loadFailed'))}
          </p>
        </Panel>
      ) : tokens.length === 0 ? (
        <Panel>
          <p className="py-6 text-center text-sm text-muted-foreground">{t('userTokens.empty')}</p>
        </Panel>
      ) : (
        <Panel bodyClassName="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('userTokens.col.name')}</TableHead>
                <TableHead>{t('userTokens.col.device')}</TableHead>
                <TableHead>{t('userTokens.col.prefix')}</TableHead>
                <TableHead>{t('userTokens.col.expires')}</TableHead>
                <TableHead>{t('userTokens.col.lastUsed')}</TableHead>
                <TableHead>{t('userTokens.col.status')}</TableHead>
                <TableHead className="w-28 text-right">{t('common.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {tokens.map((tok) => {
                const status = userTokenStatus(tok)
                return (
                  <TableRow key={tok.id}>
                    <TableCell className="font-medium">{tok.name}</TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {tok.deviceLabel || '—'}
                    </TableCell>
                    <TableCell>
                      <code className="font-mono text-xs">{tok.tokenPrefix}…</code>
                    </TableCell>
                    <TableCell className="whitespace-nowrap text-xs tabular-nums">
                      {tok.expiresAt ? new Date(tok.expiresAt).toLocaleString() : '—'}
                    </TableCell>
                    <TableCell className="whitespace-nowrap text-xs tabular-nums text-muted-foreground">
                      {tok.lastUsedAt ? new Date(tok.lastUsedAt).toLocaleString() : '—'}
                    </TableCell>
                    <TableCell>
                      <StatusBadge level={statusLevel(status)} label={t(`userTokens.status.${status}`)} />
                    </TableCell>
                    <TableCell className="text-right">
                      {status === 'active' && (
                        <Button
                          variant="outline"
                          size="sm"
                          className="text-destructive"
                          onClick={() => setRevokeTarget(tok)}
                        >
                          {t('userTokens.revoke')}
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </Panel>
      )}

      <CreateUserTokenDialog
        open={showCreate}
        pending={createM.isPending}
        plaintext={createdPlaintext}
        onClose={closeCreate}
        onSubmit={handleCreate}
      />

      <DangerConfirm
        open={revokeTarget !== null}
        title={t('userTokens.revokeTitle')}
        description={t('userTokens.revokeBody', { name: revokeTarget?.name ?? '' })}
        confirmText={t('userTokens.revoke')}
        pending={revokeM.isPending}
        onCancel={() => setRevokeTarget(null)}
        onConfirm={handleRevoke}
      />
    </div>
  )
}

/** 状态到 StatusBadge 档位映射。 */
function statusLevel(status: 'active' | 'revoked' | 'expired'): 'ok' | 'error' | 'warning' {
  switch (status) {
    case 'active':
      return 'ok'
    case 'revoked':
      return 'error'
    default:
      return 'warning'
  }
}

/** 创建令牌对话框；创建成功后原地切换为「明文展示」视图。 */
function CreateUserTokenDialog({
  open,
  pending,
  plaintext,
  onClose,
  onSubmit,
}: {
  open: boolean
  pending: boolean
  plaintext: string
  onClose: () => void
  onSubmit: (payload: CreateUserTokenPayload) => void
}) {
  const { t } = useTranslation()
  const [name, setName] = useState('')

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    const trimmed = name.trim()
    onSubmit({ name: trimmed || t('userTokens.defaultName') })
  }

  async function copyPlaintext() {
    const ok = await copyToClipboard(plaintext)
    toast[ok ? 'success' : 'error'](t(ok ? 'userTokens.copied' : 'userTokens.copyFailed'))
  }

  // 创建成功：只展示明文，不再显示表单。
  if (plaintext) {
    return (
      <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('userTokens.createdTitle')}</DialogTitle>
            <DialogDescription>{t('userTokens.createdBody')}</DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="flex items-center gap-2">
              <code className="flex-1 break-all rounded-md border bg-muted px-3 py-2 font-mono text-xs">
                {plaintext}
              </code>
              <Button variant="outline" size="sm" onClick={copyPlaintext}>
                <Copy className="size-4" />
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">{t('userTokens.onceOnly')}</p>
            <div className="rounded-md border bg-muted/40 p-3">
              <p className="mb-1 text-xs font-medium">{t('userTokens.usageTitle')}</p>
              <code className="block whitespace-pre-wrap font-mono text-[11px] text-muted-foreground">
                {'jm auth login --url <CP 地址>\n# 或\nexport JM_TOKEN=<上面这串>'}
              </code>
            </div>
          </div>
          <DialogFooter>
            <Button onClick={onClose}>{t('common.close')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    )
  }

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>{t('userTokens.createTitle')}</DialogTitle>
            <DialogDescription>{t('userTokens.createBody')}</DialogDescription>
          </DialogHeader>
          <div className="space-y-3 py-3">
            <div className="space-y-1.5">
              <Label htmlFor="user-token-name">{t('userTokens.nameLabel')}</Label>
              <Input
                id="user-token-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t('userTokens.namePlaceholder')}
                maxLength={128}
              />
              <p className="text-xs text-muted-foreground">{t('userTokens.nameHint')}</p>
            </div>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={pending}>
              {pending ? t('common.saving') : t('userTokens.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export { statusLevel }
