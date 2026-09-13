import { useTranslation } from 'react-i18next'
import { AlertTriangle, ArrowDownRight, ArrowUpRight, Minus, Users, Download, RefreshCw, HardDrive, Activity } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import type { ClientDistObservabilitySummary, ClientDistObservabilityCompare } from '@/api/clientStats'

/**
 * 分发概览洞察卡（FR-428）：KPI 同环比 + 异常标记 + 口径解释。
 * 「用户」= 机器（machineId 去重，ADR-023 不可信仅近似）；同环比基数来自后端 compare（前一等长窗口）。
 */

function fmtBytes(b: number): string {
  if (!Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1e9) return `${(b / 1024 / 1024 / 1024).toFixed(1)}G`
  if (b >= 1e6) return `${(b / 1024 / 1024).toFixed(0)}M`
  if (b >= 1e3) return `${(b / 1024).toFixed(0)}K`
  return String(b)
}

/** 同环比徽章：率值按百分点差，计数按百分比差；compare 缺失返回 null。 */
function DeltaBadge({ now, before, isRate }: { now: number; before: number | undefined; isRate?: boolean }) {
  const { t } = useTranslation()
  if (before === undefined || !Number.isFinite(before)) return null
  if (isRate) {
    const pp = (now - before) * 100
    if (Math.abs(pp) < 0.05) return <Badge variant="outline" className="gap-0.5 text-muted-foreground"><Minus className="size-3" />±0.0pp</Badge>
    const up = pp > 0
    return (
      <Badge variant="outline" className={up ? 'gap-0.5 text-emerald-600' : 'gap-0.5 text-red-600'}>
        {up ? <ArrowUpRight className="size-3" /> : <ArrowDownRight className="size-3" />}
        {up ? '+' : ''}{pp.toFixed(1)}pp
      </Badge>
    )
  }
  if (before === 0) return now > 0 ? <Badge variant="outline" className="text-emerald-600">{t('clientDistObs.deltaNew', '新增')}</Badge> : null
  const pct = ((now - before) / before) * 100
  if (Math.abs(pct) < 0.5) return <Badge variant="outline" className="gap-0.5 text-muted-foreground"><Minus className="size-3" />0%</Badge>
  const up = pct > 0
  return (
    <Badge variant="outline" className={up ? 'gap-0.5 text-emerald-600' : 'gap-0.5 text-red-600'}>
      {up ? <ArrowUpRight className="size-3" /> : <ArrowDownRight className="size-3" />}
      {up ? '+' : ''}{pct.toFixed(0)}%
    </Badge>
  )
}

interface InsightCardsProps {
  summary: ClientDistObservabilitySummary
  compare?: ClientDistObservabilityCompare
}

export function InsightCards({ summary, compare }: InsightCardsProps) {
  const { t } = useTranslation()

  // 异常标记（FR-428 口径，规则在 insight-cards spec 固化）：
  // 1) 成功率 < 90% → 偏低警示；2) 前窗有更新而本窗为 0 → 无更新活动；3) 本窗更新 ≥ 前窗 3 倍 → 激增。
  const lowSuccess = summary.successRate < 0.9 && summary.updateTotal > 0
  const noUpdates = summary.updateTotal === 0 && (compare?.updateTotal ?? 0) > 0
  const surge = (compare?.updateTotal ?? 0) > 0 && summary.updateTotal >= (compare?.updateTotal ?? 0) * 3

  return (
    <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-5" data-testid="client-dist-insight-cards">
      <StatCard
        label={t('clientDistObs.kpiUpdates', '更新次数')}
        value={summary.updateTotal.toLocaleString()}
        icon={<RefreshCw className="size-3.5" />}
        tone={surge ? 'primary' : 'neutral'}
        sub={
          <span className="flex items-center gap-1.5">
            <DeltaBadge now={summary.updateTotal} before={compare?.updateTotal} />
            {surge && <Badge className="gap-0.5"><Activity className="size-3" />{t('clientDistObs.surge', '激增')}</Badge>}
            {noUpdates && <Badge variant="outline" className="gap-0.5 border-amber-500/50 text-amber-600"><AlertTriangle className="size-3" />{t('clientDistObs.noUpdates', '本时段无更新')}</Badge>}
          </span>
        }
      />
      <StatCard
        label={t('clientDistObs.kpiSuccessRate', '更新成功率')}
        value={`${(summary.successRate * 100).toFixed(1)}%`}
        icon={<Activity className="size-3.5" />}
        tone={lowSuccess ? 'warning' : 'neutral'}
        sub={
          <span className="flex items-center gap-1.5">
            <DeltaBadge now={summary.successRate} before={compare ? summary.successRate - (compare.updateSuccess / Math.max(1, compare.updateSuccess + compare.updateFailStatic + compare.updateRolledBack + compare.updateError)) : undefined} isRate />
            {lowSuccess && <Badge variant="outline" className="gap-0.5 border-amber-500/50 text-amber-600"><AlertTriangle className="size-3" />{t('clientDistObs.lowSuccess', '偏低')}</Badge>}
          </span>
        }
      />
      <StatCard
        label={t('clientDistObs.kpiMachines', '活跃机器')}
        value={summary.activeMachines.toLocaleString()}
        icon={<Users className="size-3.5" />}
        sub={
          <span className="flex items-center gap-1.5">
            <DeltaBadge now={summary.activeMachines} before={compare?.activeMachines} />
            {!summary.activeMachinesExact && <Badge variant="outline" className="text-muted-foreground">{t('clientDistObs.approx', '近似')}</Badge>}
          </span>
        }
      />
      <StatCard
        label={t('clientDistObs.kpiDownloads', '下载字节')}
        value={fmtBytes(summary.downloadBytes)}
        icon={<HardDrive className="size-3.5" />}
        sub={<DeltaBadge now={summary.downloadBytes} before={compare?.downloadBytes} />}
      />
      <StatCard
        label={t('clientDistObs.kpiPulls', 'Manifest 拉取')}
        value={summary.manifestPulls.toLocaleString()}
        icon={<Download className="size-3.5" />}
        sub={<DeltaBadge now={summary.manifestPulls} before={compare?.manifestPulls} />}
      />
    </div>
  )
}

export default InsightCards
