import { useState, type FormEvent } from 'react'
import { Navigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { useLogin } from '@/api/auth'
import { useSetupStatus } from '@/api/setup'
import { useAuthStore } from '@/stores/auth'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Button } from '@/components/ui/button'

export default function LoginPage() {
  const { t } = useTranslation()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')

  const login = useLogin()
  const { data: setupStatus, isLoading } = useSetupStatus()
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated)

  // 已登录用户不应停留在登录页，跳回控制台（BUG-006）
  if (isAuthenticated) {
    return <Navigate to="/" replace />
  }

  if (!isLoading && setupStatus?.setupRequired) {
    return <Navigate to="/setup" replace />
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    setError('')

    login.mutate(
      { username, password },
      {
        onError: (err: Error & { response?: { data?: { message?: string } } }) => {
          setError(err.response?.data?.message || t('login.loginFailed'))
        },
      },
    )
  }

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-screen">
        <p className="text-muted-foreground">{t('common.loading')}</p>
      </div>
    )
  }

  return (
    <div className="flex items-center justify-center h-screen">
      <Card className="w-full max-w-sm">
        <CardHeader className="text-center">
          <CardTitle className="text-2xl">{t('login.title')}</CardTitle>
          <CardDescription>{t('login.subtitle')}</CardDescription>
        </CardHeader>
        <CardContent>
          {error && (
            <div className="mb-4 p-3 text-sm text-destructive bg-destructive/10 rounded-md">
              {error}
            </div>
          )}

          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="username">{t('login.username')}</Label>
              <Input
                id="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                required
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">{t('login.password')}</Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </div>
            <Button type="submit" className="w-full" disabled={login.isPending}>
              {login.isPending ? `${t('login.submit')}...` : t('login.submit')}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
