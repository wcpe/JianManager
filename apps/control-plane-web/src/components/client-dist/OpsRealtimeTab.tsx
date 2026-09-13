import { useTranslation } from 'react-i18next'
import { AlertTriangle, Download, Users } from 'lucide-react'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { Button } from '@jianmanager/ui/components/button'
import { Panel } from '@jianmanager/ui/components/panel'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import type { ChartSeries } from '@jianmanager/ui'
import type { StatsIP } from '@/api/clientStats'
import type { ClientDistErrorSummary, ClientDistRealtime } from '@/api/clientDistEvents'
import {
  LinkableDistPanel,
  TrendCard,
  ErrorPanel,
  distBuckets,
  fmtTime,
  kindLabel,
  type RuntimeLink,
} from './ops-shared'

/**
 * 页面 B · 实时监控 Tab（FR-430，迁自旧监控页 `MonitorTab`）。
 * 展示近 1h 分发请求聚合 + 近 24h 请求速率趋势 + 来源 IP / 错误码 TopN + 失败样例 + 最近错误。
 * 安全侧「异常请求」为同 Tab 的 `seg=events` 分档，由页面外壳渲染。
 */
export default function OpsRealtimeTab({
  realtime,
  errors,
  isError,
  onLink,
}: {
  realtime?: ClientDistRealtime
  errors?: ClientDistErrorSummary
  isError: boolean
  onLink: (link: RuntimeLink) => void
}) {
  const { t } = useTranslation()
  const series = realtime?.requestRate24h ?? []
  const requestSeries: ChartSeries[] = [
    { key: 'manifest', name: t('clientDistOps.kindManifest'), points: series.map((p) => ({ ts: p.ts, value: p.manifest })) },
    { key: 'artifact', name: t('clientDistOps.kindArtifact'), points: series.map((p) => ({ ts: p.ts, value: p.artifact })) },
    { key: 'error', name: t('clientDistOps.errorRequests'), points: series.map((p) => ({ ts: p.ts, value: p.error })) },
  ]
  const ipBuckets = distBuckets(realtime?.topIps1h ?? [], (r: StatsIP) => r.count, (r: StatsIP) => r.ip || '—')
  const errorBuckets = distBuckets(errors?.topErrors ?? [], (row) => row.count, (row) => row.errCode)

  if (isError) return <ErrorPanel title={t('clientDistOps.tabMonitor')} message={t('clientDistOps.loadError')} />
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <StatCard icon={<Download className="size-3.5" />} label={t('clientDistOps.manifestPulls1h')} value={String(realtime?.summary1h.manifestPulls ?? 0)} />
        <StatCard icon={<Download className="size-3.5" />} label={t('clientDistOps.artifactPulls1h')} value={String(realtime?.summary1h.artifactPulls ?? 0)} />
        <StatCard icon={<AlertTriangle className="size-3.5" />} tone="danger" label={t('clientDistOps.errorRequests1h')} value={String(realtime?.summary1h.errorRequests ?? 0)} />
        <StatCard icon={<Users className="size-3.5" />} label={t('clientDistOps.activeClients1h')} value={String(realtime?.summary1h.activeMachines ?? 0)} />
      </div>
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <TrendCard title={t('clientDistOps.requestRate24h')} series={requestSeries} valueFormatter={(v) => String(Math.round(v))} empty={t('clientDistOps.empty')} />
        <LinkableDistPanel title={t('clientDistOps.topIps1h')} buckets={ipBuckets} empty={t('clientDistOps.empty')} onPick={(ip) => onLink({ ip })} />
      </div>
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <LinkableDistPanel title={t('clientDistOps.errorTopN')} buckets={errorBuckets} empty={t('clientDistOps.noRecentErrors')} onPick={(errCode) => onLink({ errCode })} />
        <Panel title={t('clientDistOps.failureSamples')}>
          <FailureSampleTable rows={errors?.samples ?? []} onLink={onLink} />
        </Panel>
      </div>
      <Panel title={t('clientDistOps.recentErrors')}>
        <RecentErrorTable rows={realtime?.recentErrors ?? []} />
      </Panel>
    </div>
  )
}

function RecentErrorTable({ rows }: { rows: ClientDistRealtime['recentErrors'] }) {
  const { t } = useTranslation()
  if (rows.length === 0) return <p className="py-6 text-center text-sm text-muted-foreground">{t('clientDistOps.noRecentErrors')}</p>
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t('clientDistOps.colTime')}</TableHead>
          <TableHead>{t('clientDistOps.colChannel')}</TableHead>
          <TableHead>{t('clientDistOps.colKind')}</TableHead>
          <TableHead>{t('clientDistOps.colTarget')}</TableHead>
          <TableHead>{t('clientDistOps.colIp')}</TableHead>
          <TableHead>{t('clientDistOps.colStatus')}</TableHead>
          <TableHead>{t('clientDistOps.colErrCode')}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((e) => (
          <TableRow key={e.id}>
            <TableCell className="tabular-nums text-muted-foreground">{fmtTime(e.time)}</TableCell>
            <TableCell>{e.channelId || '—'}</TableCell>
            <TableCell>{kindLabel(e.kind, t)}</TableCell>
            <TableCell className="font-mono text-xs">{e.target || '—'}</TableCell>
            <TableCell>{e.ip || '—'}</TableCell>
            <TableCell className="tabular-nums">{e.status}</TableCell>
            <TableCell className="font-mono text-xs">{e.errCode || '—'}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}

function FailureSampleTable({ rows, onLink }: { rows: ClientDistErrorSummary['samples']; onLink: (link: RuntimeLink) => void }) {
  const { t } = useTranslation()
  if (rows.length === 0) return <p className="py-6 text-center text-sm text-muted-foreground">{t('clientDistOps.noRecentErrors')}</p>
  return (
    <Table>
      <TableHeader><TableRow><TableHead>{t('clientDistOps.colTime')}</TableHead><TableHead>{t('clientDistOps.colChannel')}</TableHead><TableHead>{t('clientDistOps.colErrCode')}</TableHead><TableHead>{t('clientDistOps.colMachine')}</TableHead></TableRow></TableHeader>
      <TableBody>{rows.map((row) => (
        <TableRow key={row.id}>
          <TableCell className="tabular-nums text-muted-foreground">{fmtTime(row.time)}</TableCell>
          <TableCell>{row.channelId || '—'}</TableCell>
          <TableCell><Button type="button" variant="link" size="xs" className="font-mono" onClick={() => onLink({ errCode: row.errCode })}>{row.errCode}</Button></TableCell>
          <TableCell className="font-mono text-xs">{row.machineId || '—'}</TableCell>
        </TableRow>
      ))}</TableBody>
    </Table>
  )
}
