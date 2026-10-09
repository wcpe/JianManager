/**
 * @file EditUserDialogView：编辑用户（角色 + 可选重置密码）的受控视图，更新请求由应用容器负责。
 * @input form-validation、Combobox/Button/Dialog/滚动壳原语、FieldLabel/FieldError、翻译上下文
 * @output EditUserDialogView、EditUserDialogViewProps、EditUserTarget、EditUserValues
 * @sync apps/control-plane-web/src/pages/UsersPage.tsx
 * @since FR-502（组件受控化迁包；原 FR-156，兑现 FR-003）
 */
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import {
  ScrollableDialogBody,
  scrollableDialogContentClass,
} from '@jianmanager/ui/components/scrollable-dialog'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { Button } from '@jianmanager/ui/components/button'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { minLength } from '@/lib/form-validation'

// 与初始化/创建用户的密码下限一致（BUG-022）。
const PASSWORD_MIN = 8

/** 视图只需要用户名（标题）与当前角色（初始值 + 「是否改动」判定）；用户的 id 留在容器。 */
export interface EditUserTarget {
  username: string
  role: number
}

/** 提交载荷：只含真正改动的字段（无改动时视图直接关闭，不发请求）。 */
export interface EditUserValues {
  role?: number
  password?: string
}

/**
 * 受控边界：初始值经 `user` 注入，更新经 `onSubmit` 上报（失败请抛错，视图取服务端 message 提示），
 * 待提交态由 `submitting` 注入；关闭经 `onClose` 上报。
 */
export interface EditUserDialogViewProps {
  /** 编辑目标用户（父组件须以 user.id 作 key 渲染，确保切换用户时表单重置）。 */
  user: EditUserTarget
  /** 关闭对话框：取消按钮、遮罩与 Esc 均走此回调，更新成功或「无改动」时亦由视图调用它。 */
  onClose: () => void
  /** 提交更新；失败请抛错。 */
  onSubmit: (values: EditUserValues) => Promise<void>
  /** 提交中（禁用保存按钮并显示「保存中」）。 */
  submitting?: boolean
}

/** 编辑用户：调整角色 + 可选重置登录密码（FR-156，兑现 FR-003）。 */
export function EditUserDialogView({
  user,
  onClose,
  onSubmit,
  submitting = false,
}: EditUserDialogViewProps) {
  const { t } = useTranslation()
  const [role, setRole] = useState(String(user.role))
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')

  const roleOptions: ComboboxOption[] = [
    { value: '0', label: t('users.member') },
    { value: '1', label: t('users.groupAdmin') },
    { value: '2', label: t('users.groupOperator') },
    { value: '3', label: t('users.groupViewer') },
    { value: '10', label: t('users.platformAdmin') },
  ]

  // 密码留空=不改；填了则须达下限。
  const passwordError = password !== '' ? minLength(PASSWORD_MIN)(password) : ''

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (passwordError) return
    setError('')
    const body: EditUserValues = {}
    if (Number(role) !== user.role) body.role = Number(role)
    if (password !== '') body.password = password
    // 无任何改动直接关闭，避免空请求。
    if (body.role === undefined && body.password === undefined) {
      onClose()
      return
    }
    try {
      await onSubmit(body)
      onClose()
    } catch (err) {
      const e2 = err as Error & { response?: { data?: { message?: string } } }
      setError(e2.response?.data?.message || t('common.error'))
    }
  }

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose() }}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-sm`}>
        <DialogHeader>
          <DialogTitle>{t('users.editUser', { name: user.username })}</DialogTitle>
        </DialogHeader>

        {error && (
          <div className="mb-3 p-2 text-sm text-destructive bg-destructive/10 rounded">{error}</div>
        )}

        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <ScrollableDialogBody className="space-y-3">
            <div>
              <FieldLabel>{t('users.role')}</FieldLabel>
              <div className="mt-1">
                <Combobox options={roleOptions} value={role} onChange={setRole} allowCustom={false} />
              </div>
              <p className="mt-1 text-xs text-muted-foreground">{t('users.roleHint')}</p>
            </div>

            <div>
              <FieldLabel>{t('users.resetPassword')}</FieldLabel>
              <input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={t('users.resetPasswordPlaceholder')}
                autoComplete="new-password"
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
                aria-invalid={!!passwordError}
              />
              <FieldError error={passwordError} values={{ min: PASSWORD_MIN }} />
            </div>
          </ScrollableDialogBody>

          <DialogFooter className="pt-4">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button
              type="submit"
              disabled={submitting || !!passwordError}
            >
              {submitting ? t('common.saving') : t('common.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
