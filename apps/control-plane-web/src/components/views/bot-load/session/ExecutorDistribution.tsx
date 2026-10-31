import { useTranslation } from 'react-i18next'
import type { BotLoadAllocation, BotLoadMetricPoint } from '@/lib/bot-load/bot-load-types'
import { formatFileSize } from '@/lib/shared/format-file-size'
import { Button } from '@jianmanager/ui/components/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'

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
      <div>
        {/* Table 自带外层横向滚动容器，故去掉原来的 overflow-x-auto 外壳 */}
        <Table className="text-left text-sm">
          <TableHeader className="bg-muted/40">
            <TableRow>
              <TableHead>{t('botLoad.node')}</TableHead>
              <TableHead>{t('botLoad.planned')}</TableHead>
              <TableHead>{t('botLoad.active')}</TableHead>
              <TableHead>{t('botLoad.health')}</TableHead>
              <TableHead>RSS</TableHead>
              <TableHead>eventLoop p95</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {allocations.map((a) => {
              const live = byNode.get(a.executorNodeId)
              return (
                <TableRow key={a.batchId}>
                  <TableCell className="font-medium">{a.executorNodeName}</TableCell>
                  <TableCell className="tabular-nums">{a.plannedCount}</TableCell>
                  <TableCell className="tabular-nums">{live?.activeBots ?? '—'}</TableCell>
                  <TableCell>{live?.health ?? '—'}</TableCell>
                  <TableCell className="tabular-nums">{formatFileSize(live?.rssBytes, { fallback: '—' })}</TableCell>
                  <TableCell className="tabular-nums">
                    {live?.eventLoopP95Ms != null ? `${live.eventLoopP95Ms} ms` : '—'}
                  </TableCell>
                  <TableCell className="text-right">
                    {onFilterNode && (
                      <Button
                        size="xs"
                        variant="ghost"
                        onClick={() => onFilterNode(a.executorNodeId)}
                      >
                        {t('botLoad.filterBots')}
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}
