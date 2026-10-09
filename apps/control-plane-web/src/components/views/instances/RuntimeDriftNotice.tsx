import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Wrench } from 'lucide-react'

import DangerConfirm from '@/components/views/DangerConfirm'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { cn } from '@jianmanager/ui'

/**
 * 运行态漂移提示（FR-471）。
 *
 * 语义：`runtimeDriftPid > 0` 表示该实例**工作目录下存在未被平台纳管的活进程**——
 * 典型场景是运维用 tmux/脚本手工启服，平台 DB 记为 STOPPED 而磁盘实际在跑。
 * 此时面板状态与实际不一致，且直接「启动」会双开（后端启动预检会拦，但前端应尽早暴露）。
 *
 * 「接管」是写操作且有副作用：先停止该外在进程、再以受管方式重新拉起（运行中的服务器会被重启），
 * 故一律经统一危险操作确认（FR-059 DangerConfirm）后下发，绝不直发请求。
 * 文案全部走 i18n、配色走 `--status-warning` 令牌，明暗双主题自适应。
 *
 * 受控（ADR-097）：接管动作经 `onAdopt` 上报、在途态经 props 注入；
 * 三处漂移信息（pid / cmdline）用结构化子集声明，外壳传 `RuntimeDriftInfo` 会结构兼容。
 */

/** 漂移信息（本组件所需的最小结构）。 */
export interface RuntimeDriftInfoView {
  /** 未被纳管的活进程 pid（> 0 才有意义）。 */
  pid: number
  /** 该进程的命令行摘要（可空）。 */
  cmdline?: string
}

/** 紧凑标记（列表/卡片行内用）：琥珀 chip + tooltip 说明 PID 与命令行；不含接管入口，避免行内状态爆炸。 */
export function RuntimeDriftBadge({ pid, cmdline, className }: RuntimeDriftInfoView & { className?: string }) {
  const { t } = useTranslation()
  return (
    <Badge
      variant="outline"
      data-testid="runtime-drift-badge"
      className={cn('border-status-warning/50 bg-status-warning/10 text-status-warning', className)}
      title={t('serverConsole.runtimeDriftBadgeTip', { pid }) + (cmdline ? `\n${cmdline}` : '')}
    >
      <AlertTriangle />
      {t('serverConsole.runtimeDriftBadge')}
    </Badge>
  )
}

/**
 * 「接管」写操作按钮：自持二次确认框，接管本身经回调上报。
 *
 * 抽成组件而非在调用点各抄一遍（确认框 + mutate + toast 三件套），保证入口行为与文案一致。
 * 列表页的接管入口走行内「⋯」菜单 + 页面级 DangerConfirm（与强杀同款），因为它需要
 * 与菜单的待确认状态同源；本组件服务于需要「就地一个按钮」的场景（当前为详情页横幅）。
 */
export interface RuntimeDriftAdoptButtonProps {
  /** 实例展示名（确认框标题用）。 */
  instanceName: string
  /** 未被纳管的活进程 pid。 */
  pid: number
  /** 实例写权限（FR-432）：无权限时按钮禁用并给 tooltip，最终仍由后端 RBAC 兜底。 */
  canOperate: boolean
  size?: 'sm' | 'xs'
  className?: string
  /** 接管在途：按钮禁用。 */
  adopting?: boolean
  /** 发起接管。返回是否成功（提示由外壳给出）。 */
  onAdopt: () => Promise<boolean>
}

export function RuntimeDriftAdoptButton({
  instanceName,
  pid,
  canOperate,
  size = 'sm',
  className,
  adopting = false,
  onAdopt,
}: RuntimeDriftAdoptButtonProps) {
  const { t } = useTranslation()
  const [confirmOpen, setConfirmOpen] = useState(false)

  return (
    <>
      <span title={!canOperate ? t('permissions.operateDenied') : t('serverConsole.runtimeDriftAdoptHint')}>
        <Button
          size={size}
          variant="outline"
          data-testid="runtime-drift-adopt"
          disabled={!canOperate || adopting}
          className={cn('border-status-warning/50 text-status-warning hover:bg-status-warning/15', className)}
          onClick={() => setConfirmOpen(true)}
        >
          <Wrench className="size-3.5" />
          {t('serverConsole.runtimeDriftAdopt')}
        </Button>
      </span>

      <DangerConfirm
        open={confirmOpen}
        title={t('serverConsole.runtimeDriftAdoptTitle', { name: instanceName })}
        description={t('serverConsole.runtimeDriftAdoptDesc', { pid })}
        confirmLabel={t('serverConsole.runtimeDriftAdopt')}
        scope="group"
        pending={adopting}
        onConfirm={() => {
          setConfirmOpen(false)
          void onAdopt()
        }}
        onCancel={() => setConfirmOpen(false)}
      />
    </>
  )
}

/**
 * 醒目告警横幅（实例详情页用）：说明漂移含义 + 展示 PID/命令行 + 提供「接管」入口。
 */
export function RuntimeDriftBanner({
  instanceName,
  pid,
  cmdline,
  canOperate,
  adopting = false,
  onAdopt,
}: RuntimeDriftInfoView & {
  instanceName: string
  canOperate: boolean
  adopting?: boolean
  onAdopt: () => Promise<boolean>
}) {
  const { t } = useTranslation()

  return (
    <div
      role="alert"
      data-testid="runtime-drift-banner"
      className="flex items-start gap-2 rounded-md border border-status-warning/40 bg-status-warning/10 px-3 py-2 text-xs text-status-warning"
    >
      <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
      <div className="min-w-0 flex-1">
        <p className="font-semibold">{t('serverConsole.runtimeDriftTitle')}</p>
        <p className="mt-0.5 break-words">{t('serverConsole.runtimeDriftDesc', { pid })}</p>
        {/* 命令行摘要：常是判断「这是不是我们那台服」的关键证据，可换行完整展示。 */}
        {cmdline && (
          <p className="mt-0.5 break-all font-mono text-[11px] opacity-90">
            {t('serverConsole.runtimeDriftCmdline')}
            {': '}
            {cmdline}
          </p>
        )}
      </div>
      <RuntimeDriftAdoptButton
        instanceName={instanceName}
        pid={pid}
        canOperate={canOperate}
        adopting={adopting}
        onAdopt={onAdopt}
        className="shrink-0"
      />
    </div>
  )
}
