import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Loader2, ShieldAlert } from 'lucide-react'
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

/**
 * 危险操作的「权限范围」分级（FR-059 角色门禁）。
 *
 * 与后端 model.UserRole（0 组成员 / 1 组管理员 / 10 平台管理员）对齐。
 * 前端门禁仅用于在 UI 上提前禁用/提示越权操作，最终拒绝由 Control Plane 的 RBAC 强制。
 */
export type DangerScope = 'group' | 'platform'

/**
 * 统一危险操作确认弹窗（FR-059）。
 *
 * 在既有 Dialog 之上，把零散的二次确认收敛为一处：
 * - 普通破坏性操作：二次确认 + destructive 主按钮；
 * - 高危操作（传 `confirmText`，如删实例/删节点/批量 kill）：要求逐字输入资源名称二次校验；
 * - 角色门禁（传 `scope` + `allowed`）：越权时禁用并提示。
 *
 * 受控（ADR-097）：**不自行判定权限**——`allowed` 由外壳注入（应用侧读登录态角色后判定）。
 * 组件库不持有鉴权状态，故本组件可在无登录态的环境（组件博物馆）独立渲染。
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
  /**
   * 角色门禁判定结果（外壳注入）。为 false 时禁用确认并展示越权提示。
   * 仅当声明了 `scope` 时参与判定；缺省 true（视为允许）。
   */
  allowed?: boolean
  /** 确认回调（仅在允许且校验通过时可触发）。 */
  onConfirm: () => void
  /** 取消/关闭回调。 */
  onCancel: () => void
  /** 确认操作在途：为真时确认按钮旋转并禁用，防止删除/停止等在途重复提交。 */
  pending?: boolean
}

/** 统一的危险操作确认弹窗，替代 window.confirm 与零散内联 ConfirmDialog。 */
export default function DangerConfirm({
  open,
  title,
  description,
  confirmLabel,
  confirmText,
  scope,
  allowed = true,
  onConfirm,
  onCancel,
  pending = false,
}: DangerConfirmProps) {
  const { t } = useTranslation()
  const inputId = useId()
  const [typed, setTyped] = useState('')
  // 仅当声明了 scope 时才做前端门禁；未声明视为允许（普通二次确认）。
  const denied = scope !== undefined && !allowed

  // 每次打开重置输入，避免上次输入残留导致直接可确认。
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 弹窗 open 变化时重置输入，属合法同步
    if (open) setTyped('')
  }, [open])

  const textMatched = !confirmText || typed === confirmText
  const canConfirm = !denied && textMatched

  const handleConfirm = () => {
    if (!canConfirm || pending) return
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
          <Button variant="destructive" disabled={!canConfirm || pending} onClick={handleConfirm}>
            {pending && <Loader2 className="size-4 animate-spin" aria-hidden />}
            {confirmLabel ?? t('common.delete')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
