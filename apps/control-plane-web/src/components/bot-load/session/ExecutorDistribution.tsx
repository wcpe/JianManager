import { useTranslation } from 'react-i18next'
import type { BotLoadAllocation, BotLoadMetricPoint } from '@/lib/bot-load/types'
import { formatBytes } from '@/lib/bot-load/metrics'
import { Button } from '@jianmanager/ui/components/button'

export function ExecutorDistribution({
  allocations,
  latestMetric,
  onFilterNode,
}: {
  allocations: BotLoadAllocation[]
  latestMetric?: BotLoadMetricPoint | null
  onFilterNode?: (nodeId: number) => void
}) {
  const { t } = useTranslation()
  const byNode = new Map(latestMetric?.executor.map((e) => [e.nodeId, e]) ?? [])

  if (!allocations?.length) {
    return <p className="text-sm text-muted-foreground">{t('botLoad.noExecutors')}</p>
  }

  return (
    <div data-testid="executor-distribution" className="space-y-2">
      <h3 className="text-sm font-semibold">{t('botLoad.executorDistribution')}</h3>
      <div className="overflow-x-auto">
        <table className="w-full text-left text-sm">
          <thead className="border-b text-xs text-muted-foreground">
            <tr>
              <th className="py-1.5 pr-2">{t('botLoad.node')}</th>
              <th className="py-1.5 pr-2">{t('botLoad.planned')}</th>
              <th className="py-1.5 pr-2">{t('botLoad.active')}</th>
              <th className="py-1.5 pr-2">{t('botLoad.health')}</th>
              <th className="py-1.5 pr-2">RSS</th>
              <th className="py-1.5 pr-2">eventLoop p95</th>
              <th className="py-1.5" />
            </tr>
          </thead>
          <tbody>
            {allocations.map((a) => {
              const live = byNode.get(a.executorNodeId)
              return (
                <tr key={a.batchId} className="border-b border-border/50">
                  <td className="py-1.5 pr-2 font-medium">{a.executorNodeName}</td>
                  <td className="py-1.5 pr-2 tabular-nums">{a.plannedCount}</td>
                  <td className="py-1.5 pr-2 tabular-nums">{live?.activeBots ?? '—'}</td>
                  <td className="py-1.5 pr-2">{live?.health ?? '—'}</td>
                  <td className="py-1.5 pr-2 tabular-nums">{formatBytes(live?.rssBytes)}</td>
                  <td className="py-1.5 pr-2 tabular-nums">
                    {live?.eventLoopP95Ms != null ? `${live.eventLoopP95Ms} ms` : '—'}
                  </td>
                  <td className="py-1.5 text-right">
                    {onFilterNode && (
                      <Button
                        size="xs"
                        variant="ghost"
                        onClick={() => onFilterNode(a.executorNodeId)}
                      >
                        {t('botLoad.filterBots')}
                      </Button>
                    )}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}
