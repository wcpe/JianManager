import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, Download, Loader2, Square, XCircle } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { ObjectPageHeader, type ObjectTone } from '@jianmanager/ui/components/shell'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import type { BotLoadRunV2, BotLoadVerdict } from '@/lib/bot-load-types'
import { isLiveRunState, isTerminalRunState } from '@/lib/bot-load-types'
import { DisclaimerBanner } from '@/components/views/bot-load/session/DisclaimerBanner'

/**
 * 压测会话对象头（FR-…）：运行名 + 实时状态指标条 + 停止/取消/报告下载。
 *
 * 展示层：导航、停止/取消/下载均由应用侧容器注入（分别对应 router 导航与三个 mutation），
 * 成功/失败提示也由容器负责；本组件只做确认弹窗、时长计算与渲染。
 */
export function SessionHeaderView({
  run,
  streamStatus,
  reportReady,
  onNavigate,
  onStop,
  onCancel,
  onDownload,
  stopPending,
  cancelPending,
  downloadPending,
}: {
  run: BotLoadRunV2
  streamStatus: string
  reportReady: boolean
  /** 面包屑导航（应用侧注入 router 的 navigate）。 */
  onNavigate: (to: string) => void
  /** 停止运行（应用侧注入 mutation 触发与提示）。 */
  onStop: () => void
  /** 取消运行（应用侧注入 mutation 触发与提示）。 */
  onCancel: () => void
  /** 下载报告（应用侧注入 mutation 触发与提示）。 */
  onDownload: (format: 'json' | 'csv') => void
  stopPending: boolean
  cancelPending: boolean
  downloadPending: boolean
}) {
  const { t } = useTranslation()
  const [confirm, setConfirm] = useState<'stop' | 'cancel' | null>(null)
  const duration = useRunDuration(run)

  const live = isLiveRunState(run.runState)
  const terminal = isTerminalRunState(run.runState)
  const stopping = run.runState === 'stopping' || run.runState === 'cancelling'
  const canReport = terminal && (reportReady || terminal)

  const onConfirm = () => {
    if (confirm === 'stop') {
      onStop()
    } else if (confirm === 'cancel') {
      onCancel()
    }
    setConfirm(null)
  }

  return (
    <>
      {/* 全量对齐：手写 <header> 改用布局层对象头（详情页的第一个子元素）。
          映射：返回 Bots + 实时流状态 → breadcrumbs；运行名 → title；五个实时状态
          → metrics（原为 5 个自绘 chip，均带 label+value，与指标条同构）；停止/取消/
          报告下载 → actions。
          data-testid 是 e2e 契约，live（aria-live="polite"）是 SSE 推送驱动的实时
          播报语义，两者都原样保留。 */}
      <ObjectPageHeader
        data-testid="session-header"
        live
        onNavigate={onNavigate}
        breadcrumbs={[
          {
            label: (
              <span className="inline-flex items-center gap-1">
                <ArrowLeft className="size-3.5" aria-hidden />
                {t('botLoad.backToBots')}
              </span>
            ),
            to: '/bots',
          },
          {
            label: `${t('botLoad.stream')}: ${t(`botLoad.streamStatus.${streamStatus}`, streamStatus)}`,
          },
        ]}
        title={run.name}
        metrics={[
          // 五个实时值全部落在指标条：它们都由 SSE 推送驱动，语义上是「一眼扫过的上下文」，
          // 而不是可点击的概览卡片。判定保留三态语义色（通过/失败/中止），其余走中性前景色，
          // 与原 StatusChip 的无色样式一致。
          { label: t('botLoad.runState'), value: t(`botLoad.runState_${run.runState}`, run.runState) },
          {
            label: t('botLoad.verdict'),
            value: t(`botLoad.verdict_${run.verdict}`, run.verdict),
            tone: VERDICT_TONE[run.verdict] ?? 'default',
          },
          { label: t('botLoad.stage'), value: String(run.currentStage) },
          { label: t('botLoad.duration'), value: duration },
          { label: t('botLoad.targetInstance'), value: run.instanceName ?? String(run.instanceId) },
        ]}
        actions={
          <>
            {live && (
              <>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={stopping || stopPending}
                  onClick={() => setConfirm('stop')}
                >
                  {stopPending ? (
                    <Loader2 className="size-3.5 animate-spin" />
                  ) : (
                    <Square className="size-3.5" />
                  )}
                  {t('botLoad.stop')}
                </Button>
                <Button
                  size="sm"
                  variant="destructive"
                  disabled={cancelPending}
                  onClick={() => setConfirm('cancel')}
                >
                  {cancelPending ? (
                    <Loader2 className="size-3.5 animate-spin" />
                  ) : (
                    <XCircle className="size-3.5" />
                  )}
                  {t('botLoad.cancel')}
                </Button>
              </>
            )}
            <Button
              size="sm"
              variant="outline"
              disabled={!canReport || downloadPending}
              title={!canReport ? t('botLoad.reportNotReady') : undefined}
              onClick={() => onDownload('json')}
            >
              <Download className="size-3.5" />
              JSON
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={!canReport || downloadPending}
              title={!canReport ? t('botLoad.reportNotReady') : undefined}
              onClick={() => onDownload('csv')}
            >
              <Download className="size-3.5" />
              CSV
            </Button>
          </>
        }
      />

      <DisclaimerBanner />

      <Dialog open={confirm != null} onOpenChange={(o) => !o && setConfirm(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {confirm === 'cancel' ? t('botLoad.cancelConfirmTitle') : t('botLoad.stopConfirmTitle')}
            </DialogTitle>
            <DialogDescription>
              {confirm === 'cancel' ? t('botLoad.cancelConfirmDesc') : t('botLoad.stopConfirmDesc')}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirm(null)}>
              {t('common.cancel')}
            </Button>
            <Button
              variant={confirm === 'cancel' ? 'destructive' : 'default'}
              onClick={onConfirm}
            >
              {t('common.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

/**
 * 判定 → 对象头语义色。三态色值沿用原 VerdictChip 的口径
 * （通过=成功、失败=危险、中止=警告），只是从「自绘 chip」改为对象头指标条的 tone——
 * 色表本身由 StatusBadge 统一提供，不再在页面里各写一份。
 */
const VERDICT_TONE: Partial<Record<BotLoadVerdict, ObjectTone>> = {
  passed: 'success',
  failed: 'danger',
  aborted: 'warning',
}

function useRunDuration(run: BotLoadRunV2): string {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (isTerminalRunState(run.runState) || !run.startedAt) return
    const t = window.setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [run.runState, run.startedAt])

  const start = run.startedAt ? Date.parse(run.startedAt) : NaN
  if (!Number.isFinite(start)) return '—'
  const end = isTerminalRunState(run.runState)
    ? Date.parse(run.endedAt ?? run.stoppedAt ?? '') || now
    : now
  const sec = Math.max(0, Math.floor((end - start) / 1000))
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  if (h > 0) return `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
  return `${m}:${String(s).padStart(2, '0')}`
}
