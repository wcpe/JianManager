import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  ScrollableDialogBody,
  scrollableDialogContentClass,
} from '@jianmanager/ui/components/scrollable-dialog'

/** 创建频道请求体（FR-187）。 */
export interface CreateChannelBody {
  channelId: string
  name: string
  description: string
}

/** 创建频道模态的容器注入点。 */
export interface CreateChannelDialogProps {
  open: boolean
  onOpenChange: (v: boolean) => void
  onCreated: (channelId: string) => void
  /** 提交创建（容器注入 mutation）。 */
  onCreate: (body: CreateChannelBody) => Promise<void>
  /** 提交中（禁用提交按钮）。 */
  submitting?: boolean
  /** 结果回执（容器注入 toast；组件库不依赖 toast 实现）。 */
  onNotify?: (level: 'success' | 'error', message: string) => void
}

/** 创建频道模态（FR-187，取代原内联展开表单；内容自适应壳）。 */
export function CreateChannelDialog({
  open,
  onOpenChange,
  onCreated,
  onCreate,
  submitting = false,
  onNotify,
}: CreateChannelDialogProps) {
  const { t } = useTranslation()
  const [channelId, setChannelId] = useState('')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')

  const slugOk = /^[a-z0-9][a-z0-9-]{1,63}$/.test(channelId)
  const canSubmit = slugOk && name.trim() !== '' && !submitting

  const reset = () => {
    setChannelId('')
    setName('')
    setDescription('')
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!canSubmit) return
    try {
      await onCreate({ channelId, name, description })
      onNotify?.('success', t('clientChannels.created', '频道已创建'))
      const created = channelId
      reset()
      onOpenChange(false)
      onCreated(created)
    } catch (err) {
      const message =
        (err as { response?: { data?: { message?: string } } })?.response?.data?.message ||
        t('clientChannels.createFailed', '创建频道失败')
      onNotify?.('error', message)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v: boolean) => {
        if (!v) reset()
        onOpenChange(v)
      }}
    >
      <DialogContent className={cn(scrollableDialogContentClass, 'sm:max-w-lg')}>
        <DialogHeader>
          <DialogTitle>{t('clientChannels.addChannel', '新增频道')}</DialogTitle>
          <DialogDescription>
            {t('clientChannels.createDialogDesc', '为一个服务器创建分发频道；创建后进入工作台继续配置密钥与发布版本。')}
          </DialogDescription>
        </DialogHeader>
        <form id="create-channel-form" onSubmit={submit}>
          <ScrollableDialogBody className="space-y-3">
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.channelId', '频道标识')}
              <input
                className="p-2 border rounded bg-background font-mono aria-invalid:border-destructive"
                placeholder="skyblock-s1"
                aria-invalid={channelId !== '' && !slugOk}
                value={channelId}
                onChange={(e) => setChannelId(e.target.value)}
                autoFocus
              />
              <span className="text-xs text-muted-foreground">
                {t('clientChannels.channelIdHint', '小写字母/数字/连字符，2-64 位，创建后不可改')}
              </span>
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('common.name', '名称')}
              <input
                className="p-2 border rounded bg-background"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
            <label className="flex flex-col gap-1 text-sm">
              {t('clientChannels.description', '描述')}
              <input
                className="p-2 border rounded bg-background"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            </label>
          </ScrollableDialogBody>
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel', '取消')}
          </Button>
          <Button type="submit" form="create-channel-form" disabled={!canSubmit}>
            {t('common.create', '创建')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
