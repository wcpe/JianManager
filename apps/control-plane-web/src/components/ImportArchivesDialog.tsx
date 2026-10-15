import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Loader2, PackageOpen } from 'lucide-react'
import { toast } from 'sonner'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { ScrollableDialogBody, scrollableDialogContentClass } from '@jianmanager/ui/components/scrollable-dialog'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Label } from '@jianmanager/ui/components/label'
import { useImportLogArchives } from '@/api/logRuntime'

/**
 * 「手动导入归档（.gz）」对话框（FR-504）。
 *
 * 语义：把「已经躺在源目录里、但还没被导入」的历史归档走一遍与自动路径完全相同的导入管道。
 * **不受 gz 导入开关影响** —— 应急补历史永远可用。
 *
 * 为什么需要这个入口（真实缺口，不是补个按钮而已）：定时扫描默认关，且采集轮的历史导入受
 * acquire 层不变量约束（历史段只能前置到**未被读过**的当前段）而不覆盖存量源 —— 二者叠加后
 * 存量源在自动路径上没有任何出口，唯一出路是手动；而该入口过去只有 Worker gRPC，面板不可达。
 * 这正是生产「814 个 gz 从未被导入」的第三块拼图。
 *
 * 两条红线与后端逐条对齐：
 * 1. **理由必填**：后端缺理由即拒；前端先拦，避免一次必然失败的往返，并说明它进审计凭据。
 * 2. **不假装即时完成**：导入按 WAL 余量分批、可能跨多个采集轮，故文案明确"已触发"而非
 *    "已完成"，并把核对口径指向 Worker 日志 —— 让运维知道去哪里看真实进度。
 */
export interface ImportArchivesDialogProps {
  /** 目标节点；null 时不渲染（无节点上下文就没有可导的源）。 */
  nodeId: number | null
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 单源导入时传入完整源键；省略/undefined = 该节点全部可导入源（仅平台管理员）。 */
  sourceKey?: string
}

export function ImportArchivesDialog({ nodeId, open, onOpenChange, sourceKey }: ImportArchivesDialogProps) {
  const { t } = useTranslation()
  const reasonId = useId()
  const [reason, setReason] = useState('')
  const importArchives = useImportLogArchives(nodeId ?? 0)

  // 每次打开重置理由：上次输入残留会让"显式确认"退化成一次点击。
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 弹窗 open 变化时重置输入，属合法同步
    if (open) setReason('')
  }, [open])

  const trimmed = reason.trim()
  const canConfirm = trimmed.length > 0 && nodeId !== null && !importArchives.isPending

  const handleConfirm = () => {
    if (!canConfirm) return
    importArchives.mutate(
      { storageNamespace: sourceKey, reason: trimmed },
      {
        onSuccess: (result) => {
          toast.success(result?.message || t('logsFederation.importArchives.triggered'))
          onOpenChange(false)
        },
        onError: (error) => {
          const response = (error as { response?: { data?: { message?: string; error?: string } } })
            ?.response
          const detail = response?.data?.message || response?.data?.error
          toast.error(
            detail
              ? `${t('logsFederation.importArchives.failed')}: ${detail}`
              : t('logsFederation.importArchives.failed'),
          )
        },
      },
    )
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v: boolean) => {
        if (!v) onOpenChange(false)
      }}
    >
      <DialogContent data-testid="logs-import-archives-dialog" className={scrollableDialogContentClass}>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <PackageOpen className="size-5 text-primary" aria-hidden />
            {t('logsFederation.importArchives.title')}
          </DialogTitle>
          <DialogDescription>{t('logsFederation.importArchives.description')}</DialogDescription>
        </DialogHeader>

        <ScrollableDialogBody>
          <p className="text-xs text-muted-foreground" data-testid="logs-import-archives-scope">
            {sourceKey
              ? t('logsFederation.importArchives.scopeSource')
              : t('logsFederation.importArchives.scopeNode')}
          </p>

          <div className="space-y-2">
            <Label htmlFor={reasonId} className="text-sm">
              {t('logsFederation.importArchives.reasonLabel')}
            </Label>
            <Input
              id={reasonId}
              data-testid="logs-import-archives-reason"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder={t('logsFederation.importArchives.reasonPlaceholder')}
              autoComplete="off"
              autoFocus
              aria-invalid={reason.length > 0 && trimmed.length === 0}
            />
            {reason.length > 0 && trimmed.length === 0 && (
              <p className="text-xs text-destructive" data-testid="logs-import-archives-reason-required">
                {t('logsFederation.importArchives.reasonRequired')}
              </p>
            )}
          </div>
        </ScrollableDialogBody>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button
            data-testid="logs-import-archives-confirm"
            disabled={!canConfirm}
            onClick={handleConfirm}
          >
            {importArchives.isPending && <Loader2 className="size-4 animate-spin" aria-hidden />}
            {t('logsFederation.importArchives.confirmLabel')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export default ImportArchivesDialog
