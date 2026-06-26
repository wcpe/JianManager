import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useQueryClient, useMutation } from '@tanstack/react-query'
import api from '@/api/client'
import { MODAL_OVERLAY, MODAL_PANEL } from '@/components/ui/scrollable-dialog'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { FieldLabel, FieldError } from '@/components/ui/field-label'
import { validateRequired, minLength, validateFields, hasErrors } from '@/lib/form-validation'

interface CreateUserDialogProps {
  open: boolean
  onClose: () => void
}

const USERNAME_MIN = 3
// 与初始化引导（SetupPage）的密码下限一致，避免同系统两处策略矛盾（BUG-022）。
const PASSWORD_MIN = 8

export default function CreateUserDialog({ open, onClose }: CreateUserDialogProps) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState('0')
  const [error, setError] = useState('')

  const roleOptions: ComboboxOption[] = [
    { value: '0', label: t('users.member') },
    { value: '1', label: t('users.groupAdmin') },
    { value: '10', label: t('users.platformAdmin') },
  ]

  const errors = validateFields(
    { username, password },
    {
      username: [validateRequired, minLength(USERNAME_MIN)],
      password: [validateRequired, minLength(PASSWORD_MIN)],
    },
  )

  const create = useMutation({
    mutationFn: (body: { username: string; password: string }) =>
      api.post('/auth/register', body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['users'] })
      onClose()
      resetForm()
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      setError(err.response?.data?.message || t('common.error'))
    },
  })

  const resetForm = () => {
    setUsername('')
    setPassword('')
    setRole('0')
    setError('')
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (hasErrors(errors)) return
    setError('')
    create.mutate({ username, password })
  }

  if (!open) return null

  return (
    <div className={MODAL_OVERLAY}>
      <div className={`${MODAL_PANEL} max-w-sm`}>
        <h2 className="text-lg font-bold mb-4">{t('users.createUser')}</h2>

        {error && (
          <div className="mb-3 p-2 text-sm text-destructive bg-destructive/10 rounded">{error}</div>
        )}

        <form onSubmit={handleSubmit} className="space-y-3">
          <div>
            <FieldLabel required>{t('users.username')}</FieldLabel>
            <input
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
              aria-invalid={!!errors.username}
            />
            <FieldError error={errors.username} values={{ min: USERNAME_MIN }} />
          </div>

          <div>
            <FieldLabel required>{t('login.password')}</FieldLabel>
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
              aria-invalid={!!errors.password}
            />
            <FieldError error={errors.password} values={{ min: PASSWORD_MIN }} />
          </div>

          <div>
            <FieldLabel>{t('users.role')}</FieldLabel>
            <div className="mt-1">
              <Combobox options={roleOptions} value={role} onChange={setRole} allowCustom={false} />
            </div>
          </div>

          <div className="flex justify-end gap-2 pt-2">
            <button
              type="button"
              onClick={() => { onClose(); resetForm() }}
              className="px-4 py-2 text-sm border rounded-md hover:bg-accent"
            >
              {t('common.cancel')}
            </button>
            <button
              type="submit"
              disabled={create.isPending || hasErrors(errors)}
              className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50"
            >
              {create.isPending ? t('common.creating') : t('common.create')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
