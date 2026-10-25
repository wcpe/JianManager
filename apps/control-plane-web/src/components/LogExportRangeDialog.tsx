import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Label } from '@jianmanager/ui/components/label'

/** 输入框显示 UTC 墙钟；不经浏览器本地时区转换。 */
function utcInput(value: string | undefined, fallback: number): string {
  return new Date(value ?? fallback).toISOString().slice(0, 19)
}

/** 显式选择导出时间段，使用设计系统既有自适应表单壳。 */
export function LogExportRangeDialog({ open, from, to, pending, onOpenChange, onConfirm }: {
  open: boolean
  from?: string
  to?: string
  pending: boolean
  onOpenChange: (open: boolean) => void
  onConfirm: (bounds: { from: string; to: string }) => void
}) {
  const { t } = useTranslation()
  const [start, setStart] = useState(() => utcInput(from, Date.now() - 15 * 60_000))
  const [end, setEnd] = useState(() => utcInput(to, Date.now()))
  const startMs = Date.parse(`${start}Z`)
  const endMs = Date.parse(`${end}Z`)
  const valid = Number.isFinite(startMs) && Number.isFinite(endMs) && startMs < endMs
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={scrollableDialogContentClass}>
        <DialogHeader>
          <DialogTitle>{t('logs.exportRangeTitle')}</DialogTitle>
          <DialogDescription>{t('logs.exportRangeHelp')}</DialogDescription>
        </DialogHeader>
        <ScrollableDialogBody className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="logs-export-from">{t('logs.exportRangeFrom')}</Label>
            <Input id="logs-export-from" type="datetime-local" step="1" value={start} disabled={pending} onChange={(event) => setStart(event.target.value)} />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="logs-export-to">{t('logs.exportRangeTo')}</Label>
            <Input id="logs-export-to" type="datetime-local" step="1" value={end} disabled={pending} onChange={(event) => setEnd(event.target.value)} />
          </div>
          {!valid && <p role="alert" className="text-sm text-destructive">{t('logs.exportRangeInvalid')}</p>}
        </ScrollableDialogBody>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>{t('common.cancel')}</Button>
          <Button disabled={!valid || pending} onClick={() => onConfirm({ from: new Date(startMs).toISOString(), to: new Date(endMs).toISOString() })}>
            {pending ? t('logs.exporting') : t('logs.exportRangeConfirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
