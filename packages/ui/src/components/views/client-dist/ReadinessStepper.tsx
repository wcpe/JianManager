import { useTranslation } from 'react-i18next'
import { ArrowRight, Check } from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import {
  readinessCompletedCount,
  type ReadinessStep,
  type ReadinessStepId,
} from '../../../lib/client-readiness'

/** 渠道工作台页签标识（应用侧 ChannelWorkbench 的 tab 值）。 */
export type WorkbenchTabId = 'keys' | 'versions' | 'core' | 'stats' | 'guide'
/** 步骤器文案配置（标题 + 引导说明 + CTA 跳转 tab）。 */
export const STEP_META: Record<
  ReadinessStepId,
  { titleKey: string; titleFallback: string; hintKey: string; hintFallback: string; ctaKey: string; ctaFallback: string; goto: WorkbenchTabId }
> = {
  channel: {
    titleKey: 'clientChannels.stepChannel',
    titleFallback: '创建频道',
    hintKey: 'clientChannels.stepChannelHint',
    hintFallback: '频道已创建。',
    ctaKey: '',
    ctaFallback: '',
    goto: 'keys',
  },
  keys: {
    titleKey: 'clientChannels.stepKeys',
    titleFallback: '拉取密钥',
    hintKey: 'clientChannels.stepKeysHint',
    hintFallback: '创建一个拉取密钥，供玩家侧更新器鉴权拉取。',
    ctaKey: 'clientChannels.createKey',
    ctaFallback: '创建密钥',
    goto: 'keys',
  },
  version: {
    titleKey: 'clientChannels.stepVersion',
    titleFallback: '发布版本',
    hintKey: 'clientChannels.stepVersionHint',
    hintFallback: '上传客户端文件并发布第一个版本，玩家即可拉取 latest。',
    ctaKey: 'clientVersions.publish',
    ctaFallback: '发布新版本',
    goto: 'versions',
  },
  integrate: {
    titleKey: 'clientChannels.stepIntegrate',
    titleFallback: '接入启动器',
    hintKey: 'clientChannels.stepIntegrateHint',
    hintFallback: '照接入指引把更新器接入整合包并下发玩家。',
    ctaKey: 'clientChannels.viewGuide',
    ctaFallback: '查看接入指引',
    goto: 'guide',
  },
}

/** 顶部常驻就绪度步骤器（纯展示，状态由 detail 推导）。 */
export function ReadinessStepper({ steps, onCta }: { steps: ReadinessStep[]; onCta: (stepId: ReadinessStepId) => void }) {
  const { t } = useTranslation()
  const current = steps.find((s) => s.current)
  const completed = readinessCompletedCount(steps)
  const allReady = completed === steps.length

  return (
    <div className="rounded-xl border bg-card/40 p-4 space-y-3">
      <div className="flex items-center justify-between gap-2 flex-wrap">
        <h2 className="text-sm font-semibold">{t('clientChannels.readinessTitle', '接入就绪度')}</h2>
        <span className="text-xs text-muted-foreground">
          {allReady
            ? t('clientChannels.readyAll', '全部就绪，玩家可正常更新')
            : t('clientChannels.readinessShort', '就绪度 {{c}}/{{n}}', { c: completed, n: steps.length })}
        </span>
      </div>

      <ol className="flex items-stretch gap-2 flex-wrap">
        {steps.map((s, i) => {
          const meta = STEP_META[s.id]
          return (
            <li key={s.id} className="flex items-center gap-2">
              <div
                className={cn(
                  'flex items-center gap-2 rounded-lg border px-3 py-2 text-sm',
                  s.current && 'border-primary/50 bg-primary/5',
                  s.done && !s.current && 'border-emerald-500/30 bg-emerald-500/5',
                )}
              >
                <span
                  className={cn(
                    'grid size-6 shrink-0 place-items-center rounded-full text-xs font-medium',
                    s.done
                      ? 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-500'
                      : s.current
                        ? 'bg-primary text-primary-foreground'
                        : 'bg-muted text-muted-foreground',
                  )}
                >
                  {s.done ? <Check className="size-3.5" /> : i + 1}
                </span>
                <span className={cn('whitespace-nowrap', s.done && !s.current && 'text-muted-foreground')}>
                  {t(meta.titleKey, meta.titleFallback)}
                </span>
              </div>
              {i < steps.length - 1 && <ArrowRight className="size-3.5 shrink-0 text-muted-foreground/50" />}
            </li>
          )
        })}
      </ol>

      {current && (
        <div className="flex items-center justify-between gap-3 flex-wrap rounded-lg bg-muted/40 px-3 py-2">
          <p className="text-xs text-muted-foreground">{t(STEP_META[current.id].hintKey, STEP_META[current.id].hintFallback)}</p>
          {STEP_META[current.id].ctaKey && (
            <Button size="sm" variant="outline" className="shrink-0" onClick={() => onCta(current.id)}>
              {t(STEP_META[current.id].ctaKey, STEP_META[current.id].ctaFallback)}
              <ArrowRight className="size-3.5" />
            </Button>
          )}
        </div>
      )}
    </div>
  )
}
