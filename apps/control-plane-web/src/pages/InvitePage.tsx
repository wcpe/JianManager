import { useState, type FormEvent } from 'react'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import api from '@/api/client'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { FieldLabel } from '@jianmanager/ui/components/field-label'

/** 公开邀请接受页：令牌仅从 fragment 读取，避免跟随首个请求进入服务端日志。 */
export default function InvitePage() {
  const { t } = useTranslation()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const token = window.location.hash.slice(1)
    if (!token) {
      setError(t('users.invitationInvalid'))
      return
    }
    setPending(true)
    setError('')
    try {
      await api.post('/auth/invitations/accept', { token, username, password })
      window.history.replaceState(null, '', '/invite')
      setMessage(t('users.invitationAccepted'))
    } catch (err: unknown) {
      const detail = (err as { response?: { data?: { message?: string } } }).response?.data?.message
      setError(detail || t('users.invitationInvalid'))
    } finally {
      setPending(false)
    }
  }

  return (
    <main className="flex min-h-screen items-center justify-center bg-muted/30 p-4">
      <Panel className="w-full max-w-sm" bodyClassName="space-y-4 p-6">
        <div>
          <h1 className="text-xl font-semibold">{t('users.acceptInvitation')}</h1>
          <p className="mt-1 text-sm text-muted-foreground">{t('users.acceptInvitationHint')}</p>
        </div>
        {message ? (
          <div className="space-y-3">
            <p className="text-sm text-status-success">{message}</p>
            <Button asChild><Link to="/login">{t('users.goLogin')}</Link></Button>
          </div>
        ) : (
          <form className="space-y-3" onSubmit={submit}>
            {error && <p className="rounded bg-destructive/10 p-2 text-sm text-destructive">{error}</p>}
            <div>
              <FieldLabel htmlFor="invite-username" required>{t('users.username')}</FieldLabel>
              <Input id="invite-username" value={username} onChange={(event) => setUsername(event.target.value)} minLength={3} required />
            </div>
            <div>
              <FieldLabel htmlFor="invite-password" required>{t('login.password')}</FieldLabel>
              <Input id="invite-password" type="password" value={password} onChange={(event) => setPassword(event.target.value)} minLength={8} required />
            </div>
            <Button type="submit" className="w-full" disabled={pending}>{t('users.acceptInvitation')}</Button>
          </form>
        )}
      </Panel>
    </main>
  )
}
