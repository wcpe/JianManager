import { useTranslation } from 'react-i18next'
import { TrendingUp } from 'lucide-react'
import { useCapacityForecast, type ForecastResult } from '@/api/metrics'
import { Panel } from '@jianmanager/ui/components/panel'
import type { MetricRange } from '@jianmanager/ui'
import { fmtBytes } from './format'

/** 待预测的「已用」指标 → i18n 标签键。 */
const CAPACITY_METRIC_LABEL: Record<string, string> = {
  inst_heap_used: 'capacity.metricHeapUsed',
  node_disk_used: 'capacity.metricDiskUsed',
  node_mem_used: 'capacity.metricMemUsed',
}

/** 每秒增量 → 人类可读（字节类指标按 /s 展示）。 */
function fmtSlope(metricKey: string, slope: number): string {
  if (!Number.isFinite(slope)) return '--'
  if (metricKey.startsWith('node_') || metricKey.startsWith('inst_heap')) {
    return `${fmtBytes(slope)}/s`
  }
  return slope.toFixed(3)
}

/** 80% 置信区间 → 「a ~ b 天」。 */
function fmtCI(low: number | null, high: number | null): string | null {
  if (low == null || high == null) return null
  return `${low.toFixed(2)} ~ ${high.toFixed(2)}`
}

/**
 * 容量预测一行。`confidence=insufficient` 时**不渲染任何预测数字**，只显示后端给的原因
 * （「无增长趋势」/「样本不足」/「缺少容量上限」）——避免把「不知道」伪装成「不会耗尽」。
 */
function ForecastRow({ item }: { item: ForecastResult }) {
  const { t } = useTranslation()
  const label = t(CAPACITY_METRIC_LABEL[item.metricKey] ?? item.metricKey)
  const ci = fmtCI(item.exhaustLowDays, item.exhaustHighDays)
  const insufficient = item.confidence === 'insufficient'
  return (
    <div className="grid grid-cols-[1fr_auto] items-start gap-3 border-b border-border py-2.5 last:border-b-0">
      <div className="min-w-0">
        <p className="truncate text-sm font-medium">{label}</p>
        <p className="mt-0.5 text-[11px] text-muted-foreground">
          {t('capacity.now')}: {fmtBytes(item.nowValue)} · {t('capacity.limit')}: {fmtBytes(item.limitValue)} ·{' '}
          {t('capacity.slope')}: {fmtSlope(item.metricKey, item.slopePerSec)}
        </p>
        {insufficient && item.note && <p className="mt-1 text-[11px] text-muted-foreground">{item.note}</p>}
      </div>
      <div className="text-right">
        {insufficient ? (
          <span className="text-sm text-muted-foreground">{t('capacity.insufficient')}</span>
        ) : (
          <>
            <p className="text-sm font-semibold tabular-nums">
              {item.exhaustLowDays == null ? '--' : t('capacity.days', { days: item.exhaustLowDays.toFixed(2) })}
            </p>
            {ci && (
              <p className="mt-0.5 text-[11px] text-muted-foreground">
                {t('capacity.ci80')}: {t('capacity.range', { low: ci.split(' ~ ')[0], high: ci.split(' ~ ')[1] })}
              </p>
            )}
            <p className="mt-0.5 text-[11px] text-muted-foreground">
              {t('capacity.confidence')}: {t(`capacity.${item.confidence}`)}
            </p>
          </>
        )}
      </div>
    </div>
  )
}

/** 容量预测卡的 props。 */
interface CapacityForecastCardProps {
  /** 目标维度：node（节点资源）或 instance（实例堆内存）。 */
  scope: 'node' | 'instance'
  /** 目标 UUID：node 维度为节点 UUID，instance 维度为实例 UUID。 */
  targetId: string
  /** 统计窗口（同时决定样本档位；7d 为推荐值）。 */
  range: MetricRange
  /** 待预测的「已用」指标键；缺省由后端按 scope 取默认集合。 */
  metrics?: string[]
}

/**
 * 容量预测卡片（FR-464）：Theil–Sen 稳健线性外推 + 80% 置信区间，按需查询（60s 刷新）。
 * 只在「有增长且显著」时给出耗尽时间；无增长/样本不足/缺上限一律显式标注，不伪造预测。
 */
export function CapacityForecastCard({ scope, targetId, range, metrics }: CapacityForecastCardProps) {
  const { t } = useTranslation()
  const { data, isError, isLoading } = useCapacityForecast({ scope, targetId, range, metrics })
  const forecasts = data?.forecasts ?? []

  return (
    <Panel title={t('capacity.title')} icon={<TrendingUp className="size-4" />}>
      {isError ? (
        <p className="py-4 text-center text-sm text-muted-foreground">{t('capacity.error')}</p>
      ) : isLoading ? (
        <p className="py-4 text-center text-sm text-muted-foreground">{t('capacity.loading')}</p>
      ) : forecasts.length === 0 ? (
        <p className="py-4 text-center text-sm text-muted-foreground">{t('capacity.empty')}</p>
      ) : (
        <div>
          {forecasts.map((item) => (
            <ForecastRow key={item.metricKey} item={item} />
          ))}
        </div>
      )}
    </Panel>
  )
}
