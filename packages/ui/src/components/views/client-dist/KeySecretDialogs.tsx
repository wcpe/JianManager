import { useTranslation } from 'react-i18next'
import { Copy } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { copyToClipboard } from '../../../lib/clipboard'
import type { ClientKeyWithSecret } from '../../../lib/client-channel-types'
/** 创建或改值后的明文展示弹窗：密钥已加密保存，后续仍可从列表查看（复制兼容 HTTP 非安全上下文）。 */
export function SecretDialog({
  secret,
  onClose,
  onNotify,
}: {
  secret: ClientKeyWithSecret | null
  onClose: () => void
  /** 复制结果回执（容器注入 toast；组件库不依赖 toast 实现）。 */
  onNotify?: (level: 'success' | 'error', message: string) => void
}) {
  const { t } = useTranslation()

  const copy = async () => {
    if (!secret) return
    const ok = await copyToClipboard(secret.key)
    onNotify?.(
      ok ? 'success' : 'error',
      ok ? t('clientChannels.copied', '已复制到剪贴板') : t('clientChannels.copyFailed', '复制失败，请手动选择复制'),
    )
  }

  return (
    <Dialog open={secret !== null} onOpenChange={(v: boolean) => { if (!v) onClose() }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('clientChannels.secretTitle', '拉取密钥')}</DialogTitle>
          <DialogDescription>
            {t('clientChannels.secretDesc', '此密钥已加密保存，关闭后仍可在密钥列表中随时查看明文。')}
          </DialogDescription>
        </DialogHeader>
        <div className="flex items-center gap-2 rounded-md border bg-muted/50 p-3">
          <code className="flex-1 break-all font-mono text-sm">{secret?.key}</code>
          <Button type="button" variant="outline" size="sm" onClick={copy} className="shrink-0">
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

/** 查看已存密钥明文弹窗（FR-192，可逆加密存储 → 可随时查看）：展示明文 + 复制（走 copyToClipboard）。 */
export function RevealDialog({
  revealed,
  onClose,
  onNotify,
}: {
  revealed: { name: string; key: string } | null
  onClose: () => void
  /** 复制结果回执（容器注入 toast；组件库不依赖 toast 实现）。 */
  onNotify?: (level: 'success' | 'error', message: string) => void
}) {
  const { t } = useTranslation()

  const copy = async () => {
    if (!revealed) return
    const ok = await copyToClipboard(revealed.key)
    onNotify?.(
      ok ? 'success' : 'error',
      ok ? t('clientChannels.copied', '已复制到剪贴板') : t('clientChannels.copyFailed', '复制失败，请手动选择复制'),
    )
  }

  return (
    <Dialog open={revealed !== null} onOpenChange={(v: boolean) => { if (!v) onClose() }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('clientChannels.revealTitle', '拉取密钥明文')}</DialogTitle>
          <DialogDescription>
            {t('clientChannels.revealDesc', '用于玩家侧更新器鉴权拉取，请复制到 jm-updater.json 妥善保存。')}
          </DialogDescription>
        </DialogHeader>
        {revealed && (
          <p className="text-xs text-muted-foreground">
            {t('clientChannels.revealKeyName', '密钥名称')}：{revealed.name}
          </p>
        )}
        <div className="flex items-center gap-2 rounded-md border bg-muted/50 p-3">
          <code className="flex-1 break-all font-mono text-sm">{revealed?.key}</code>
          <Button type="button" variant="outline" size="sm" onClick={copy} className="shrink-0">
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
