import { useEffect, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@jianmanager/ui/components/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { FieldError, FieldLabel } from '@jianmanager/ui/components/field-label'
import { Input } from '@jianmanager/ui/components/input'
import { ScrollableDialogBody, scrollableDialogContentClass } from '@jianmanager/ui/components/scrollable-dialog'
import { COMMAND_ORCHESTRATION_V1, DEFAULT_STABLE_PROFILE, DEFAULT_STRICT_THRESHOLDS } from '@/lib/bot-load/bot-load-presets'
import type { BotLoadTemplate, BotLoadTemplateInput } from '@/lib/bot-load/bot-load-types'
import { validateCommandSchedule, validateLoadProfile, validateThresholds } from '@/lib/bot-load/bot-load-validation'
import { CommandPlanEditor } from '@/components/views/bot-load/CommandPlanEditor'
import { LoadProfileEditor } from '@/components/views/bot-load/LoadProfileEditor'
import { ThresholdEditor } from '@/components/views/bot-load/ThresholdEditor'

export type TemplateDialogMode = 'create' | 'edit' | 'copy'

export interface TemplateDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 编辑/复制时传入来源模板。 */
  template?: BotLoadTemplate | null
  mode?: TemplateDialogMode
  /**
   * 提交（容器注入 mutation）。返回 `null` 表示成功——展示层随即关闭弹窗；
   * 返回字符串则作为错误文案就地展示。成功提示由容器负责。
   */
  onSubmit: (payload: BotLoadTemplateInput) => Promise<string | null>
}

/** 模板创建/编辑/复制对话框：本地校验通过后交给容器的 mutation。 */
export function TemplateDialog({
  open,
  onOpenChange,
  template,
  mode = 'create',
  onSubmit,
}: TemplateDialogProps) {
  const { t } = useTranslation()

  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [tags, setTags] = useState('')
  const [commandSchedule, setCommandSchedule] = useState(COMMAND_ORCHESTRATION_V1)
  const [loadProfile, setLoadProfile] = useState(DEFAULT_STABLE_PROFILE)
  const [thresholds, setThresholds] = useState(DEFAULT_STRICT_THRESHOLDS)
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (!open) return
    /* eslint-disable react-hooks/set-state-in-effect -- 弹窗打开时一次性回填/清空表单，属合法同步 */
    if ((mode === 'edit' || mode === 'copy') && template) {
      setName(mode === 'copy' ? `${template.name} - ${t('botsLoad.copySuffix')}` : template.name)
      setDescription(template.description)
      setTags(template.tags.join(', '))
      setCommandSchedule(structuredClone(template.commandSchedule))
      setLoadProfile(structuredClone(template.loadProfile))
      setThresholds(structuredClone(template.thresholds))
    } else {
      setName('')
      setDescription('')
      setTags('')
      setCommandSchedule(structuredClone(COMMAND_ORCHESTRATION_V1))
      setLoadProfile({ ...DEFAULT_STABLE_PROFILE })
      setThresholds({ ...DEFAULT_STRICT_THRESHOLDS })
    }
    setError('')
    /* eslint-enable react-hooks/set-state-in-effect */
  }, [open, mode, template, t])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    if (!name.trim()) {
      setError(t('botsLoad.templateNameRequired'))
      return
    }
    const scheduleErrs = validateCommandSchedule(commandSchedule)
    const profileErrs = validateLoadProfile(loadProfile)
    const thrErrs = validateThresholds(thresholds)
    if (scheduleErrs.length || profileErrs.length || thrErrs.length) {
      setError(scheduleErrs[0]?.message || profileErrs[0]?.message || thrErrs[0]?.message || t('common.error'))
      return
    }
    const payload: BotLoadTemplateInput = {
      name: name.trim(),
      description: description.trim(),
      commandSchedule,
      loadProfile,
      thresholds,
      tags: tags
        .split(/[,，]/)
        .map((s) => s.trim())
        .filter(Boolean),
    }
    setPending(true)
    try {
      const failure = await onSubmit(payload)
      if (failure) setError(failure)
      else onOpenChange(false)
    } finally {
      setPending(false)
    }
  }

  const title =
    mode === 'edit'
      ? t('botsLoad.editTemplate')
      : mode === 'copy'
        ? t('botsLoad.copyTemplate')
        : t('botsLoad.createTemplate')

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-3xl`}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
          <ScrollableDialogBody className="space-y-4 py-1">
            {mode === 'edit' && (
              <p className="rounded border bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
                {t('botsLoad.editDoesNotAffectRuns')}
              </p>
            )}
            {error && <div className="rounded bg-destructive/10 p-2 text-sm text-destructive">{error}</div>}
            <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
              <div className="space-y-1">
                <FieldLabel required htmlFor="tpl-name">
                  {t('common.name')}
                </FieldLabel>
                <Input id="tpl-name" value={name} onChange={(e) => setName(e.target.value)} />
                <FieldError error={!name.trim() ? t('botsLoad.templateNameRequired') : undefined} />
              </div>
              <div className="space-y-1">
                <FieldLabel htmlFor="tpl-tags">{t('botsLoad.tags')}</FieldLabel>
                <Input
                  id="tpl-tags"
                  value={tags}
                  onChange={(e) => setTags(e.target.value)}
                  placeholder={t('botsLoad.tagsPlaceholder')}
                />
              </div>
            </div>
            <div className="space-y-1">
              <FieldLabel htmlFor="tpl-desc">{t('botsLoad.description')}</FieldLabel>
              <Input id="tpl-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
            </div>
            <section className="space-y-2">
              <h3 className="text-sm font-semibold">{t('botsLoad.step_commands')}</h3>
              <CommandPlanEditor value={commandSchedule} onChange={setCommandSchedule} />
            </section>
            <section className="space-y-2">
              <h3 className="text-sm font-semibold">{t('botsLoad.step_profile')}</h3>
              <LoadProfileEditor value={loadProfile} onChange={setLoadProfile} />
            </section>
            <section className="space-y-2">
              <h3 className="text-sm font-semibold">{t('botsLoad.thresholds')}</h3>
              <ThresholdEditor value={thresholds} onChange={setThresholds} />
            </section>
          </ScrollableDialogBody>
          <DialogFooter className="pt-4">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={pending}>
              {pending ? t('common.loading') : t('common.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
