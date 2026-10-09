import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@jianmanager/ui/components/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { ScrollableDialogBody, scrollableDialogContentClass } from '@jianmanager/ui/components/scrollable-dialog'
import { FieldLabel } from '@jianmanager/ui/components/field-label'
import { Input } from '@jianmanager/ui/components/input'

/**
 * 管理员签发一次性成员邀请；链接只在本次响应的对话框内显示。
 *
 * 受控视图（ADR-097 b 范式）：不发请求——创建经 `onCreate` 上报，外壳负责发请求与失效缓存。
 * 表单态、错误展示与「已创建则只显示链接」的切换留在视图内。
 */
export interface CreateInvitationDialogProps {
  open: boolean
  onClose: () => void
  /** 创建邀请；失败请抛错，视图取服务端 message 提示。 */
  onCreate: (email: string) => Promise<{ invitationUrl: string }>
}

export default function CreateInvitationDialog({ open, onClose, onCreate }: CreateInvitationDialogProps) {
  const { t } = useTranslation()
  const [email, setEmail] = useState('')
  const [error, setError] = useState('')
  const [invitationUrl, setInvitationUrl] = useState('')
  const [pending, setPending] = useState(false)

  const close = () => {
    setEmail('')
    setError('')
    setInvitationUrl('')
    onClose()
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setError('')
    setPending(true)
    try {
      const data = await onCreate(email)
      setInvitationUrl(data.invitationUrl)
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      setError(msg || t('common.error'))
    } finally {
      setPending(false)
    }
  }

  if (!open) return null

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) close() }}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-md`}>
        <DialogHeader><DialogTitle>{t('users.inviteUser')}</DialogTitle></DialogHeader>
        <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
          <ScrollableDialogBody className="space-y-3">
            {error && <p className="rounded bg-destructive/10 p-2 text-sm text-destructive">{error}</p>}
            {invitationUrl ? (
              <div className="space-y-2">
                <p className="text-sm">{t('users.invitationCreated')}</p>
                <p className="text-xs text-muted-foreground">{t('users.invitationUrlHint')}</p>
                <Input aria-label={t('users.invitationUrl')} value={invitationUrl} readOnly />
              </div>
            ) : (
              <div>
                <FieldLabel htmlFor="invite-email" required>{t('users.email')}</FieldLabel>
                <Input id="invite-email" type="email" value={email} onChange={(event) => setEmail(event.target.value)} className="mt-1" required />
              </div>
            )}
          </ScrollableDialogBody>
          <DialogFooter className="pt-4">
            <Button type="button" variant="outline" onClick={close}>{invitationUrl ? t('common.close') : t('common.cancel')}</Button>
            {!invitationUrl && <Button type="submit" disabled={pending}>{t('users.createInvitation')}</Button>}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
