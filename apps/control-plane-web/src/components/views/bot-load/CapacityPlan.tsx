import { useTranslation } from 'react-i18next'
import { AlertTriangle } from 'lucide-react'
import type { BotLoadAllocation, BotLoadNodeCapacity, BotLoadPreflightResult } from '@/lib/bot-load/bot-load-types'
import { Alert, AlertDescription, AlertTitle } from '@jianmanager/ui/components/alert'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import { cn } from '@jianmanager/ui'
import { Card } from '@jianmanager/ui/components/card'
import { Checkbox } from '@jianmanager/ui/components/checkbox'

interface CapacityPlanProps {
  nodes?: BotLoadNodeCapacity[]
  totalCapacity?: number
  availableCapacity?: number
  preflight?: BotLoadPreflightResult | null
  selectedNodeIds: number[]
  onToggleNode: (nodeId: number) => void
  executorMode: 'auto' | 'manual'
  loading?: boolean
}

/** 容量表 + 预检 allocations / blockers / warnings。 */
export function CapacityPlan({
  nodes = [],
  totalCapacity = 0,
  availableCapacity = 0,
  preflight,
  selectedNodeIds,
  onToggleNode,
  executorMode,
  loading,
}: CapacityPlanProps) {
  const { t } = useTranslation()

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4" aria-live="polite">
        <Metric label={t('botsLoad.totalCapacity')} value={String(totalCapacity)} />
        <Metric label={t('botsLoad.availableCapacity')} value={String(availableCapacity)} />
        {preflight && (
          <>
            <Metric label={t('botsLoad.preflightTarget')} value={String(preflight.targetBots)} />
            <Metric
              label={t('botsLoad.preflightReady')}
              value={preflight.ready ? t('common.yes') : t('common.no')}
              danger={!preflight.ready}
            />
          </>
        )}
      </div>

      {loading ? (
        <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <Table>
            <TableHeader className="bg-muted/40">
              <TableRow>
                {executorMode === 'manual' && <TableHead className="w-10" />}
                <TableHead>{t('botsLoad.nodeName')}</TableHead>
                <TableHead>{t('botsLoad.nodeState')}</TableHead>
                <TableHead className="text-right">{t('botsLoad.maxBots')}</TableHead>
                <TableHead className="text-right">{t('botsLoad.activeBots')}</TableHead>
                <TableHead className="text-right">{t('botsLoad.reservedBots')}</TableHead>
                <TableHead className="text-right">{t('botsLoad.availableBots')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {nodes.map((n) => {
                const selectable = n.online && n.botWorkerReady && !n.legacy && n.availableBots > 0
                return (
                  <TableRow key={n.nodeId} className={cn(!selectable && 'opacity-60')}>
                    {executorMode === 'manual' && (
                      <TableCell>
                        <Checkbox
                          checked={selectedNodeIds.includes(n.nodeId)}
                          disabled={!selectable}
                          onCheckedChange={() => onToggleNode(n.nodeId)}
                          aria-label={n.nodeName}
                        />
                      </TableCell>
                    )}
                    <TableCell className="font-medium">{n.nodeName}</TableCell>
                    <TableCell className="text-xs">
                      {!n.online
                        ? t('botsLoad.nodeOffline')
                        : n.legacy
                          ? t('botsLoad.nodeLegacy')
                          : !n.botWorkerReady
                            ? t('botsLoad.nodeNotReady')
                            : t('botsLoad.nodeReady')}
                      {n.unavailableReason ? ` · ${n.unavailableReason}` : ''}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{n.maxBots}</TableCell>
                    <TableCell className="text-right tabular-nums">{n.activeBots}</TableCell>
                    <TableCell className="text-right tabular-nums">{n.reservedBots}</TableCell>
                    <TableCell className="text-right tabular-nums">{n.availableBots}</TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      )}

      {preflight && preflight.allocations.length > 0 && (
        <AllocationsTable allocations={preflight.allocations} />
      )}

      {preflight && preflight.blockers.length > 0 && (
        <Alert variant="destructive">
          <AlertTriangle />
          <AlertTitle>{t('botsLoad.blockers')}</AlertTitle>
          <AlertDescription>
            {/* AlertDescription 是 justify-items-start 的 grid：列表不显式占满会缩成内容宽，
                导致条目在长文案下换行位置不一致。 */}
            <ul className="w-full list-disc space-y-1 pl-5">
              {preflight.blockers.map((b, i) => (
                <li key={i}>{b.message}</li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}

      {preflight && preflight.warnings.length > 0 && (
        <Alert variant="warning">
          <AlertTriangle />
          <AlertTitle>{t('botsLoad.warnings')}</AlertTitle>
          <AlertDescription>
            {/* 同上：列表需占满描述列宽，条目换行才一致。 */}
            <ul className="w-full list-disc space-y-1 pl-5">
              {preflight.warnings.map((w, i) => (
                <li key={i}>{w.message}</li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}

      {preflight?.planToken && preflight.expiresAt && (
        <p className="text-xs text-muted-foreground" aria-live="polite">
          {t('botsLoad.planExpiresAt', { at: new Date(preflight.expiresAt).toLocaleTimeString() })}
        </p>
      )}
    </div>
  )
}

function Metric({ label, value, danger }: { label: string; value: string; danger?: boolean }) {
  return (
    <Card className="p-3">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className={cn('mt-1 text-lg font-semibold tabular-nums', danger && 'text-destructive')}>{value}</p>
    </Card>
  )
}

function AllocationsTable({ allocations }: { allocations: BotLoadAllocation[] }) {
  const { t } = useTranslation()
  return (
    <div className="overflow-x-auto rounded-lg border">
      <Table>
        <TableHeader className="bg-muted/40">
          <TableRow>
            <TableHead>{t('botsLoad.allocationOrdinal')}</TableHead>
            <TableHead>{t('botsLoad.nodeName')}</TableHead>
            <TableHead className="text-right">{t('botsLoad.plannedCount')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {allocations.map((a) => (
            <TableRow key={a.batchId}>
              <TableCell>{a.ordinal + 1}</TableCell>
              <TableCell>{a.executorNodeName}</TableCell>
              <TableCell className="text-right tabular-nums">{a.plannedCount}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}
