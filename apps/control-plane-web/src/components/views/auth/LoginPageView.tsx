/**
 * @file LoginPageView：登录页的受控视图，认证请求、token 落地与路由跳转由应用容器负责。
 * @input Panel/Input/PasswordInput/Label/Button 原语、翻译上下文
 * @output LoginPageView、LoginPageViewProps、LoginValues
 * @sync apps/control-plane-web/src/pages/LoginPage.tsx、apps/control-plane-web/src/pages/LoginPage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原 FR-157 认证体验、FR-199 身份访问域）
 */
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Panel } from '@jianmanager/ui/components/panel'
import { Input } from '@jianmanager/ui/components/input'
import { PasswordInput } from '@jianmanager/ui/components/password-input'
import { Label } from '@jianmanager/ui/components/label'
import { Button } from '@jianmanager/ui/components/button'

/** 登录提交载荷（原样转给 `POST /auth/login`）。 */
export interface LoginValues {
  username: string
  password: string
}

/**
 * 受控边界：表单草稿与内联错误留在视图；登录请求、token 落地、`returnTo` 解析以及
 * 「已登录/待初始化」重定向全在容器（包内不依赖 zustand 鉴权 store、react-query、react-router）。
 */
export interface LoginPageViewProps {
  /**
   * 提交凭据；失败请抛错（视图取服务端 `message` 作内联错误，缺失时回退「登录失败」）。
   * 成功后视图不做任何跳转——由容器在 mutation 内导航。
   */
  onSubmit: (values: LoginValues) => Promise<void>
  /** 提交中：禁用输入与按钮，按钮文案追加省略号（在途回车/再点由视图短路，防重复提交）。 */
  submitting?: boolean
  /** 初始化状态查询中：渲染居中加载态；是否应重定向到初始化页由容器先行判定。 */
  loading?: boolean
}

/** 登录页（FR-157）：账号密码登录，认证失败内联提示且停留本页。 */
export function LoginPageView({ onSubmit, submitting = false, loading = false }: LoginPageViewProps) {
  const { t } = useTranslation()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    // 在途短路：pending 中回车/再点不重发登录请求（验收矩阵 #1 防重复提交）。
    if (submitting) return
    setError('')
    try {
      await onSubmit({ username, password })
    } catch (err) {
      const e2 = err as Error & { response?: { data?: { message?: string } } }
      setError(e2.response?.data?.message || t('login.loginFailed'))
    }
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center h-screen">
        <p className="text-muted-foreground">{t('common.loading')}</p>
      </div>
    )
  }

  return (
    <div className="flex items-center justify-center h-screen">
      <Panel className="w-full max-w-sm" bodyClassName="p-6">
        <div className="mb-5 text-center">
          <h1 className="text-2xl font-semibold">{t('login.title')}</h1>
          <p className="mt-1 text-sm text-muted-foreground">{t('login.subtitle')}</p>
        </div>
        {error && (
          <div role="alert" className="mb-4 rounded-md bg-destructive/10 p-3 text-sm text-destructive">{error}</div>
        )}

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="username">{t('login.username')}</Label>
            <Input
              id="username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              disabled={submitting}
              required
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="password">{t('login.password')}</Label>
            <PasswordInput
              id="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              disabled={submitting}
              required
            />
          </div>
          <Button type="submit" className="w-full" disabled={submitting}>
            {submitting ? `${t('login.submit')}...` : t('login.submit')}
          </Button>
        </form>
      </Panel>
    </div>
  )
}
