import { useState, type FormEvent } from 'react'
import { Navigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { useSetupStatus, useSetup } from '@/api/setup'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Button } from '@/components/ui/button'

export default function SetupPage() {
  const { t } = useTranslation()
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')

  const { data: status, isLoading } = useSetupStatus()
  const setup = useSetup()

  if (!isLoading && status && !status.setupRequired) {
    return <Navigate to="/login" replace />
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    setError('')

    if (password !== confirm) {
      setError(t('setup.passwordMismatch'))
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
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
                minLength={8}
                maxLength={128}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="confirm">{t('setup.confirm')}</Label>
              <Input
                id="confirm"
                type="password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                required
                minLength={8}
                maxLength={128}
              />
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
