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
} from '@jianmanager/ui/components/dialog'
import { Alert, AlertDescription } from '@jianmanager/ui/components/alert'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Label } from '@jianmanager/ui/components/label'
import { Spinner } from '@jianmanager/ui/components/spinner'
import { useDangerPermission, type DangerScope } from '@/lib/shared/danger'

/**
 * 统一危险操作确认弹窗（FR-059）。
 *
 * 在既有 Dialog 之上，把零散的二次确认收敛为一处：
 * - 普通破坏性操作：二次确认 + destructive 主按钮；
 * - 高危操作（传 `confirmText`，如删实例/删节点/批量 kill）：要求逐字输入资源名称二次校验；
 * - 角色门禁（传 `scope`）：越权时禁用并提示。
 *
 * 门禁自行判定：本组件直接读登录态角色。早先一轮决策曾把角色判定留在外壳、
 * 由 props 注入 `allowed`，结果是外壳漏传即静默放行——全仓 17 处传了 `scope`
 * 却没接上判定，门禁空转且无测试或类型能发现。角色是渲染决策的一部分，
 * 收进组件本身后该失效模式从结构上消失；后端 RBAC 仍是最终防线。
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
  onConfirm,
  onCancel,
  pending = false,
}: DangerConfirmProps) {
  const { t } = useTranslation()
  const inputId = useId()
  const [typed, setTyped] = useState('')
  // 自行判定角色（hook 必须无条件调用，故 scope 缺省时按最宽松的 group 取值）。
  const { allowed } = useDangerPermission(scope ?? 'group')
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
          // 越权提示：原为手写 `border-destructive/40 bg-destructive/10` 块，改用包的 destructive 档。
          //
          // 【视觉会变淡，这是有意接受的一致性代价】包的 destructive 变体是 `bg-card text-destructive`
          // 加 base 层的 --border，即「卡片底 + 红字」，没有淡红底。本处因此从「淡红底红边」变为
          // 「卡片底红字」——与第一批已改的 CapacityPlan / SystemUpdatePageView 保持同一套破坏性提示外观，
          // 也把"破坏性提示长什么样"的决定权收归组件一处（否则每处都在各自微调红底浓度）。
          // 醒目度由三处兜住：红字、DialogTitle 里的红三角图标、以及这道提示出现时确认按钮必然 disabled。
          //
          // 内边距压回 px-3 py-2：DialogContent 本身是 `p-6` + `grid gap-4`，这条提示是弹窗正文里的一格。
          // 若沿用 Alert 默认的 px-4 py-3，它会比上方 DialogHeader 与下方 DialogFooter 各多缩进一段
          // （6-4=2px 与 6-3=3px 的错位），弹窗内两侧对齐被打散；保留原内边距则与替换前逐像素一致，
          // 不存在叠加问题——className 走 twMerge，px/py 是覆盖而非累加。
          //
          // 图标位置：原 flex 里的 ShieldAlert 改为 Alert 的首个 svg 子元素——组件对 `has-[>svg]` 自动切
          // 三列栅格（图标列 + 文案列），等价于原来的 `flex items-start gap-2`，不必再手写 flex。
          //
          // 保留组件默认 role="alert"：本弹窗内没有第二种 alert/status 语义块，不与既有 a11y 断言争位；
          // 越权是「点了删除才浮现的阻断结论」，值得被播报一次。
          <Alert variant="destructive" className="px-3 py-2">
            <ShieldAlert aria-hidden />
            <AlertDescription className="text-sm">{t('danger.denied')}</AlertDescription>
          </Alert>
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
            {pending && <Spinner aria-hidden />}
            {confirmLabel ?? t('common.delete')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
