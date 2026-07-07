import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useCreateGroup } from '@/api/groups'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { Input } from '@jianmanager/ui/components/input'
import { Textarea } from '@jianmanager/ui/components/textarea'
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

  const handleClose = () => {
    onClose()
    resetForm()
  }

  return (
    <Dialog open={open} onOpenChange={(v: boolean) => { if (!v) handleClose() }}>
      <DialogContent className="sm:max-w-sm">
        <form onSubmit={handleSubmit} className="space-y-3">
          <DialogHeader>
            <DialogTitle>{t('groups.createGroup')}</DialogTitle>
          </DialogHeader>

          {error && (
            <div className="rounded bg-destructive/10 p-2 text-sm text-destructive">{error}</div>
          )}

          <div>
            <FieldLabel htmlFor="create-group-name" required>{t('common.name')}</FieldLabel>
            <Input
              id="create-group-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="mt-1"
              aria-invalid={!!nameError}
            />
            <FieldError error={nameError} />
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

          <DialogFooter className="pt-2">
            <Button type="button" variant="outline" onClick={handleClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={create.isPending || !!nameError}>
              {create.isPending ? t('common.creating') : t('common.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
