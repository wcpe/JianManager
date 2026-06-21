import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, ShieldAlert } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useDangerPermission, type DangerScope } from '@/lib/danger'

/**
 * 统一危险操作确认组件（FR-059）。
 *
 * 在既有 shadcn/ui Dialog 之上，把零散的二次确认收敛为一处：
 * - 普通破坏性操作：二次确认 + destructive 主按钮；
 * - 高危操作（传 `confirmText`，如删实例/删节点/批量 kill）：要求逐字输入资源名称二次校验；
 * - 角色门禁（传 `scope`）：组成员对越权范围的危险操作被禁用并提示，最终拒绝仍由后端 RBAC 强制。
 *
 * 文案全部走 i18n（danger 命名空间 + common），颜色用主题 CSS 变量，暗/亮色自适应。
 */
export interface DangerConfirmProps {
  /** 是否打开。 */
  open: boolean
  /** 标题。 */
  title: string
  /** 描述（说明影响与不可逆性）。可选，缺省回落到 common.irreversible。 */
  description?: string
  /** 确认按钮文案，缺省 common.delete。 */
  confirmLabel?: string
  /**
   * 高危二次校验：需用户原样输入此文本（通常为资源名称）后才能确认。
   * 省略时为普通二次确认，不要求输入。
   */
  confirmText?: string
  /**
   * 角色门禁范围：'group'（组管理员+，如删实例/删备份）或 'platform'（仅平台管理员，如删用户/删节点）。
   * 省略时不做前端角色门禁（仅二次确认）。
   */
  scope?: DangerScope
  /** 确认回调（仅在允许且校验通过时可触发）。 */
  onConfirm: () => void
  /** 取消/关闭回调。 */
  onCancel: () => void
}

/** 统一的危险操作确认弹窗，替代 window.confirm 与零散内联 ConfirmDialog。 */
export default function DangerConfirm({
  open,
  title,
  description,
  confirmLabel,
  confirmText,
  scope,
  onConfirm,
  onCancel,
}: DangerConfirmProps) {
  const { t } = useTranslation()
  const inputId = useId()
  const [typed, setTyped] = useState('')
  const permission = useDangerPermission(scope ?? 'group')

  // 仅当声明了 scope 时才做前端门禁；未声明视为允许（普通二次确认）。
  const denied = scope !== undefined && !permission.allowed

  // 每次打开重置输入，避免上次输入残留导致直接可确认。
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 弹窗 open 变化时重置输入，属合法同步
    if (open) setTyped('')
  }, [open])

  const textMatched = !confirmText || typed === confirmText
  const canConfirm = !denied && textMatched

  const handleConfirm = () => {
    if (!canConfirm) return
    onConfirm()
  }

  return (
    <Dialog open={open} onOpenChange={(v: boolean) => { if (!v) onCancel() }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <AlertTriangle className="size-5 text-destructive" aria-hidden />
            {title}
          </DialogTitle>
          <DialogDescription>{description ?? t('common.irreversible')}</DialogDescription>
        </DialogHeader>

        {denied ? (
          <div className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
            <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
            <span>{t('danger.denied')}</span>
          </div>
        ) : (
          confirmText && (
            <div className="space-y-2">
              <Label htmlFor={inputId} className="text-sm">
                {t('danger.typeToConfirm', { name: confirmText })}
              </Label>
              <Input
                id={inputId}
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                placeholder={confirmText}
                autoComplete="off"
                autoFocus
                aria-invalid={typed.length > 0 && !textMatched}
              />
            </div>
          )
        )}

        <DialogFooter>
          <Button variant="outline" onClick={onCancel}>
            {t('common.cancel')}
          </Button>
          <Button variant="destructive" disabled={!canConfirm} onClick={handleConfirm}>
            {confirmLabel ?? t('common.delete')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
