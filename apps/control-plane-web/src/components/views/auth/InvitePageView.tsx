/**
 * @file InvitePageView：公开邀请接受页的受控视图，令牌读取与接受请求由应用容器负责。
 * @input Panel/Button/Input/FieldLabel 原语、翻译上下文
 * @output InvitePageView、InvitePageViewProps、InviteValues
 * @sync apps/control-plane-web/src/pages/InvitePage.tsx、apps/control-plane-web/src/pages/InvitePage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原 FR-405 邮箱邀请，其前身 FR-001/002/156）
 */
import { useState, type FormEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { FieldLabel } from '@jianmanager/ui/components/field-label'

/** 接受邀请的提交载荷（令牌不在其中：它由容器从 URL fragment 读取，视图不接触）。 */
export interface InviteValues {
  username: string
  password: string
}

/** 受控边界：接受请求与成功后清理 URL（history）由容器负责，视图只管表单草稿与结果展示。 */
export interface InvitePageViewProps {
  /**
   * 接受邀请；失败请抛错（视图取服务端 `message` 作内联错误，缺失时回退「邀请无效」，
   * 令牌缺失同样走此兜底）。成功后视图切到「已接受」态并给出去登录入口。
   */
  onSubmit: (values: InviteValues) => Promise<void>
  /** 渲染「前往登录」链接（应用侧接 react-router 的 Link；缺省渲染原生 `<a>`）。 */
  renderLoginLink?: (args: { to: string; children: ReactNode }) => ReactNode
}

/** 公开邀请接受页（FR-405）：令牌仅从 fragment 读取，避免跟随首个请求进入服务端日志。 */
export function InvitePageView({ onSubmit, renderLoginLink }: InvitePageViewProps) {
  const { t } = useTranslation()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    // 与原实现一致：在途不做短路，仅靠按钮禁用态挡住重复点击。
    setPending(true)
    setError('')
    try {
      await onSubmit({ username, password })
      setMessage(t('users.invitationAccepted'))
    } catch (err: unknown) {
      const detail = (err as { response?: { data?: { message?: string } } }).response?.data?.message
      setError(detail || t('users.invitationInvalid'))
    } finally {
      setPending(false)
    }
  }

  const loginLink =
    renderLoginLink?.({ to: '/login', children: t('users.goLogin') }) ?? <a href="/login">{t('users.goLogin')}</a>

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
            <Button asChild>{loginLink}</Button>
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
