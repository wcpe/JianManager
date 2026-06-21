import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Plus, Trash2 } from 'lucide-react'
import {
  useTemplates,
  useCreateTemplate,
  useDeleteTemplate,
  type TemplateInfo,
} from '@/api/templates'
import { Panel } from '@/components/ui/panel'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@/components/ui/scrollable-dialog'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { FieldLabel, FieldError } from '@/components/ui/field-label'
import { validateRequired, validateUrl, validateAbsPath, validateFields, hasErrors } from '@/lib/form-validation'
import DangerConfirm from '@/components/DangerConfirm'

/** 服务端模板管理页（FR-064）：新建/删除模板，套 FR-061 高密度风格。 */
export default function TemplatesPage() {
  const { t } = useTranslation()
  const { data: templates, isLoading } = useTemplates()
  const [createOpen, setCreateOpen] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<TemplateInfo | null>(null)
  const del = useDeleteTemplate()

  const confirmDelete = () => {
    if (!deleteTarget) return
    del.mutate(deleteTarget.id, {
      onSuccess: () => toast.success(t('templates.deleted')),
      onError: () => toast.error(t('templates.deleteFailed')),
    })
    setDeleteTarget(null)
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-2">
        <div>
          <h1 className="text-xl font-bold">{t('templates.title')}</h1>
          <p className="text-xs text-muted-foreground">{t('templates.subtitle')}</p>
        </div>
        <Button size="sm" onClick={() => setCreateOpen(true)}>
          <Plus />
          {t('templates.create')}
        </Button>
      </div>

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : !templates || templates.length === 0 ? (
        <Panel>
          <p className="py-8 text-center text-sm text-muted-foreground">{t('templates.empty')}</p>
        </Panel>
      ) : (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-3">
          {templates.map((tpl) => (
            <Panel
              key={tpl.id}
              title={
                <span className="flex items-center gap-2">
                  <span className="text-foreground">{tpl.name}</span>
                  <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] font-normal text-muted-foreground">
                    {tpl.type}
                  </span>
                </span>
              }
              actions={
                <Button
                  variant="ghost"
                  size="icon-xs"
                  className="text-muted-foreground hover:text-destructive"
                  onClick={() => setDeleteTarget(tpl)}
                  aria-label={t('common.delete')}
                >
                  <Trash2 />
                </Button>
              }
              bodyClassName="space-y-2 p-3"
            >
              {tpl.description && (
                <p className="text-xs text-muted-foreground">{tpl.description}</p>
              )}
              <div className="overflow-hidden text-ellipsis whitespace-nowrap rounded bg-muted p-2 font-mono text-xs">
                {tpl.startCommand}
              </div>
              {tpl.downloadUrl && (
                <a
                  href={tpl.downloadUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-block text-xs text-primary hover:underline"
                >
                  {t('templates.downloadLink')}
                </a>
              )}
            </Panel>
          ))}
        </div>
      )}

      {createOpen && <CreateTemplateDialog onClose={() => setCreateOpen(false)} />}

      <DangerConfirm
        open={deleteTarget !== null}
        title={t('templates.deleteConfirm', { name: deleteTarget?.name ?? '' })}
        description={t('templates.deleteDescription')}
        confirmLabel={t('common.delete')}
        onConfirm={confirmDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

/** 新建模板对话框：名称/类型/描述/启动命令/下载URL/默认工作目录。 */
function CreateTemplateDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation()
  const { data: templates } = useTemplates()
  const create = useCreateTemplate()
  const [name, setName] = useState('')
  const [type, setType] = useState('minecraft_java')
  const [description, setDescription] = useState('')
  const [startCommand, setStartCommand] = useState('')
  const [downloadUrl, setDownloadUrl] = useState('')
  const [defaultWorkDir, setDefaultWorkDir] = useState('')

  // 类型可编辑下拉：内置常用类型 + 已有模板出现过的类型去重（FR-072）。
  const typeOptions: ComboboxOption[] = Array.from(
    new Set(['minecraft_java', 'generic', ...((templates ?? []).map((tpl) => tpl.type).filter(Boolean))]),
  ).map((v) => ({ value: v }))

  const errors = validateFields(
    { name, type, startCommand, downloadUrl, defaultWorkDir },
    {
      name: [validateRequired],
      type: [validateRequired],
      startCommand: [validateRequired],
      downloadUrl: [validateUrl],
      defaultWorkDir: [validateAbsPath],
    },
  )

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (hasErrors(errors)) return
    create.mutate(
      {
        name,
        type,
        description: description || undefined,
        startCommand,
        downloadUrl: downloadUrl || undefined,
        defaultWorkDir: defaultWorkDir || undefined,
      },
      {
        onSuccess: () => {
          toast.success(t('templates.created'))
          onClose()
        },
        onError: () => toast.error(t('templates.createFailed')),
      },
    )
  }

  return (
    <Dialog open onOpenChange={(v) => { if (!v) onClose() }}>
      <DialogContent className={scrollableDialogContentClass}>
        <DialogHeader>
          <DialogTitle>{t('templates.createTitle')}</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
          <ScrollableDialogBody className="space-y-3 py-1">
            <div className="space-y-1.5">
              <FieldLabel required htmlFor="tpl-name">{t('templates.name')}</FieldLabel>
              <Input
                id="tpl-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t('templates.namePlaceholder')}
                aria-invalid={!!errors.name}
              />
              <FieldError error={errors.name} />
            </div>
            <div className="space-y-1.5">
              <FieldLabel required htmlFor="tpl-type">{t('templates.type')}</FieldLabel>
              <Combobox
                id="tpl-type"
                options={typeOptions}
                value={type}
                onChange={setType}
                placeholder={t('templates.typePlaceholder')}
                invalid={!!errors.type}
              />
              <FieldError error={errors.type} />
            </div>
            <div className="space-y-1.5">
              <FieldLabel htmlFor="tpl-desc">{t('templates.description')}</FieldLabel>
              <Input
                id="tpl-desc"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder={t('templates.descriptionPlaceholder')}
              />
            </div>
            <div className="space-y-1.5">
              <FieldLabel required htmlFor="tpl-cmd">{t('templates.startCommand')}</FieldLabel>
              <Textarea
                id="tpl-cmd"
                value={startCommand}
                onChange={(e) => setStartCommand(e.target.value)}
                placeholder={t('templates.startCommandPlaceholder')}
                className="font-mono text-xs"
                rows={2}
                aria-invalid={!!errors.startCommand}
              />
              <FieldError error={errors.startCommand} />
            </div>
            <div className="space-y-1.5">
              <FieldLabel htmlFor="tpl-url">{t('templates.downloadUrl')}</FieldLabel>
              <Input
                id="tpl-url"
                value={downloadUrl}
                onChange={(e) => setDownloadUrl(e.target.value)}
                placeholder={t('templates.downloadUrlPlaceholder')}
                aria-invalid={!!errors.downloadUrl}
              />
              <FieldError error={errors.downloadUrl} />
            </div>
            <div className="space-y-1.5">
              <FieldLabel htmlFor="tpl-workdir">{t('templates.defaultWorkDir')}</FieldLabel>
              <Input
                id="tpl-workdir"
                value={defaultWorkDir}
                onChange={(e) => setDefaultWorkDir(e.target.value)}
                placeholder={t('templates.defaultWorkDirPlaceholder')}
                aria-invalid={!!errors.defaultWorkDir}
              />
              <FieldError error={errors.defaultWorkDir} />
            </div>
          </ScrollableDialogBody>
          <DialogFooter className="pt-4">
            <Button type="button" variant="outline" onClick={onClose}>
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
