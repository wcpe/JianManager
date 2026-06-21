import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useCreateGroup } from '@/api/groups'
import { MODAL_OVERLAY, MODAL_PANEL } from '@/components/ui/scrollable-dialog'
import { FieldLabel, FieldError } from '@/components/ui/field-label'
import { validateRequired } from '@/lib/form-validation'

interface CreateGroupDialogProps {
  open: boolean
  onClose: () => void
}

export default function CreateGroupDialog({ open, onClose }: CreateGroupDialogProps) {
  const { t } = useTranslation()
  const create = useCreateGroup()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [error, setError] = useState('')

  const resetForm = () => {
    setName('')
    setDescription('')
    setError('')
  }

  const nameError = validateRequired(name)

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (nameError) return
    setError('')
    create.mutate(
      { name, description },
      {
        onSuccess: () => {
          onClose()
          resetForm()
        },
        onError: (err: Error & { response?: { data?: { message?: string } } }) => {
          setError(err.response?.data?.message || t('groups.createFailed', t('common.error')))
        },
      },
    )
  }

  if (!open) return null

  return (
    <div className={MODAL_OVERLAY}>
      <div className={`${MODAL_PANEL} max-w-sm`}>
        <h2 className="text-lg font-bold mb-4">{t('groups.createGroup')}</h2>

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
            <FieldLabel>{t('groups.description', 'Description')}</FieldLabel>
            <textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
              rows={3}
            />
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
              disabled={create.isPending || !!nameError}
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
