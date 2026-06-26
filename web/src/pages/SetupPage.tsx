import { useState, type FormEvent } from 'react'
import { Navigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { useSetupStatus, useSetup } from '@/api/setup'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Button } from '@/components/ui/button'
import { PasswordInput } from '@/components/ui/password-input'
import { passwordStrength } from '@/lib/password-strength'

/** 密码强度档位对应的进度条配色（FR-157）。 */
const STRENGTH_BAR: Record<number, string> = {
  1: 'bg-destructive',
  2: 'bg-yellow-500',
  3: 'bg-green-500',
  4: 'bg-green-500',
}

export default function SetupPage() {
  const { t } = useTranslation()
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')

  const { data: status, isLoading } = useSetupStatus()
  const setup = useSetup()

  const strength = passwordStrength(password)
  const mismatch = confirm.length > 0 && confirm !== password

  if (!isLoading && status && !status.setupRequired) {
    return <Navigate to="/login" replace />
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    setError('')

    if (password !== confirm) {
      setError(t('setup.passwordMismatch'))
      document.getElementById('confirm')?.focus()
      return
    }

    if (password.length < 8) {
      setError(t('setup.passwordTooShort'))
      return
    }

    setup.mutate(
      { username, password },
      {
        onError: (err: Error & { response?: { data?: { message?: string } } }) => {
          setError(err.response?.data?.message || t('setup.createFailed'))
        },
      },
    )
  }

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-screen">
        <p className="text-muted-foreground">{t('setup.loading')}</p>
      </div>
    )
  }

  return (
    <div className="flex items-center justify-center h-screen">
      <Card className="w-full max-w-sm">
        <CardHeader className="text-center">
          <CardTitle className="text-2xl">{t('setup.title')}</CardTitle>
          <CardDescription>{t('setup.subtitle')}</CardDescription>
        </CardHeader>
        <CardContent>
          {error && (
            <div className="mb-4 p-3 text-sm text-destructive bg-destructive/10 rounded-md">
              {error}
            </div>
          )}

          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="username">{t('setup.username')}</Label>
              <Input
                id="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                required
                minLength={3}
                maxLength={64}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">{t('setup.password')}</Label>
              <PasswordInput
                id="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
                minLength={8}
                maxLength={128}
              />
              {password.length > 0 && (
                <div className="space-y-1">
                  <div className="flex items-center gap-2">
                    <div className="flex flex-1 gap-1">
                      {[1, 2, 3, 4].map((i) => (
                        <div
                          key={i}
                          className={`h-1 flex-1 rounded ${i <= strength.score ? STRENGTH_BAR[strength.score] : 'bg-muted'}`}
                        />
                      ))}
                    </div>
                    {strength.labelKey && (
                      <span className="text-xs text-muted-foreground">{t(strength.labelKey)}</span>
                    )}
                  </div>
                  <div className="flex flex-wrap gap-x-3 gap-y-0.5 text-[11px] text-muted-foreground">
                    {strength.rules.map((r) => (
                      <span key={r.key} className={r.met ? 'text-green-600 dark:text-green-500' : ''}>
                        {r.met ? '✓' : '○'} {t(r.key)}
                      </span>
                    ))}
                  </div>
                </div>
              )}
            </div>
            <div className="space-y-2">
              <Label htmlFor="confirm">{t('setup.confirm')}</Label>
              <PasswordInput
                id="confirm"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                required
                minLength={8}
                maxLength={128}
                aria-invalid={mismatch}
              />
              {mismatch && <p className="text-xs text-destructive">{t('setup.passwordMismatch')}</p>}
            </div>
            <Button type="submit" className="w-full" disabled={setup.isPending}>
              {setup.isPending ? t('setup.creating') : t('setup.submit')}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
