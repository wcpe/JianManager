import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useParams } from 'react-router'
import { Ban, PauseCircle, PlayCircle, RotateCcw, Send, ShieldCheck, Workflow } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { Panel } from '@jianmanager/ui/components/panel'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { PaginationBar } from '@jianmanager/ui/components/pagination'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { NativeSelect } from '@jianmanager/ui/components/native-select'
import { Textarea } from '@jianmanager/ui/components/textarea'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import {
  DELIVERY_FLOW,
  DELIVERY_STATUS_KEYS,
  DELIVERY_TARGET_STATUS_KEYS,
  deferredNote,
  deliveryActionsOf,
  deliveryStatusLevel,
  deliveryTargetStatusLevel,
  flowIndexOf,
  isActivationConfirmed,
  isCircuitBreakPaused,
  useApproveConfigDelivery,
  useCancelConfigDelivery,
  useConfigDelivery,
  useConfigDeliveryImpact,
  useConfigDeliveryObserve,
  useConfigDeliveryTargets,
  usePauseConfigDelivery,
  useResumeConfigDelivery,
  useRollbackConfigDelivery,
  useStartConfigDelivery,
  useSubmitConfigDelivery,
  type ConfigDeliveryOrder,
  type ConfigScopeLevel,
  type DeliveryResumeMode,
  type DeliveryStatus,
  type DeliveryTargetStatus,
  type DeliveryTargetView,
} from '@/api/configDeliveries'
import type { StatusLevel } from '@jianmanager/ui'

/** 
 * 配置交付单详情页（灰度状态机生效编排）。
 *
 * 【三处绝对不能含糊的语义】
 *
 * 1. **`deferred` ≠ `failed`**。离线目标标记 `deferred` 且**不计入失败率**（分母排除它们），
 *    上线后自动补推。界面上两者必须分开显示并各自计数——合并会让运维把「机器不在」
 *    误判成「配置坏了」，进而去回滚一份本来正确的配置。
 *
 * 2. **熔断后是 `paused` 而不是 `failed`**。`pauseKind=circuit_break` 表示系统替人踩了刹车；
 *    这与「人工暂停等人点继续」是两种完全不同的处境，必须显式提示并给出熔断现场
 *    （哪条阈值、阈值多少、实测多少）。
 *
 * 3. **`BEST_EFFORT` 不能显示成「已生效」**。插件（如 Bukkit 的 `reloadConfig()`）是 void 返回，
 *    探针无从得知是否真的生效。此时目标虽然记 `activated`，但 `activateConfirmed=false` +
 *    `bestEffort=true`，文案只能是「已请求重载，插件未提供确认」。
 *
 * 状态机可视化：主流程 draft → pending_approval → approved → rolling → observing → completed
 * 走流程条；侧路状态（paused / failed / rolling_back / rolled_back / cancelled）单独高亮，
 * 因为把它们塞进主流程条只会让人误以为「还能沿流程往下走」。
 */

/** 单个目标的展示文案：把后端给的诚实回执翻成界面语言，绝不夸大。 */
interface TargetVerdict {
  /** 主文案（人类可读）。 */
  text: string
  /** 徽章等级。 */
  level: StatusLevel
  /** 
   * 是否需要额外一行解释。BEST_EFFORT / 落盘成功但生效失败 / 熔断跳过 都必须解释，
   * 否则用户只能看到「已生效」或「失败」而不知所以。
   */
  note?: string
}

/** 
 * 由目标行推导界面文案。
 *
 * 优先级刻意如此：先看 `bestEffort`（它会让 `activated` 变得不可信），再看
 * `diskWritten && failed`（落盘成功但生效失败——磁盘是新配置、内存是旧配置，重启就会变），
 * 最后才按状态枚举给通用文案。
 */
function targetVerdictOf(target: DeliveryTargetView, t: (key: string) => string): TargetVerdict {
  // best_effort：状态是 activated，但没有任何「已生效」的证据。
  if (target.status === 'activated' && target.bestEffort) {
    return {
      text: t('configDeliveries.target.bestEffort'),
      level: 'warning',
      note:
        target.activationMessage ||
        t('configDeliveries.target.bestEffortNote'),
    }
  }
  if (target.status === 'activated' && isActivationConfirmed(target)) {
    return {
      text: t('configDeliveries.target.activated'),
      level: 'success',
      note: target.activationMessage || undefined,
    }
  }
  if (target.status === 'activated') {
    // activated 但未确认且非 best-effort（例如 push_only）：仍然不能说「已生效」。
    return {
      text: t('configDeliveries.target.activatedUnconfirmed'),
      level: 'warning',
      note: target.activationMessage || undefined,
    }
  }
  if (target.status === 'failed') {
    // 落盘成功但生效失败：磁盘上是新配置、内存里是旧配置，重启就会变——必须点明。
    const note = target.diskWritten
      ? t('configDeliveries.target.diskOkActivateFailed')
      : t('configDeliveries.target.diskNotWritten')
    return {
      text: t('configDeliveries.target.failed'),
      level: 'danger',
      note: target.reason || note,
    }
  }
  return {
    text: t(`configDeliveries.target.${DELIVERY_TARGET_STATUS_KEYS[target.status]}`),
    level: deliveryTargetStatusLevel(target.status),
    note: target.reason || undefined,
  }
}

/** 层作用域的展示文案。 */
function scopeLabel(level: ConfigScopeLevel, scopeId: number, t: (key: string) => string): string {
  return level === 'group'
    ? t('configDeliveries.scopeGroup').replace('{{id}}', String(scopeId))
    : t('configDeliveries.scopeInstance').replace('{{id}}', String(scopeId))
}

/** 时间格式化（空值显示占位）。 */
function formatTime(value: string | null | undefined): string {
  if (!value) return '—'
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString()
}

/** 主流程条：当前状态高亮，侧路状态在条外单独说明。 */
function StatusFlow({ status }: { status: DeliveryStatus }) {
  const { t } = useTranslation()
  const index = flowIndexOf(status)
  const onSidePath = index < 0
  return (
    <div className="space-y-2">
      <ol className="flex flex-wrap items-center gap-1" data-testid="delivery-flow">
        {DELIVERY_FLOW.map((step, at) => {
          const active = at === index
          const passed = index >= 0 && at < index
          return (
            <li key={step} className="flex items-center gap-1">
              <span
                data-testid={`delivery-flow-step-${step}`}
                data-active={active ? 'true' : 'false'}
                className={
                  active
                    ? 'rounded bg-primary px-2 py-1 text-xs font-semibold text-primary-foreground'
                    : passed
                      ? 'rounded bg-muted px-2 py-1 text-xs text-foreground'
                      : 'rounded px-2 py-1 text-xs text-muted-foreground'
                }
              >
                {t(`configDeliveries.status.${DELIVERY_STATUS_KEYS[step]}`)}
              </span>
              {at < DELIVERY_FLOW.length - 1 ? <span className="text-xs text-muted-foreground">→</span> : null}
            </li>
          )
        })}
      </ol>
      {onSidePath ? (
        // 侧路状态不进流程条：塞进去会让人以为「还能沿流程继续」，实际要等回滚结束或人工决策。
        <p className="text-xs text-muted-foreground" data-testid="delivery-flow-sidepath">
          {t('configDeliveries.sidePathHint')}
        </p>
      ) : null}
    </div>
  )
}

/** 熔断提示条：说清「谁踩的刹车、踩在哪条阈值上」。 */
function CircuitBreakNotice({ order }: { order: ConfigDeliveryOrder }) {
  const { t } = useTranslation()
  if (!isCircuitBreakPaused(order)) return null
  return (
    <div
      className="space-y-1 rounded border border-[color:var(--danger-line)] bg-[color:var(--danger-weak)] px-3 py-2 text-xs text-[color:var(--danger-ink)]"
      data-testid="delivery-circuit-break"
    >
      <p className="font-semibold">{t('configDeliveries.circuitBreakTitle')}</p>
      <p>
        {order.breakReason
          ? order.breakReason
          : t('configDeliveries.circuitBreakFallback')}
      </p>
      {/* 阈值与实测值分开列：只写「触发了熔断」对排障毫无帮助。 */}
      {order.breakThreshold || order.breakObserved ? (
        <p className="tabular-nums">
          {t('configDeliveries.circuitBreakNumbers', {
            threshold: order.breakThreshold ?? 0,
            observed: order.breakObserved ?? 0,
          })}
        </p>
      ) : null}
      {order.failedBatchNo ? (
        <p>{t('configDeliveries.circuitBreakBatch', { batch: order.failedBatchNo })}</p>
      ) : null}
    </div>
  )
}

/** 
 * 回滚对话框。
 *
 * 【整批 vs 子集是两个不同的操作，不是同一个操作的两个参数】
 * - 整批：逐台还原快照 **+ 配置层版本回退** + 重新生效；
 * - 子集：只还原选中的几台，**层版本不回退**（§10.2）。
 *
 * 因此对话框先让用户选范围，再按范围给出不同的后果说明——把两者塞进一个「确认回滚」
 * 按钮里，用户根本不知道自己签下的是什么。
 */
function RollbackDialog({
  open,
  onOpenChange,
  targets,
  pending,
  onConfirm,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  targets: DeliveryTargetView[]
  pending: boolean
  onConfirm: (payload: { instanceIds?: number[]; reason?: string }) => void
}) {
  const { t } = useTranslation()
  const [scope, setScope] = useState<'full' | 'subset'>('full')
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [reason, setReason] = useState('')

  /** 可被选中的目标：只列出真正下发过的（pending / skipped 没有可还原的内容）。 */
  const selectable = useMemo(
    () => targets.filter((tg) => tg.status !== 'pending' && tg.status !== 'skipped'),
    [targets],
  )

  const submit = () => {
    onConfirm({
      // 整批必须显式不传 instanceIds：后端以「非空」判定子集，传 undefined 才是整批。
      instanceIds: scope === 'subset' ? [...selected] : undefined,
      reason: reason.trim() || undefined,
    })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={scrollableDialogContentClass}>
        <DialogHeader>
          <DialogTitle>{t('configDeliveries.rollbackTitle')}</DialogTitle>
          <DialogDescription>{t('configDeliveries.rollbackDesc')}</DialogDescription>
        </DialogHeader>
        <ScrollableDialogBody className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <Button
              size="sm"
              variant={scope === 'full' ? 'default' : 'outline'}
              data-testid="rollback-scope-full"
              onClick={() => setScope('full')}
            >
              {t('configDeliveries.rollbackFull')}
            </Button>
            <Button
              size="sm"
              variant={scope === 'subset' ? 'default' : 'outline'}
              data-testid="rollback-scope-subset"
              onClick={() => setScope('subset')}
            >
              {t('configDeliveries.rollbackSubset')}
            </Button>
          </div>

          {/* 后果说明随范围切换：这是用户按下确认前唯一能读到「我会失去什么」的地方。 */}
          <p className="text-xs text-muted-foreground" data-testid="rollback-scope-hint">
            {scope === 'full' ? t('configDeliveries.rollbackFullHint') : t('configDeliveries.rollbackSubsetHint')}
          </p>

          {scope === 'subset' ? (
            selectable.length === 0 ? (
              <p className="text-xs text-muted-foreground">{t('configDeliveries.rollbackNoTargets')}</p>
            ) : (
              <div className="max-h-64 space-y-1 overflow-y-auto rounded border border-border p-2">
                {selectable.map((tg) => (
                  <label key={tg.instanceId} className="flex items-center gap-2 text-sm">
                    <Checkbox
                      aria-label={tg.instanceName}
                      checked={selected.has(tg.instanceId)}
                      onCheckedChange={(v) => {
                        setSelected((cur) => {
                          const next = new Set(cur)
                          if (v === true) next.add(tg.instanceId)
                          else next.delete(tg.instanceId)
                          return next
                        })
                      }}
                    />
                    <span>{tg.instanceName || `#${tg.instanceId}`}</span>
                    <span className="text-xs text-muted-foreground">
                      {t(`configDeliveries.target.${DELIVERY_TARGET_STATUS_KEYS[tg.status]}`)}
                    </span>
                  </label>
                ))}
              </div>
            )
          ) : null}

          <div className="space-y-1">
            <label className="text-xs text-muted-foreground" htmlFor="rollback-reason">
              {t('configDeliveries.rollbackReason')}
            </label>
            <Textarea
              id="rollback-reason"
              className="min-h-16"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
            />
          </div>
        </ScrollableDialogBody>
        <DialogFooter>
          <Button variant="outline" size="sm" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button
            size="sm"
            variant="destructive"
            data-testid="rollback-confirm"
            // 子集回滚必须至少选一台：后端收到空数组会当成整批，语义静默反转。
            disabled={pending || (scope === 'subset' && selected.size === 0)}
            onClick={submit}
          >
            {pending ? t('common.saving') : t('configDeliveries.rollbackConfirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** 目标列表的分页与筛选（状态筛选只给出实际出现过的取值）。 */
const TARGET_FILTERS: (DeliveryTargetStatus | '')[] = [
  '',
  'pending',
  'pushing',
  'pushed',
  'activating',
  'activated',
  'failed',
  'deferred',
  'skipped',
]

export default function ConfigDeliveryDetailPage() {
  const { t } = useTranslation()
  const params = useParams()
  const id = params.id ? Number(params.id) : null

  const orderQ = useConfigDelivery(id)
  const order = orderQ.data
  const [targetStatus, setTargetStatus] = useState<DeliveryTargetStatus | ''>('')
  const [page, setPage] = useState(1)
  const targetsQ = useConfigDeliveryTargets(id, { status: targetStatus || undefined, page, pageSize: 50 })
  const impactQ = useConfigDeliveryImpact(id, !!order)
  const observeQ = useConfigDeliveryObserve(id, !!order)

  const submit = useSubmitConfigDelivery()
  const approve = useApproveConfigDelivery()
  const start = useStartConfigDelivery()
  const pause = usePauseConfigDelivery()
  const cancel = useCancelConfigDelivery()
  const rollback = useRollbackConfigDelivery()
  const resume = useResumeConfigDelivery()

  const [rollbackOpen, setRollbackOpen] = useState(false)
  /** 继续模式：默认重试失败目标（「继续」在运维语境里就是「再试一次」）。 */
  const [resumeMode, setResumeMode] = useState<DeliveryResumeMode>('retry_failed')

  const targets = targetsQ.data?.targets ?? []
  const targetTotal = targetsQ.data?.total ?? 0
  const targetTotalPages = Math.max(1, Math.ceil(targetTotal / (targetsQ.data?.pageSize ?? 50)))

  if (!id) {
    return (
      <PageShell>
        <p className="text-sm text-destructive">{t('configDeliveries.invalidId')}</p>
      </PageShell>
    )
  }

  if (orderQ.isError) {
    return (
      <PageShell>
        <PageHeader title={t('configDeliveries.detailTitle')} />
        <p className="text-sm text-destructive" data-testid="delivery-detail-error">
          {t('configDeliveries.loadFailed')}
        </p>
      </PageShell>
    )
  }

  if (!order) {
    return (
      <PageShell>
        <PageHeader title={t('configDeliveries.detailTitle')} />
        <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
      </PageShell>
    )
  }

  const actions = deliveryActionsOf(order.status)
  const observe = observeQ.data
  const impact = impactQ.data

  return (
    <PageShell>
      <PageHeader
        title={t('configDeliveries.detailTitleOf', { id: order.id })}
        description={order.filePath}
        actions={
          <Link to="/config-deliveries" className="text-sm text-muted-foreground hover:underline">
            {t('common.back')}
          </Link>
        }
      />

      {/* ── 状态与操作 ─────────────────────────────────────────────────── */}
      <Panel title={t('configDeliveries.sectionStatus')} icon={<Workflow className="size-4" />}>
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <StatusBadge
              level={deliveryStatusLevel(order.status)}
              label={t(`configDeliveries.status.${DELIVERY_STATUS_KEYS[order.status]}`)}
              pulse={order.status === 'rolling' || order.status === 'rolling_back'}
            />
            {isCircuitBreakPaused(order) ? (
              <StatusBadge level="danger" label={t('configDeliveries.circuitBreakBadge')} dot={false} />
            ) : null}
            {order.status === 'paused' && !isCircuitBreakPaused(order) ? (
              <StatusBadge level="warning" label={t('configDeliveries.pausedManualBadge')} dot={false} />
            ) : null}
            <span className="text-xs text-muted-foreground">
              {t('configDeliveries.batchProgress', {
                current: order.currentBatch,
                total: order.totalBatches,
              })}
            </span>
          </div>

          <StatusFlow status={order.status} />
          <CircuitBreakNotice order={order} />

          {/* 按钮按状态启用/禁用；判据与后端状态机表一致（见 deliveryActionsOf）。 */}
          <div className="flex flex-wrap items-center gap-2" data-testid="delivery-actions">
            <Button
              size="sm"
              variant="outline"
              data-testid="action-submit"
              disabled={!actions.submit || submit.isPending}
              onClick={() => submit.mutate(order.id)}
            >
              <Send className="size-3.5" />
              {t('configDeliveries.actionSubmit')}
            </Button>
            <Button
              size="sm"
              variant="outline"
              data-testid="action-approve"
              disabled={!actions.approve || approve.isPending}
              onClick={() => approve.mutate(order.id)}
            >
              <ShieldCheck className="size-3.5" />
              {t('configDeliveries.actionApprove')}
            </Button>
            <Button
              size="sm"
              data-testid="action-start"
              disabled={!actions.start || start.isPending}
              onClick={() => start.mutate(order.id)}
            >
              <PlayCircle className="size-3.5" />
              {t('configDeliveries.actionStart')}
            </Button>
            <Button
              size="sm"
              variant="outline"
              data-testid="action-pause"
              disabled={!actions.pause || pause.isPending}
              onClick={() => pause.mutate({ id: order.id, reason: t('configDeliveries.pauseByOperator') })}
            >
              <PauseCircle className="size-3.5" />
              {t('configDeliveries.actionPause')}
            </Button>
            <div className="flex items-center gap-1">
              <NativeSelect
                aria-label={t('configDeliveries.resumeMode')}
                data-testid="resume-mode"
                value={resumeMode}
                onChange={(e) => setResumeMode(e.target.value as DeliveryResumeMode)}
              >
                <option value="retry_failed">{t('configDeliveries.resumeRetryFailed')}</option>
                <option value="skip_failed">{t('configDeliveries.resumeSkipFailed')}</option>
              </NativeSelect>
              <Button
                size="sm"
                variant="outline"
                data-testid="action-resume"
                disabled={!actions.resume || resume.isPending}
                onClick={() => resume.mutate({ id: order.id, mode: resumeMode })}
              >
                {t('configDeliveries.actionResume')}
              </Button>
            </div>
            <Button
              size="sm"
              variant="outline"
              data-testid="action-cancel"
              disabled={!actions.cancel || cancel.isPending}
              onClick={() => cancel.mutate({ id: order.id, reason: t('configDeliveries.cancelByOperator') })}
            >
              <Ban className="size-3.5" />
              {t('configDeliveries.actionCancel')}
            </Button>
            <Button
              size="sm"
              variant="destructive"
              data-testid="action-rollback"
              disabled={!actions.rollback}
              onClick={() => setRollbackOpen(true)}
            >
              <RotateCcw className="size-3.5" />
              {t('configDeliveries.actionRollback')}
            </Button>
          </div>
        </div>
      </Panel>

      {/* ── 基本信息 ─────────────────────────────────────────────────── */}
      <Panel title={t('configDeliveries.sectionBasic')}>
        <dl className="grid grid-cols-1 gap-x-6 gap-y-2 text-sm sm:grid-cols-2 lg:grid-cols-3">
          <div>
            <dt className="text-xs text-muted-foreground">{t('configDeliveries.fieldFilePath')}</dt>
            <dd className="font-mono text-xs">{order.filePath}</dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">{t('configDeliveries.fieldLayer')}</dt>
            <dd>{scopeLabel(order.targetLayerLevel, order.targetLayerScopeId, t)}</dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">{t('configDeliveries.fieldActivation')}</dt>
            <dd>{t(`configDeliveries.activation.${order.activationMethod}`)}</dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">{t('configDeliveries.fieldBatchMode')}</dt>
            <dd>
              {t(`configDeliveries.batchMode.${order.batchMode}`)} ·{' '}
              {t('configDeliveries.fieldBatches', { total: order.totalBatches })}
            </dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">{t('configDeliveries.fieldObserveWindow')}</dt>
            <dd className="tabular-nums">{order.observeWindowSec}s</dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">{t('configDeliveries.fieldThresholds')}</dt>
            <dd className="tabular-nums">
              {order.failureRateThreshold}% / {order.unhealthyRateThreshold}%
            </dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">{t('configDeliveries.fieldAutoRetry')}</dt>
            <dd>
              {order.autoRetryDeferred
                ? t('configDeliveries.autoRetryOn', { limit: order.deferredRetryLimit })
                : t('configDeliveries.autoRetryOff')}
            </dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">{t('configDeliveries.colCreatedAt')}</dt>
            <dd className="text-xs">{formatTime(order.createdAt)}</dd>
          </div>
          <div>
            <dt className="text-xs text-muted-foreground">{t('configDeliveries.fieldFinishedAt')}</dt>
            <dd className="text-xs">{formatTime(order.finishedAt)}</dd>
          </div>
        </dl>
      </Panel>

      {/* ── 观察窗 ───────────────────────────────────────────────────── */}
      <Panel title={t('configDeliveries.sectionObserve')}>
        {observeQ.isError ? (
          <p className="text-sm text-destructive">{t('configDeliveries.observeFailed')}</p>
        ) : !observe ? (
          <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
        ) : (
          <div className="space-y-3" data-testid="delivery-observe">
            <div className="flex flex-wrap gap-6 text-sm">
              <span>
                {t('configDeliveries.observePlanned')}: <strong className="tabular-nums">{observe.measured.planned}</strong>
              </span>
              {/* failed 与 deferred 分两处：deferred 不进失败率分母，合并会误判。 */}
              <span data-testid="observe-failed">
                {t('configDeliveries.colFailed')}:{' '}
                <strong className="tabular-nums">{observe.measured.failed}</strong>
              </span>
              <span data-testid="observe-deferred">
                {t('configDeliveries.colDeferred')}:{' '}
                <strong className="tabular-nums">{observe.measured.deferred}</strong>
              </span>
              <span data-testid="observe-failure-rate">
                {t('configDeliveries.observeFailureRate')}:{' '}
                <strong className="tabular-nums">{observe.measured.failureRate}%</strong>
              </span>
              <span>
                {t('configDeliveries.observeUnhealthyRate')}:{' '}
                <strong className="tabular-nums">{observe.measured.unhealthyRate}%</strong>
              </span>
            </div>

            {/* 口径说明：失败率分母排除 deferred，不说清的话 0% 会让人以为「没有异常」。 */}
            <p className="text-xs text-muted-foreground" data-testid="observe-deferred-note">
              {deferredNote(observe.measured) || t('configDeliveries.observeFailureRateFormula')}
            </p>

            {observe.batches.length > 0 ? (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t('configDeliveries.colBatch')}</TableHead>
                    <TableHead>{t('common.status')}</TableHead>
                    <TableHead>{t('configDeliveries.colPlanned')}</TableHead>
                    <TableHead>{t('configDeliveries.colActivated')}</TableHead>
                    <TableHead>{t('configDeliveries.colFailed')}</TableHead>
                    <TableHead>{t('configDeliveries.colDeferred')}</TableHead>
                    <TableHead>{t('configDeliveries.colSkipped')}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {observe.batches.map((b) => (
                    <TableRow key={b.batchNo} data-testid="observe-batch-row">
                      <TableCell className="tabular-nums">{b.batchNo}</TableCell>
                      <TableCell className="text-xs">{b.state}</TableCell>
                      <TableCell className="tabular-nums">{b.total}</TableCell>
                      <TableCell className="tabular-nums">{b.activated}</TableCell>
                      <TableCell className="tabular-nums">{b.failed}</TableCell>
                      <TableCell className="tabular-nums">{b.deferred}</TableCell>
                      <TableCell className="tabular-nums">{b.skipped}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            ) : null}
          </div>
        )}
      </Panel>

      {/* ── 影响预览 ─────────────────────────────────────────────────── */}
      <Panel title={t('configDeliveries.sectionImpact')}>
        {impactQ.isError ? (
          <p className="text-sm text-destructive">{t('configDeliveries.impactFailed')}</p>
        ) : !impact ? (
          <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
        ) : (
          <div className="space-y-2" data-testid="delivery-impact">
            <div className="flex flex-wrap gap-6 text-sm">
              <span>
                {t('configDeliveries.colPlanned')}: <strong className="tabular-nums">{impact.planned}</strong>
              </span>
              <span>
                {t('configDeliveries.impactChanged')}: <strong className="tabular-nums">{impact.changed}</strong>
              </span>
              <span>
                {t('configDeliveries.impactUnchanged')}: <strong className="tabular-nums">{impact.unchanged}</strong>
              </span>
              <span>
                {t('configDeliveries.impactUndetermined')}:{' '}
                <strong className="tabular-nums">{impact.undetermined}</strong>
              </span>
              <span>
                {t('configDeliveries.impactOfflineRate')}:{' '}
                <strong className="tabular-nums">{impact.offlineRate}%</strong>
              </span>
            </div>
            {impact.wouldCircuitBreak ? (
              // 开跑前就把「会熔断」说出来：否则用户点了开始，第一批就停下。
              <p className="text-xs text-[color:var(--danger-ink)]" data-testid="impact-would-break">
                {t('configDeliveries.impactWouldBreak')}
              </p>
            ) : null}
            {impact.truncated ? (
              <p className="text-xs text-muted-foreground">{t('configDeliveries.impactTruncated')}</p>
            ) : null}
          </div>
        )}
      </Panel>

      {/* ── 目标列表 ─────────────────────────────────────────────────── */}
      <Panel
        title={t('configDeliveries.sectionTargets')}
        actions={
          <NativeSelect
            aria-label={t('common.status')}
            data-testid="target-status-filter"
            value={targetStatus}
            onChange={(e) => {
              setTargetStatus(e.target.value as DeliveryTargetStatus | '')
              setPage(1)
            }}
          >
            {TARGET_FILTERS.map((s) => (
              <option key={s || 'all'} value={s}>
                {s ? t(`configDeliveries.target.${DELIVERY_TARGET_STATUS_KEYS[s]}`) : t('common.all')}
              </option>
            ))}
          </NativeSelect>
        }
        footer={
          targetTotal > 0 ? (
            <PaginationBar
              page={page}
              totalPages={targetTotalPages}
              total={targetTotal}
              onChange={setPage}
              totalLabel={t('configDeliveries.totalLabel', { total: targetTotal })}
              prevLabel={t('common.prevPage')}
              nextLabel={t('common.nextPage')}
            />
          ) : undefined
        }
      >
        {targetsQ.isError ? (
          <p className="text-sm text-destructive">{t('configDeliveries.targetsFailed')}</p>
        ) : targets.length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="delivery-targets-empty">
            {t('configDeliveries.targetsEmpty')}
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('configDeliveries.colBatch')}</TableHead>
                <TableHead>{t('configDeliveries.colInstance')}</TableHead>
                <TableHead>{t('common.status')}</TableHead>
                <TableHead>{t('configDeliveries.colDisk')}</TableHead>
                <TableHead>{t('configDeliveries.colActivate')}</TableHead>
                <TableHead>{t('configDeliveries.colDetail')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {targets.map((tg) => {
                const verdict = targetVerdictOf(tg, t)
                return (
                  <TableRow key={tg.id} data-testid="delivery-target-row" data-status={tg.status}>
                    <TableCell className="tabular-nums">{tg.batchNo}</TableCell>
                    <TableCell className="text-xs">
                      <Link to={`/instances/${tg.instanceId}`} className="hover:underline">
                        {tg.instanceName || `#${tg.instanceId}`}
                      </Link>
                      {tg.instanceStatus ? (
                        <span className="ml-1 text-muted-foreground">({tg.instanceStatus})</span>
                      ) : null}
                    </TableCell>
                    <TableCell>
                      <StatusBadge level={verdict.level} label={verdict.text} />
                    </TableCell>
                    {/* 落盘与生效分两列：磁盘是新配置、内存是旧配置是最危险的一种状态。 */}
                    <TableCell className="text-xs">
                      {tg.diskWritten ? t('configDeliveries.diskWritten') : t('configDeliveries.diskNotWritten')}
                    </TableCell>
                    <TableCell className="text-xs">
                      {tg.activateConfirmed
                        ? t('configDeliveries.activateConfirmed')
                        : tg.bestEffort
                          ? t('configDeliveries.activateBestEffort')
                          : t('configDeliveries.activateUnconfirmed')}
                    </TableCell>
                    <TableCell className="max-w-md text-xs text-muted-foreground">
                      {verdict.note ? <span data-testid="delivery-target-note">{verdict.note}</span> : '—'}
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        )}
      </Panel>

      <RollbackDialog
        open={rollbackOpen}
        onOpenChange={setRollbackOpen}
        targets={targets}
        pending={rollback.isPending}
        onConfirm={(payload) => {
          rollback.mutate(
            { id: order.id, ...payload },
            { onSuccess: () => setRollbackOpen(false) },
          )
        }}
      />
    </PageShell>
  )
}
