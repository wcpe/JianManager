import { useTranslation } from 'react-i18next'
import type { BotLoadAllocation, BotLoadNodeCapacity, BotLoadPreflightResult } from '@/api/botLoad'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import { cn } from '@jianmanager/ui'

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
export default function CapacityPlan({
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
                        <input
                          type="checkbox"
                          checked={selectedNodeIds.includes(n.nodeId)}
                          disabled={!selectable}
                          onChange={() => onToggleNode(n.nodeId)}
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
        <div className="rounded-lg border border-destructive/40 bg-destructive/10 p-3" role="alert">
          <p className="mb-1 text-sm font-semibold text-destructive">{t('botsLoad.blockers')}</p>
          <ul className="list-disc space-y-1 pl-5 text-sm">
            {preflight.blockers.map((b, i) => (
              <li key={i}>{b.message}</li>
            ))}
          </ul>
        </div>
      )}

      {preflight && preflight.warnings.length > 0 && (
        <div className="rounded-lg border border-status-warning/40 bg-status-warning/10 p-3">
          <p className="mb-1 text-sm font-semibold">{t('botsLoad.warnings')}</p>
          <ul className="list-disc space-y-1 pl-5 text-sm text-muted-foreground">
            {preflight.warnings.map((w, i) => (
              <li key={i}>{w.message}</li>
            ))}
          </ul>
        </div>
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
    <div className="rounded-lg border bg-card p-3 shadow-soft">
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className={cn('mt-1 text-lg font-semibold tabular-nums', danger && 'text-destructive')}>{value}</p>
    </div>
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
