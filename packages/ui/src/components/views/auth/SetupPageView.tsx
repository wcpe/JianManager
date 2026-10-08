/**
 * @file SetupPageView：首次使用初始化页的受控视图，初始化状态查询、提交与跳转由应用容器负责。
 * @input lib/password-strength、Panel/Input/PasswordInput/Label/Button 原语、翻译上下文
 * @output SetupPageView、SetupPageViewProps、SetupValues
 * @sync apps/control-plane-web/src/pages/SetupPage.tsx、apps/control-plane-web/src/pages/SetupPage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原 FR-157 密码强度提示、FR-199 身份访问域）
 */
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Panel } from '@jianmanager/ui/components/panel'
import { Input } from '@jianmanager/ui/components/input'
import { Label } from '@jianmanager/ui/components/label'
import { Button } from '@jianmanager/ui/components/button'
import { PasswordInput } from '@jianmanager/ui/components/password-input'
import { passwordStrength } from '@jianmanager/ui/lib/password-strength'

/** 密码强度档位对应的进度条配色（FR-157）。 */
const STRENGTH_BAR: Record<number, string> = {
  1: 'bg-destructive',
  2: 'bg-yellow-500',
  3: 'bg-green-500',
  4: 'bg-green-500',
}

// 与创建用户/改密的密码下限一致（BUG-022）。
const PASSWORD_MIN = 8

/** 初始化提交载荷（初始管理员账号密码）。 */
export interface SetupValues {
  username: string
  password: string
}

/**
 * 受控边界：表单草稿、校验提示与密码强度展示留在视图；是否「无需初始化」的重定向、
 * 创建请求、token 落地与登录后跳转全在容器（包内不依赖 react-query / zustand / react-router）。
 */
export interface SetupPageViewProps {
  /**
   * 创建初始管理员；失败请抛错（视图取服务端 `message` 作内联错误，缺失时回退「创建失败」）。
   * 成功后视图不做跳转——由容器在 mutation 内导航。
   */
  onSubmit: (values: SetupValues) => Promise<void>
  /** 提交中：禁用提交按钮并显示「创建中」。 */
  submitting?: boolean
  /** 初始化状态查询中：渲染居中加载态；是否应重定向到登录页由容器先行判定。 */
  loading?: boolean
}

/** 首次使用引导（FR-157）：创建初始管理员，含实时密码强度与两次输入一致性提示。 */
export function SetupPageView({ onSubmit, submitting = false, loading = false }: SetupPageViewProps) {
  const { t } = useTranslation()
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')

  const strength = passwordStrength(password)
  const mismatch = confirm.length > 0 && confirm !== password

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')

    if (password !== confirm) {
      setError(t('setup.passwordMismatch'))
      document.getElementById('confirm')?.focus()
      return
    }

    if (password.length < PASSWORD_MIN) {
      setError(t('setup.passwordTooShort'))
      return
    }

    try {
      await onSubmit({ username, password })
    } catch (err) {
      const e2 = err as Error & { response?: { data?: { message?: string } } }
      setError(e2.response?.data?.message || t('setup.createFailed'))
    }
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center h-screen">
        <p className="text-muted-foreground">{t('setup.loading')}</p>
      </div>
    )
  }

  return (
    <div className="flex items-center justify-center h-screen">
      <Panel className="w-full max-w-sm" bodyClassName="p-6">
        <div className="mb-5 text-center">
          <h1 className="text-2xl font-semibold">{t('setup.title')}</h1>
          <p className="mt-1 text-sm text-muted-foreground">{t('setup.subtitle')}</p>
        </div>
        {error && (
          <div className="mb-4 rounded-md bg-destructive/10 p-3 text-sm text-destructive">{error}</div>
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
              minLength={PASSWORD_MIN}
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
              minLength={PASSWORD_MIN}
              maxLength={128}
              aria-invalid={mismatch}
            />
            {mismatch && <p className="text-xs text-destructive">{t('setup.passwordMismatch')}</p>}
          </div>
          <Button type="submit" className="w-full" disabled={submitting}>
            {submitting ? t('setup.creating') : t('setup.submit')}
          </Button>
        </form>
      </Panel>
    </div>
  )
}
