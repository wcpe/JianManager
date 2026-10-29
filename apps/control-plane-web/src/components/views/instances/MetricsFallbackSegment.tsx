import { useTranslation } from 'react-i18next'

import { Panel } from '@jianmanager/ui/components/panel'
import { Card } from '@jianmanager/ui/components/card'
import { ProcessPanel } from '@/components/views/instances/ProcessPanel'
import type { ProcessMetricsView } from '@/components/views/instances/ProcessPanel'

/** 直探（SLP）摘要数据：无探针时由 Worker 直探 MC 服务器列表协议取得。 */
export interface DirectProbeSummaryData {
  motd?: string
  onlinePlayers?: number
  maxPlayers?: number
  version?: string
  latencyMs?: number
}

/** 
 * 轻量监控视图（§2.1）：`metrics` Tab 在探针不可用时切换到的降级样式。
 *
 * 不再渲染 ServerProbe 全量指标（TPS/MSPT/世界/区块——探针不在时它们是零值，展示即伪数据），
 * 改为两段：
 * - 节点进程指标 {@link ProcessPanel}（CPU/内存/线程/运行时长，与探针无关，始终可得）；
 * - 直探摘要 {@link DirectProbeSummary}（MC SLP；直探编排属447，未落地时显式「不可用」）。
 *
 * 缺测项以「不可用」文案或 ProcessPanel 的破折号兜底，**不展示 `-1`/`--` 占位垃圾值**。
 *
 * 受控视图（ADR-103 a 范式）：不取数——进程指标与直探摘要经 props 注入。
 */
export interface MetricsFallbackSegmentProps {
  /** 进程指标（外壳取数注入）。 */
  metrics?: ProcessMetricsView
  /** 直探摘要；缺省即「直探不可用」，显式呈现而非落回探针的伪值。 */
  summary?: DirectProbeSummaryData | null
}

export default function MetricsFallbackSegment({ metrics, summary }: MetricsFallbackSegmentProps) {
  const { t } = useTranslation()
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 p-4" data-testid="metrics-fallback">
      <Panel title={t('metrics.fallbackTitle')} className="flex-none">
        <p className="text-xs text-muted-foreground">{t('metrics.fallbackHint')}</p>
      </Panel>
      <ProcessPanel metrics={metrics} />
      {/*447 接入点：直探摘要落地后由外壳传入真实 SLP 数据。 */}
      <DirectProbeSummary summary={summary} />
    </div>
  )
}

/** 
 * 直探摘要（§2.1SLP）：MOTD / 在线人数 / 版本 / 延迟。
 *
 * `summary` 缺省即「直探不可用」——显式呈现，绝不落回探针的伪值。
 */
export function DirectProbeSummary({ summary }: { summary?: DirectProbeSummaryData | null }) {
  const { t } = useTranslation()
  return (
    <Panel title={t('metrics.directProbeTitle')} data-testid="direct-probe-summary">
      {summary ? (
        <div className="grid grid-cols-2 gap-2 p-2 text-xs sm:grid-cols-4">
          {summary.motd != null && <DirectProbeStat label={t('metrics.directProbeMotd')} value={summary.motd} />}
          {summary.onlinePlayers != null && (
            <DirectProbeStat
              label={t('metrics.directProbeOnline')}
              value={`${summary.onlinePlayers}${summary.maxPlayers != null ? ` / ${summary.maxPlayers}` : ''}`}
            />
          )}
          {summary.version != null && <DirectProbeStat label={t('metrics.directProbeVersion')} value={summary.version} />}
          {summary.latencyMs != null && <DirectProbeStat label={t('metrics.directProbeLatency')} value={`${summary.latencyMs}ms`} />}
        </div>
      ) : (
        <p className="p-2 text-xs text-muted-foreground">{t('metrics.directProbeUnavailable')}</p>
      )}
    </Panel>
  )
}

function DirectProbeStat({ label, value }: { label: string; value: string }) {
  return (
    /* 直探统计格：Card 只作外壳；格内两段紧排文本靠 mt-1 控距，故 gap-0。 */
    <Card className="gap-0 p-2">
      <p className="text-muted-foreground">{label}</p>
      <p className="mt-1 truncate font-mono text-sm font-semibold">{value}</p>
    </Card>
  )
}
