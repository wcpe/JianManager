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
import {
  ScrollableDialogBody,
  scrollableDialogContentClass,
} from '@jianmanager/ui/components/scrollable-dialog'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { Input } from '@jianmanager/ui/components/input'
import { Textarea } from '@jianmanager/ui/components/textarea'
import { validateRequired } from '@/lib/form-validation'
import { useFieldGate } from '@/lib/use-field-gate'

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
  const gate = useFieldGate()

  const resetForm = () => {
    setName('')
    setDescription('')
    setError('')
    gate.reset()
  }

  const nameError = validateRequired(name)

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    gate.submit()
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
            <Button type="submit" disabled={create.isPending || !!nameError}>
              {create.isPending ? t('common.creating') : t('common.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
