/**
 * @file CreateGroupDialogView：新建用户组对话框的受控视图，创建请求由应用容器负责。
 * @input form-validation、useFieldGate、FieldLabel/FieldError、Input/Textarea、Dialog/Button 原语、翻译上下文
 * @output CreateGroupDialogView、CreateGroupDialogViewProps、CreateGroupValues
 * @sync apps/control-plane-web/src/pages/GroupsPage.tsx
 * @since FR-502（组件受控化迁包；原 FR-156，兑现 FR-003）
 */
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@jianmanager/ui/components/button'
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
import { Textarea } from '@jianmanager/ui/components/textarea'
import { validateRequired } from '@/lib/form-validation'
import { useFieldGate } from '@/lib/use-field-gate'

/** 提交载荷：新建用户组的名称与描述。 */
export interface CreateGroupValues {
  name: string
  description: string
}

/**
 * 受控边界：开关经 `open`/`onClose` 注入，创建经 `onSubmit` 上报（失败请抛错，视图取服务端 message 提示），
 * 待提交态由 `submitting` 注入。表单态、错误展示时机门控（`useFieldGate`）留在视图内。
 */
export interface CreateGroupDialogViewProps {
  /** 是否展示对话框（关闭时不挂 DOM，与调用点 `{open && ...}` 等价）。 */
  open: boolean
  /** 关闭对话框：取消按钮、遮罩与 Esc 均走此回调，创建成功后亦由视图调用它。 */
  onClose: () => void
  /** 创建用户组；失败请抛错。 */
  onSubmit: (values: CreateGroupValues) => Promise<void>
  /** 创建中（禁用提交按钮并显示「创建中」）。 */
  submitting?: boolean
}

/** 新建用户组：名称 + 描述（FR-156，兑现 FR-003）。 */
export function CreateGroupDialogView({
  open,
  onClose,
  onSubmit,
  submitting = false,
}: CreateGroupDialogViewProps) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [error, setError] = useState('')
  const gate = useFieldGate()

  const resetForm = () => {
    setName('')
    setDescription('')
    setError('')
    gate.reset()
  }

  const nameError = validateRequired(name)

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    gate.submit()
    if (nameError) return
    setError('')
    try {
      await onSubmit({ name, description })
      onClose()
      resetForm()
    } catch (err) {
      const e2 = err as Error & { response?: { data?: { message?: string } } }
      setError(e2.response?.data?.message || t('groups.createFailed', t('common.error')))
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
          <DialogTitle>{t('groups.createGroup')}</DialogTitle>
        </DialogHeader>

        {error && (
          <div className="mb-3 rounded bg-destructive/10 p-2 text-sm text-destructive">{error}</div>
        )}

        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <ScrollableDialogBody className="space-y-3">
            <div>
              <FieldLabel htmlFor="create-group-name" required>{t('common.name')}</FieldLabel>
              <Input
                id="create-group-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                onBlur={() => gate.touch('name')}
                className="mt-1"
                aria-invalid={!!gate.show('name', nameError)}
              />
              <FieldError error={gate.show('name', nameError)} />
            </div>

            <div>
              <FieldLabel htmlFor="create-group-description">{t('groups.description')}</FieldLabel>
              <Textarea
                id="create-group-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                rows={3}
                className="mt-1"
              />
            </div>
          </ScrollableDialogBody>

          <DialogFooter className="pt-4">
            <Button type="button" variant="outline" onClick={handleClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={submitting || !!nameError}>
              {submitting ? t('common.creating') : t('common.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
