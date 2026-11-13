import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { TemplateDialog as TemplateDialogView } from '@/components/views/bot-load/TemplateDialog'
import { useCreateBotLoadTemplate, useUpdateBotLoadTemplate, type BotLoadTemplate, type BotLoadTemplateInput } from '@/api/botLoad'

interface TemplateDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 编辑时传入；复制时传入 source 且 mode=copy */
  template?: BotLoadTemplate | null
  mode?: 'create' | 'edit' | 'copy'
}

/**
 * 模板对话框接线层：本地校验与表单已回迁应用侧，此处只注入 create/update mutation，
 * 并把失败原因（服务端 message 优先）作为字符串回传给展示层就地展示。
 */
export default function TemplateDialog({
  open,
  onOpenChange,
  template,
  mode = 'create',
}: TemplateDialogProps) {
  const { t } = useTranslation()
  const createTpl = useCreateBotLoadTemplate()
  const updateTpl = useUpdateBotLoadTemplate()

  const onSubmit = (payload: BotLoadTemplateInput) =>
    new Promise<string | null>((resolve) => {
      const onSuccess = () => {
        toast.success(mode === 'edit' ? t('botsLoad.templateUpdated') : t('botsLoad.templateCreated'))
        resolve(null)
      }
      const onError = (err: unknown) => {
        const msg =
          err && typeof err === 'object' && 'response' in err
            ? (err as { response?: { data?: { message?: string } } }).response?.data?.message
            : undefined
        resolve(msg || t('botsLoad.templateSaveFailed'))
      }
      if (mode === 'edit' && template) {
        updateTpl.mutate({ id: template.id, payload }, { onSuccess, onError })
      } else {
        createTpl.mutate(payload, { onSuccess, onError })
      }
    })

  return (
    <TemplateDialogView
      open={open}
      onOpenChange={onOpenChange}
      template={template}
      mode={mode}
      onSubmit={onSubmit}
    />
  )
}
