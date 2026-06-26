import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useUpdateGroup, useUpdateGroupQuota, type GroupInfo } from '@/api/groups'
import { MODAL_OVERLAY, MODAL_PANEL } from '@/components/ui/scrollable-dialog'
import { FieldLabel, FieldError } from '@/components/ui/field-label'
import { validateRequired } from '@/lib/form-validation'

interface GroupEditDialogProps {
  /** 编辑目标组（父组件须以 group.id 作 key 渲染以重置表单）。 */
  group: GroupInfo
  onClose: () => void
}

/** 编辑用户组：名称/描述 + 配额（实例/Bot/存储上限）（FR-156，兑现 FR-003）。 */
export default function GroupEditDialog({ group, onClose }: GroupEditDialogProps) {
  const { t } = useTranslation()
  const update = useUpdateGroup()
  const updateQuota = useUpdateGroupQuota()
  const [name, setName] = useState(group.name)
  const [description, setDescription] = useState(group.description ?? '')
  const [maxInstances, setMaxInstances] = useState(String(group.quota?.maxInstances ?? 0))
  const [maxBots, setMaxBots] = useState(String(group.quota?.maxBots ?? 0))
  const [maxStorageMb, setMaxStorageMb] = useState(String(group.quota?.maxStorageMb ?? 0))
  const [error, setError] = useState('')

  const nameError = validateRequired(name)
  const pending = update.isPending || updateQuota.isPending

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (nameError) return
    setError('')
    try {
      await update.mutateAsync({ id: group.id, name, description })
      await updateQuota.mutateAsync({
        id: group.id,
        maxInstances: Number(maxInstances),
        maxBots: Number(maxBots),
        maxStorageMb: Number(maxStorageMb),
      })
      onClose()
    } catch (err) {
      const e2 = err as Error & { response?: { data?: { message?: string } } }
      setError(e2.response?.data?.message || t('common.error'))
    }
  }

  return (
    <div className={MODAL_OVERLAY}>
      <div className={`${MODAL_PANEL} max-w-sm`}>
        <h2 className="text-lg font-bold mb-4">{t('groups.editGroup', { name: group.name })}</h2>

        {error && (
          <div className="mb-3 p-2 text-sm text-destructive bg-destructive/10 rounded">{error}</div>
        )}

        <form onSubmit={handleSubmit} className="space-y-3">
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
              disabled={pending || !!nameError}
              className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50"
            >
              {pending ? t('common.saving') : t('common.save')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
