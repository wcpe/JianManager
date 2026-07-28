import { useState, type FormEvent } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import api from '@/api/client'
import type { CreateInvitationResponse } from '@/api/users'
import { Button } from '@jianmanager/ui/components/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { ScrollableDialogBody, scrollableDialogContentClass } from '@jianmanager/ui/components/scrollable-dialog'
import { FieldLabel } from '@jianmanager/ui/components/field-label'
import { Input } from '@jianmanager/ui/components/input'

interface CreateInvitationDialogProps {
  open: boolean
  onClose: () => void
}

/** 管理员签发一次性成员邀请；链接只在本次响应的对话框内显示。 */
export default function CreateInvitationDialog({ open, onClose }: CreateInvitationDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [email, setEmail] = useState('')
  const [error, setError] = useState('')
  const [invitationUrl, setInvitationUrl] = useState('')

  const create = useMutation({
    mutationFn: async () => {
      const { data } = await api.post<CreateInvitationResponse>('/users/invitations', { email, sendEmail: true })
      return data
    },
    onSuccess: (data) => {
      setInvitationUrl(data.invitationUrl)
      void queryClient.invalidateQueries({ queryKey: ['users', 'invitations'] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      setError(err.response?.data?.message || t('common.error'))
    },
  })

  const close = () => {
    setEmail('')
    setError('')
    setInvitationUrl('')
    onClose()
  }

  const submit = (event: FormEvent) => {
    event.preventDefault()
    setError('')
    create.mutate()
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
            {!invitationUrl && <Button type="submit" disabled={create.isPending}>{t('users.createInvitation')}</Button>}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
