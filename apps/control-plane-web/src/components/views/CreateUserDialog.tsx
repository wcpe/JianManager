import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

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

/** 提示通道：视图算好文案交外壳展示（本包不弹 toast）。 */
export type CreateUserNotice = (kind: 'success' | 'error', message: string) => void

const USERNAME_MIN = 3
// 与初始化引导（SetupPage）的密码下限一致，避免同系统两处策略矛盾（BUG-022）。
const PASSWORD_MIN = 8

/**
 * 新建用户对话框。
 *
 * 受控视图（ADR-097 b 范式）：不发请求——创建经 `onCreate` 上报，外壳负责发请求与失效缓存。
 * 表单态、错误展示时机门控（`useFieldGate`）、角色/状态选项留在视图内。
 */
export interface CreateUserDialogProps {
  open: boolean
  onClose: () => void
  /** 创建用户；失败请抛错，视图取服务端 message 提示。 */
  onCreate: (payload: { username: string; password: string; role: number; status: number }) => Promise<void>
  /** 提示通道（成功与失败都经它）。 */
  notify: CreateUserNotice
}

export default function CreateUserDialog({ open, onClose, onCreate, notify }: CreateUserDialogProps) {
  const { t } = useTranslation()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState('0')
  const [status, setStatus] = useState('0')
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)
  const gate = useFieldGate()

  const roleOptions: ComboboxOption[] = [
    { value: '0', label: t('users.member') },
    { value: '1', label: t('users.groupAdmin') },
    { value: '10', label: t('users.platformAdmin') },
  ]
  const statusOptions: ComboboxOption[] = [
    { value: '0', label: t('users.enabled') },
    { value: '1', label: t('users.disabled') },
  ]

  const errors = validateFields(
    { username, password },
    {
      username: [validateRequired, minLength(USERNAME_MIN)],
      password: [validateRequired, minLength(PASSWORD_MIN)],
    },
  )

  const resetForm = () => {
    setUsername('')
    setPassword('')
    setRole('0')
    setStatus('0')
    setError('')
    gate.reset()
  }

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    gate.submit()
    if (hasErrors(errors)) return
    setError('')
    setPending(true)
    try {
      await onCreate({ username, password, role: Number(role), status: Number(status) })
      notify('success', t('users.created'))
      onClose()
      resetForm()
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      setError(msg || t('common.error'))
    } finally {
      setPending(false)
    }
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

            <div>
              <FieldLabel>{t('users.status')}</FieldLabel>
              <div className="mt-1">
                <Combobox options={statusOptions} value={status} onChange={setStatus} allowCustom={false} />
              </div>
            </div>
          </ScrollableDialogBody>

          <DialogFooter className="pt-4">
            <Button type="button" variant="outline" onClick={handleClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={pending || hasErrors(errors)}>
              {pending ? t('common.creating') : t('common.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
