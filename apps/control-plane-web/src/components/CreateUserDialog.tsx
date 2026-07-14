import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useQueryClient, useMutation } from '@tanstack/react-query'
import api from '@/api/client'
import { type UserInfo } from '@/api/users'
import { Button } from '@jianmanager/ui/components/button'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  ScrollableDialogBody,
  scrollableDialogContentClass,
} from '@jianmanager/ui/components/scrollable-dialog'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { Input } from '@jianmanager/ui/components/input'
import { validateRequired, minLength, validateFields, hasErrors } from '@/lib/form-validation'
import { useFieldGate } from '@/lib/use-field-gate'

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
  const gate = useFieldGate()

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
    // register 仅建普通成员；若选了更高角色，据返回 uuid 定位新用户并应用（FR-156）。
    mutationFn: async (body: { username: string; password: string; role: string }) => {
      const res = await api.post<{ id?: string }>('/auth/register', {
        username: body.username,
        password: body.password,
      })
      const newUuid = res.data?.id
      if (body.role !== '0' && newUuid) {
        const { data: list } = await api.get<UserInfo[]>('/users')
        const created = list.find((u) => u.uuid === newUuid)
        if (created) await api.put(`/users/${created.id}`, { role: Number(body.role) })
      }
    },
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
    gate.reset()
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    gate.submit()
    if (hasErrors(errors)) return
    setError('')
    create.mutate({ username, password, role })
  }

  const handleClose = () => {
    onClose()
    resetForm()
  }

  if (!open) return null

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) handleClose() }}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-sm`}>
        <DialogHeader>
          <DialogTitle>{t('users.createUser')}</DialogTitle>
        </DialogHeader>

        {error && (
          <div className="mb-3 rounded bg-destructive/10 p-2 text-sm text-destructive">{error}</div>
        )}

        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <ScrollableDialogBody className="space-y-3">
            <div>
              <FieldLabel htmlFor="create-user-username" required>{t('users.username')}</FieldLabel>
              <Input
                id="create-user-username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                onBlur={() => gate.touch('username')}
                className="mt-1"
                aria-invalid={!!gate.show('username', errors.username)}
              />
              <FieldError error={gate.show('username', errors.username)} values={{ min: USERNAME_MIN }} />
            </div>

            <div>
              <FieldLabel htmlFor="create-user-password" required>{t('login.password')}</FieldLabel>
              <Input
                id="create-user-password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                onBlur={() => gate.touch('password')}
                className="mt-1"
                aria-invalid={!!gate.show('password', errors.password)}
              />
              <FieldError error={gate.show('password', errors.password)} values={{ min: PASSWORD_MIN }} />
            </div>

            <div>
              <FieldLabel>{t('users.role')}</FieldLabel>
              <div className="mt-1">
                <Combobox options={roleOptions} value={role} onChange={setRole} allowCustom={false} />
              </div>
              <p className="mt-1 text-xs text-muted-foreground">{t(`users.roleDesc_${role}`)}</p>
            </div>
          </ScrollableDialogBody>

          <DialogFooter className="pt-4">
            <Button type="button" variant="outline" onClick={handleClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={create.isPending || hasErrors(errors)}>
              {create.isPending ? t('common.creating') : t('common.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
