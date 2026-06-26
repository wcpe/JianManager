import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useUpdateUser, type UserInfo } from '@/api/users'
import { MODAL_OVERLAY, MODAL_PANEL } from '@/components/ui/scrollable-dialog'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { FieldLabel, FieldError } from '@/components/ui/field-label'
import { minLength } from '@/lib/form-validation'

// 与初始化/创建用户的密码下限一致（BUG-022）。
const PASSWORD_MIN = 8

interface EditUserDialogProps {
  /** 编辑目标用户（父组件须以 user.id 作 key 渲染，确保切换用户时表单重置）。 */
  user: UserInfo
  onClose: () => void
}

/** 编辑用户：调整角色 + 可选重置登录密码（FR-156，兑现 FR-003）。 */
export default function EditUserDialog({ user, onClose }: EditUserDialogProps) {
  const { t } = useTranslation()
  const update = useUpdateUser()
  const [role, setRole] = useState(String(user.role))
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')

  const roleOptions: ComboboxOption[] = [
    { value: '0', label: t('users.member') },
    { value: '1', label: t('users.groupAdmin') },
    { value: '10', label: t('users.platformAdmin') },
  ]

  // 密码留空=不改；填了则须达下限。
  const passwordError = password !== '' ? minLength(PASSWORD_MIN)(password) : ''

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (passwordError) return
    setError('')
    const body: { id: number; role?: number; password?: string } = { id: user.id }
    if (Number(role) !== user.role) body.role = Number(role)
    if (password !== '') body.password = password
    // 无任何改动直接关闭，避免空请求。
    if (body.role === undefined && body.password === undefined) {
      onClose()
      return
    }
    update.mutate(body, {
      onSuccess: () => onClose(),
      onError: (err: Error & { response?: { data?: { message?: string } } }) =>
        setError(err.response?.data?.message || t('common.error')),
    })
  }

  return (
    <div className={MODAL_OVERLAY}>
      <div className={`${MODAL_PANEL} max-w-sm`}>
        <h2 className="text-lg font-bold mb-4">{t('users.editUser', { name: user.username })}</h2>

        {error && (
          <div className="mb-3 p-2 text-sm text-destructive bg-destructive/10 rounded">{error}</div>
        )}

        <form onSubmit={handleSubmit} className="space-y-3">
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

          <div className="flex justify-end gap-2 pt-2">
            <button
              type="button"
              onClick={onClose}
              className="px-4 py-2 text-sm border rounded-md hover:bg-accent"
            >
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              disabled={update.isPending || !!passwordError}
              className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50"
            >
              {update.isPending ? t('common.saving') : t('common.save')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
