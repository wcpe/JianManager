import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

interface PromptDialogProps {
  open: boolean
  title: string
  /** 初始值（重命名时为旧名）。 */
  initialValue?: string
  /** 校验：返回错误信息字符串则禁用确认，返回空串表示合法。 */
  validate?: (value: string) => string
  onSubmit: (value: string) => void
  onCancel: () => void
}

/** 通用输入对话框（FR-070 新建/重命名）。基于 shadcn Dialog，回车提交、Esc 取消。 */
export default function PromptDialog({
  open,
  title,
  initialValue = '',
  validate,
  onSubmit,
  onCancel,
}: PromptDialogProps) {
  const { t } = useTranslation()
  const [value, setValue] = useState(initialValue)

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 打开时同步初始值，属合法
    if (open) setValue(initialValue)
  }, [open, initialValue])

  const errorMsg = validate ? validate(value) : ''
  const canSubmit = value.trim().length > 0 && !errorMsg

  return (
    <Dialog open={open} onOpenChange={(v: boolean) => { if (!v) onCancel() }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <Input
          autoFocus
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && canSubmit) onSubmit(value.trim())
          }}
          aria-invalid={!!errorMsg}
        />
        {errorMsg && <p className="text-xs text-destructive">{errorMsg}</p>}
        <DialogFooter>
          <Button variant="outline" onClick={onCancel}>
            {t('common.cancel')}
          </Button>
          <Button disabled={!canSubmit} onClick={() => onSubmit(value.trim())}>
            {t('common.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
