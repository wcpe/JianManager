/**
 * @file GroupEditDialogView：编辑用户组（名称/描述 + 配额）的受控视图，提交经回调上报。
 * @input form-validation、FieldLabel/FieldError、Dialog/Button/滚动壳原语、翻译上下文
 * @output GroupEditDialogView、GroupEditDialogViewProps、GroupEditTarget、GroupEditValues
 * @sync apps/control-plane-web/src/components/GroupEditDialog.tsx、apps/control-plane-web/src/pages/GroupsPage.tsx
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
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import {
  ScrollableDialogBody,
  scrollableDialogContentClass,
} from '@jianmanager/ui/components/scrollable-dialog'
import { validateRequired } from '@jianmanager/ui/lib/form-validation'

/** 视图只需要展示用字段；组的 id 等归属信息留在应用容器（请求由容器发出）。 */
export interface GroupEditTarget {
  name: string
  description?: string
  /** 配额缺省项按 0 呈现（与后端「0 = 不限」一致）。 */
  quota?: { maxInstances?: number; maxBots?: number; maxStorageMb?: number }
}

/** 提交载荷：名称/描述与配额一并上报，由容器决定拆成哪几个请求。 */
export interface GroupEditValues {
  name: string
  description: string
  quota: { maxInstances: number; maxBots: number; maxStorageMb: number }
}

/**
 * 受控边界：初始值经 `group` 注入，提交经 `onSubmit` 上报，关闭经 `onClose` 上报。
 * 表单态、必填校验与内联错误留在视图内；请求、toast 与缓存失效由容器负责。
 */
export interface GroupEditDialogViewProps {
  /** 编辑目标的初始值（父级须以组 id 作 key 渲染，切换组时重置表单）。 */
  group: GroupEditTarget
  /** 关闭对话框：取消按钮、遮罩与 Esc 均走此回调，提交成功后亦由视图调用它。 */
  onClose: () => void
  /** 提交编辑；失败请抛错，视图取服务端 message 作内联提示。 */
  onSubmit: (values: GroupEditValues) => Promise<void>
  /** 提交中（禁用保存按钮并显示「保存中」）。 */
  submitting?: boolean
}

/** 编辑用户组：名称/描述 + 配额（实例/Bot/存储上限）（FR-156，兑现 FR-003）。 */
export function GroupEditDialogView({
  group,
  onClose,
  onSubmit,
  submitting = false,
}: GroupEditDialogViewProps) {
  const { t } = useTranslation()
  const [name, setName] = useState(group.name)
  const [description, setDescription] = useState(group.description ?? '')
  const [maxInstances, setMaxInstances] = useState(String(group.quota?.maxInstances ?? 0))
  const [maxBots, setMaxBots] = useState(String(group.quota?.maxBots ?? 0))
  const [maxStorageMb, setMaxStorageMb] = useState(String(group.quota?.maxStorageMb ?? 0))
  const [error, setError] = useState('')

  const nameError = validateRequired(name)

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (nameError) return
    setError('')
    try {
      await onSubmit({
        name,
        description,
        quota: {
          maxInstances: Number(maxInstances),
          maxBots: Number(maxBots),
          maxStorageMb: Number(maxStorageMb),
        },
      })
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
          <DialogTitle>{t('groups.editGroup', { name: group.name })}</DialogTitle>
        </DialogHeader>

        {error && (
          <div className="mb-3 p-2 text-sm text-destructive bg-destructive/10 rounded">{error}</div>
        )}

        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <ScrollableDialogBody className="space-y-3">
            <div>
              <FieldLabel required>{t('common.name')}</FieldLabel>
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
                aria-invalid={!!nameError}
              />
              <FieldError error={nameError} />
            </div>

            <div>
              <FieldLabel>{t('groups.description')}</FieldLabel>
              <textarea
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
                rows={2}
              />
            </div>

            <div className="grid grid-cols-3 gap-2">
              <div>
                <FieldLabel>{t('groups.instanceQuota')}</FieldLabel>
                <input
                  type="number"
                  min={0}
                  value={maxInstances}
                  onChange={(e) => setMaxInstances(e.target.value)}
                  className="w-full mt-1 px-2 py-2 border rounded-md bg-background text-sm"
                />
              </div>
              <div>
                <FieldLabel>{t('groups.botQuota')}</FieldLabel>
                <input
                  type="number"
                  min={0}
                  value={maxBots}
                  onChange={(e) => setMaxBots(e.target.value)}
                  className="w-full mt-1 px-2 py-2 border rounded-md bg-background text-sm"
                />
              </div>
              <div>
                <FieldLabel>{t('groups.storageQuotaMb')}</FieldLabel>
                <input
                  type="number"
                  min={0}
                  value={maxStorageMb}
                  onChange={(e) => setMaxStorageMb(e.target.value)}
                  className="w-full mt-1 px-2 py-2 border rounded-md bg-background text-sm"
                />
              </div>
            </div>
          </ScrollableDialogBody>

          <DialogFooter className="pt-4">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button
              type="submit"
              disabled={submitting || !!nameError}
            >
              {submitting ? t('common.saving') : t('common.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
