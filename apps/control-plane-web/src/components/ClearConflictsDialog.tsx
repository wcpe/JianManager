import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Loader2 } from 'lucide-react'
import { toast } from 'sonner'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Label } from '@jianmanager/ui/components/label'
import { useClearLogIdentityConflicts } from '@/api/logRuntime'

/**
 * 「确认并清空身份冲突」对话框（日志页质量标记的运维入口）。
 *
 * 语义（与后端逐条对齐）：运维**审阅后确认**这些冲突已被正确处理 —— 系统在无法自动裁定的
 * 形态下保留先到者并跳过冲突行，即保留的是正确版本；冲突行从未进入写入面。故清空只移除
 * **诊断记录**（质量标记随之消失），不改动任何日志内容。
 *
 * 两个红线的 UI 对应：
 * 1. **理由是必填**：后端缺理由即拒；这里在前端也拦（避免一次必然失败的往返），
 *    并说明它进审计凭据 —— 不让「清空」看起来像一次无痕的动作。
 * 2. **破坏性操作二次确认**：走独立的 Dialog + 不可逆说明，不复用普通按钮；
 *    理由输入本身也构成一次显式确认（不能一路回车点过去）。
 */
export interface ClearConflictsDialogProps {
  /** 目标节点；null 时不渲染（无节点上下文就没有可清的源）。 */
  nodeId: number | null
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 单源清空时传入完整源键；省略/undefined = 清该节点全部持有冲突记录的源。 */
  sourceKey?: string
  /** 成功后回调（供调用方刷新查询）。 */
  onCleared?: (clearedCount: number, skipped: number) => void
}

export function ClearConflictsDialog({
  nodeId,
  open,
  onOpenChange,
  sourceKey,
  onCleared,
}: ClearConflictsDialogProps) {
  const { t } = useTranslation()
  const reasonId = useId()
  const [reason, setReason] = useState('')
  const clear = useClearLogIdentityConflicts(nodeId ?? 0)

  // 每次打开重置理由：上次输入残留会让「二次确认」退化成一次点击。
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 弹窗 open 变化时重置输入，属合法同步
    if (open) setReason('')
  }, [open])

  const trimmed = reason.trim()
  const canConfirm = trimmed.length > 0 && nodeId !== null && !clear.isPending

  const handleConfirm = () => {
    if (!canConfirm) return
    clear.mutate(
      { storageNamespace: sourceKey, reason: trimmed },
      {
        onSuccess: (result) => {
          const skipped = result.skippedSources?.length ?? 0
          if (skipped > 0) {
            // 跳过的源必须显式告知：那意味着期间有**未经审阅**的新冲突写入，
            // 只报"成功"会让人误以为整批都清干净了。
            toast.info(
              t('logsFederation.clearConflicts.partial', {
                count: result.clearedCount,
                skipped,
              }),
            )
          } else {
            toast.success(
              t('logsFederation.clearConflicts.success', { count: result.clearedCount }),
            )
          }
          onOpenChange(false)
          onCleared?.(result.clearedCount, skipped)
        },
        onError: (error) => {
          const response = (error as { response?: { data?: { message?: string; error?: string } } })
            ?.response
          const detail = response?.data?.message || response?.data?.error
          toast.error(
            detail
              ? `${t('logsFederation.clearConflicts.failed')}: ${detail}`
              : t('logsFederation.clearConflicts.failed'),
          )
        },
      },
    )
  }

  return (
    <Dialog open={open} onOpenChange={(v: boolean) => { if (!v) onOpenChange(false) }}>
      <DialogContent data-testid="logs-clear-conflicts-dialog">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <AlertTriangle className="size-5 text-destructive" aria-hidden />
            {t('logsFederation.clearConflicts.title')}
          </DialogTitle>
          <DialogDescription>
            {t('logsFederation.clearConflicts.description')}
          </DialogDescription>
        </DialogHeader>

        {!sourceKey && (
          <p className="text-xs text-muted-foreground" data-testid="logs-clear-conflicts-scope">
            {t('logsFederation.clearConflicts.scopeHint')}
          </p>
        )}

        <div className="space-y-2">
          <Label htmlFor={reasonId} className="text-sm">
            {t('logsFederation.clearConflicts.reasonLabel')}
          </Label>
          <Input
            id={reasonId}
            data-testid="logs-clear-conflicts-reason"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={t('logsFederation.clearConflicts.reasonPlaceholder')}
            autoComplete="off"
            autoFocus
            aria-invalid={reason.length > 0 && trimmed.length === 0}
          />
          {reason.length > 0 && trimmed.length === 0 && (
            <p className="text-xs text-destructive" data-testid="logs-clear-conflicts-reason-required">
              {t('logsFederation.clearConflicts.reasonRequired')}
            </p>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button
            variant="destructive"
            data-testid="logs-clear-conflicts-confirm"
            disabled={!canConfirm}
            onClick={handleConfirm}
          >
            {clear.isPending && <Loader2 className="size-4 animate-spin" aria-hidden />}
            {t('logsFederation.clearConflicts.confirmLabel')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export default ClearConflictsDialog
