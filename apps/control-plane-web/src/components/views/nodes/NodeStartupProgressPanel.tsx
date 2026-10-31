import { useTranslation } from 'react-i18next'
import { Badge } from '@jianmanager/ui/components/badge'
import { STARTUP_PHASE_LABELS, type NodeStartupProgressInfo } from '@/api/nodes'

/** 
 * 「正在启动的 Worker」提示条（范围②）。
 *
 * ## 为什么它必须独立于节点列表
 *
 * 启动中的 Worker 尚未注册：在 `nodes` 表里没有任何记录（首装时连 UUID 都没有）。
 * 把它塞进节点列表会凭空出现「存在但从未上线」的节点，比看不到更误导。
 * 所以这里是一块独立的提示区，明确表达「这些还**不是**节点，它们正在变成节点」。
 *
 * ## 为什么 stale 用后端算好的值
 *
 * 前端拿 `updatedAt` 自己比较会依赖客户端时钟——而运维机器的时钟偏差是常见现象，
 * 那会让「启动中断」判定随机失真。后端算、前端只渲染。
 */

/** 阶段名取中文对照；未收录的阶段原样显示，避免新阶段上线后界面空白。 */
function phaseLabel(phase: string, t: (k: string) => string): string {
  const mapped = STARTUP_PHASE_LABELS[phase]
  if (mapped) return t(`nodes.startupPhase.${phase}`)
  return phase
}

export interface NodeStartupProgressPanelProps {
  items: NodeStartupProgressInfo[]
  /** 加载中：不渲染任何东西，避免启动瞬间闪一条空提示。 */
  loading?: boolean
}

export function NodeStartupProgressPanel({ items, loading }: NodeStartupProgressPanelProps) {
  const { t } = useTranslation()

  // 空态不占位：绝大多数时间没有正在启动的节点，留一块空白区域只是噪声。
  if (loading || items.length === 0) return null

  return (
    <div
      role="status"
      aria-live="polite"
      className="mb-3 rounded-[var(--radius)] border border-border bg-muted/40 p-3"
    >
      <div className="mb-2 text-sm font-medium">{t('nodes.startupInProgressTitle')}</div>
      <ul className="space-y-2">
        {items.map((it) => {
          const stale = it.stale
          const pct = it.phasesTotal > 0 ? Math.round((it.phasesDone / it.phasesTotal) * 100) : 0
          return (
            <li key={it.key} className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
              <span className="font-medium">{it.nodeName || it.key}</span>
              {stale ? (
                // 中断与「正在启动」必须视觉可分：前者需要人去查进程，后者只需等待。
                <Badge variant="chip-bad">{t('nodes.startupInterrupted')}</Badge>
              ) : (
                <>
                  <Badge variant="chip-idle">
                    {t('nodes.startupPhaseLabel', {
                      phase: phaseLabel(it.phase, t),
                      done: it.phasesDone,
                      total: it.phasesTotal,
                    })}
                  </Badge>
                  <span className="text-muted-foreground">
                    {t('nodes.startupElapsed', { seconds: (it.totalElapsedMs / 1000).toFixed(1) })}
                  </span>
                  <span
                    className="h-1 w-24 overflow-hidden rounded-full bg-border"
                    role="progressbar"
                    aria-valuenow={pct}
                    aria-valuemin={0}
                    aria-valuemax={100}
                    aria-label={t('nodes.startupProgressAria', { name: it.nodeName || it.key })}
                  >
                    <span className="block h-full bg-primary" style={{ width: `${pct}%` }} />
                  </span>
                </>
              )}
              {stale ? (
                <span className="text-muted-foreground">{t('nodes.startupInterruptedHint')}</span>
              ) : null}
            </li>
          )
        })}
      </ul>
    </div>
  )
}
