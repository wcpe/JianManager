import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { Activity, AlertTriangle, Clock, Download, RefreshCw, Search, Server, Users } from 'lucide-react'
import { useClientChannels } from '@/api/clientChannels'
import { useClientStats, type ClientDistStats, type StatsIP } from '@/api/clientStats'
import {
  useClientDistErrorSummary,
  useClientDistEventDetail,
  useClientDistEventSearch,
  useClientDistRealtime,
  type ClientDistEvent,
  type ClientDistErrorSummary,
  type ClientDistEventDetail,
  type ClientDistRealtime,
} from '@/api/clientDistEvents'
import {
  useClientRuntimeOverview,
  type ClientRuntimeOverview,
  type ClientRuntimeState,
  type RuntimeUpdateSeriesPoint,
} from '@/api/clientRuntimeStates'
import { useAuthStore } from '@/stores/auth'
import { Panel } from '@jianmanager/ui/components/panel'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@jianmanager/ui/components/tabs'
import { RangePicker, type MetricRange } from '@jianmanager/ui'
import { TimeSeriesChart, type ChartSeries } from '@jianmanager/ui'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import type { DistBucket } from '@/lib/platform-stats'
import { buildClientDistHref, readClientDistQuery, updateClientDistQuery } from '@/lib/client-dist-query'
import { useTabParam } from '@/lib/use-tab-param'
import ClientDistExportButton from '@/components/ClientDistExportButton'
import {
  KPI_I18N,
  activeClientsHintKey,
  formatKpiRate,
  resolveActiveClients,
  resolveRequestRates,
} from '@/lib/client-dist-kpi'

const ROLE_PLATFORM_ADMIN = 10
const ALL_CHANNELS = '__all__'
const ALL = '__all__'

type PageTab = 'statistics' | 'monitor' | 'logs' | 'clients'
type RuntimeLink = {
  machineId?: string
  runtimeVersion?: number
  version?: number
  coreVersion?: string
  platform?: string
  lag?: number
  errCode?: string
  ip?: string
}

function toApiRange(r: MetricRange): string {
  switch (r) {
    case '1h':
    case '6h':
    case '24h':
      return '24h'
    case '7d':
      return '7d'
    case '30d':
      return '30d'
    case '90d':
      return '90d'
    case '1y':
      return '180d'
    default:
      return '7d'
  }
}

function toStatsDays(r: MetricRange): number {
  switch (r) {
    case '1h':
    case '6h':
    case '24h':
      return 1
    case '7d':
      return 7
    case '30d':
      return 30
    case '90d':
      return 90
    case '1y':
      return 180
    default:
      return 7
  }
}

function fmtBytes(b: number): string {
  if (!Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1e9) return `${(b / 1024 / 1024 / 1024).toFixed(1)}G`
  if (b >= 1e6) return `${(b / 1024 / 1024).toFixed(0)}M`
  if (b >= 1e3) return `${(b / 1024).toFixed(0)}K`
  return String(b)
}

function fmtRate(r: number): string {
  return `${((Number.isFinite(r) ? r : 0) * 100).toFixed(1)}%`
}

function fmtTime(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}

function platformLabel(os: string): string {
  if (!os) return '—'
  const map: Record<string, string> = { windows: 'Windows', macos: 'macOS', linux: 'Linux' }
  return map[os] ?? os
}

function distBuckets<T>(items: T[], countOf: (it: T) => number, label: (it: T) => string): DistBucket[] {
  const total = items.reduce((s, it) => s + countOf(it), 0)
  return items
    .map((it) => ({ key: label(it), count: countOf(it), pct: total > 0 ? countOf(it) / total : 0 }))
    .sort((a, b) => b.count - a.count)
}

function DistPanel({ title, buckets, empty }: { title: string; buckets: DistBucket[]; empty: string }) {
  return (
    <Panel title={title}>
      {buckets.length === 0 ? (
        <p className="py-6 text-center text-sm text-muted-foreground">{empty}</p>
      ) : (
        <ul className="space-y-2.5">
          {buckets.map((b) => (
            <li key={b.key} className="space-y-1">
              <div className="flex items-baseline justify-between text-sm">
                <span className="font-medium">{b.key}</span>
                <span className="tabular-nums text-muted-foreground">
                  {b.count}
                  <span className="ml-1.5 text-xs">{(b.pct * 100).toFixed(0)}%</span>
                </span>
              </div>
              <MiniBar value={b.pct * 100} level="info" />
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}

function LinkableDistPanel({
  title,
  buckets,
  empty,
  onPick,
}: {
  title: string
  buckets: DistBucket[]
  empty: string
  onPick: (key: string) => void
}) {
  return (
    <Panel title={title}>
      {buckets.length === 0 ? (
        <p className="py-6 text-center text-sm text-muted-foreground">{empty}</p>
      ) : (
        <ul className="space-y-2.5">
          {buckets.map((b) => (
            <li key={b.key} className="space-y-1">
              <button type="button" className="flex w-full items-baseline justify-between text-left text-sm" onClick={() => onPick(b.key)}>
                <span className="font-medium text-primary underline-offset-2 hover:underline">{b.key}</span>
                <span className="tabular-nums text-muted-foreground">
                  {b.count}
                  <span className="ml-1.5 text-xs">{(b.pct * 100).toFixed(0)}%</span>
                </span>
              </button>
              <MiniBar value={b.pct * 100} level="info" />
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}

function TrendCard({
  title,
  series,
  valueFormatter,
  yDomain,
  empty,
}: {
  title: string
  series: ChartSeries[]
  valueFormatter?: (v: number) => string
  yDomain?: [number | 'auto', number | 'auto']
  empty: string
}) {
  return (
    <Panel title={title} bodyClassName="p-3">
      <TimeSeriesChart series={series} valueFormatter={valueFormatter} yDomain={yDomain} emptyHint={empty} />
    </Panel>
  )
}

function ResultBadge({ status }: { status: number }) {
  const { t } = useTranslation()
  const failure = status >= 400
  return (
    <Badge variant={failure ? 'destructive' : 'secondary'}>
      {failure ? t('clientDistMonitor.resultFailure') : t('clientDistMonitor.resultSuccess')}
    </Badge>
  )
}

function kindLabel(kind: string, t: (key: string) => string): string {
  if (kind === 'manifest') return t('clientDistMonitor.kindManifest')
  if (kind === 'artifact') return t('clientDistMonitor.kindArtifact')
  return kind || '—'
}

function targetOf(e: ClientDistEvent): string {
  if (e.kind === 'artifact') return e.artifactSha ? e.artifactSha.slice(0, 12) : '—'
  return e.version > 0 ? `v${e.version}` : '—'
}

function StatisticsTab({ stats, isError, isLoading, onLink }: { stats?: ClientDistStats; isError: boolean; isLoading: boolean; onLink: (link: RuntimeLink) => void }) {
  const { t } = useTranslation()
  // FR-356：统计 Tab 仅展示「请求侧」KPI；请求成功率≠更新成功率（后者在客户端 Tab / 频道统计）。
  const active = resolveActiveClients(null, stats)
  const requestRates = resolveRequestRates(stats)
  const activeHintKey = activeClientsHintKey(active.exactness, active.source)
  const downloadSeries: ChartSeries[] = [
    {
      key: 'requests',
      name: t(KPI_I18N.downloadRequests, t('clientDistMonitor.totalRequests')),
      points: (stats?.downloads ?? []).map((p) => ({ ts: p.day, value: p.requests })),
    },
  ]
  const bytesSeries: ChartSeries[] = [
    {
      key: 'bytes',
      name: t(KPI_I18N.downloadBytes, t('clientDistMonitor.downloadBytes')),
      points: (stats?.downloads ?? []).map((p) => ({ ts: p.day, value: p.bytes })),
    },
  ]
  const versionBuckets = distBuckets(stats?.versions ?? [], (v) => v.requests, (v) => `v${v.version}`)
  const resultBuckets = distBuckets(stats?.results ?? [], (r) => r.count, (r) => resultLabel(r.result, t))
  const ipBuckets = distBuckets(stats?.topIps ?? [], (r) => r.count, (r) => r.ip || '—')

  if (isError) return <ErrorPanel title={t('clientDistMonitor.tabStatistics')} message={t('clientDistMonitor.loadError')} />
  return (
    <div className="space-y-4" data-kpi-scope="client-dist-monitor-statistics">
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
        <StatCard icon={<Download className="size-3.5" />} label={t(KPI_I18N.manifestPulls, t('clientDistMonitor.manifestPulls'))} value={String(kindRequests(stats, 'manifest'))} />
        <StatCard icon={<Download className="size-3.5" />} label={t(KPI_I18N.artifactPulls, t('clientDistMonitor.artifactPulls'))} value={String(kindRequests(stats, 'artifact'))} />
        <StatCard icon={<Server className="size-3.5" />} label={t(KPI_I18N.downloadBytes, t('clientDistMonitor.downloadBytes'))} value={fmtBytes(totalBytes(stats))} />
        <StatCard
          icon={<Users className="size-3.5" />}
          label={t(KPI_I18N.activeClients, t('clientDistMonitor.activeClients'))}
          value={String(active.value)}
          sub={activeHintKey ? t(activeHintKey) : t('clientDistMonitor.fromRequests')}
        />
        <StatCard
          icon={<Activity className="size-3.5" />}
          tone="success"
          label={t(KPI_I18N.requestSuccessRate, t('clientDistMonitor.requestSuccessRate'))}
          value={formatKpiRate(requestRates.successRate, fmtRate(0))}
        />
        <StatCard
          icon={<AlertTriangle className="size-3.5" />}
          tone="warning"
          label={t(KPI_I18N.requestFailureRate, t('clientDistMonitor.requestFailureRate'))}
          value={formatKpiRate(requestRates.failureRate, fmtRate(0))}
        />
      </div>
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <TrendCard title={t(KPI_I18N.downloadTrend, t('clientDistMonitor.requestTrend'))} series={downloadSeries} valueFormatter={(v) => String(Math.round(v))} empty={isLoading ? t('common.loading') : t('clientDistMonitor.empty')} />
        <TrendCard title={t('clientDistMonitor.downloadBytesTrend')} series={bytesSeries} valueFormatter={fmtBytes} empty={isLoading ? t('common.loading') : t('clientDistMonitor.empty')} />
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <LinkableDistPanel title={t('clientDistMonitor.versionDist')} buckets={versionBuckets} empty={t('clientDistMonitor.empty')} onPick={(key) => onLink({ version: Number(key.replace(/^v/, '')) })} />
        <DistPanel title={t('clientDistMonitor.resultDist')} buckets={resultBuckets} empty={t('clientDistMonitor.empty')} />
        <LinkableDistPanel title={t('clientDistMonitor.topIps')} buckets={ipBuckets} empty={t('clientDistMonitor.empty')} onPick={(ip) => onLink({ ip })} />
      </div>
    </div>
  )
}

function MonitorTab({ realtime, errors, isError, onLink }: { realtime?: ClientDistRealtime; errors?: ClientDistErrorSummary; isError: boolean; onLink: (link: RuntimeLink) => void }) {
  const { t } = useTranslation()
  const series = realtime?.requestRate24h ?? []
  const requestSeries: ChartSeries[] = [
    { key: 'manifest', name: t('clientDistMonitor.kindManifest'), points: series.map((p) => ({ ts: p.ts, value: p.manifest })) },
    { key: 'artifact', name: t('clientDistMonitor.kindArtifact'), points: series.map((p) => ({ ts: p.ts, value: p.artifact })) },
    { key: 'error', name: t('clientDistMonitor.errorRequests'), points: series.map((p) => ({ ts: p.ts, value: p.error })) },
  ]
  const ipBuckets = distBuckets(realtime?.topIps1h ?? [], (r: StatsIP) => r.count, (r: StatsIP) => r.ip || '—')
  const errorBuckets = distBuckets(errors?.topErrors ?? [], (row) => row.count, (row) => row.errCode)

  if (isError) return <ErrorPanel title={t('clientDistMonitor.tabMonitor')} message={t('clientDistMonitor.loadError')} />
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <StatCard icon={<Download className="size-3.5" />} label={t('clientDistMonitor.manifestPulls1h')} value={String(realtime?.summary1h.manifestPulls ?? 0)} />
        <StatCard icon={<Download className="size-3.5" />} label={t('clientDistMonitor.artifactPulls1h')} value={String(realtime?.summary1h.artifactPulls ?? 0)} />
        <StatCard icon={<AlertTriangle className="size-3.5" />} tone="danger" label={t('clientDistMonitor.errorRequests1h')} value={String(realtime?.summary1h.errorRequests ?? 0)} />
        <StatCard icon={<Users className="size-3.5" />} label={t('clientDistMonitor.activeClients1h')} value={String(realtime?.summary1h.activeMachines ?? 0)} />
      </div>
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <TrendCard title={t('clientDistMonitor.requestRate24h')} series={requestSeries} valueFormatter={(v) => String(Math.round(v))} empty={t('clientDistMonitor.empty')} />
        <LinkableDistPanel title={t('clientDistMonitor.topIps1h')} buckets={ipBuckets} empty={t('clientDistMonitor.empty')} onPick={(ip) => onLink({ ip })} />
      </div>
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <LinkableDistPanel title={t('clientDistMonitor.errorTopN')} buckets={errorBuckets} empty={t('clientDistMonitor.noRecentErrors')} onPick={(errCode) => onLink({ errCode })} />
        <Panel title={t('clientDistMonitor.failureSamples')}>
          <FailureSampleTable rows={errors?.samples ?? []} onLink={onLink} />
        </Panel>
      </div>
      <Panel title={t('clientDistMonitor.recentErrors')}>
        <RecentErrorTable rows={realtime?.recentErrors ?? []} />
      </Panel>
    </div>
  )
}

function RecentErrorTable({ rows }: { rows: ClientDistRealtime['recentErrors'] }) {
  const { t } = useTranslation()
  if (rows.length === 0) return <p className="py-6 text-center text-sm text-muted-foreground">{t('clientDistMonitor.noRecentErrors')}</p>
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t('clientDistMonitor.colTime')}</TableHead>
          <TableHead>{t('clientDistMonitor.colChannel')}</TableHead>
          <TableHead>{t('clientDistMonitor.colKind')}</TableHead>
          <TableHead>{t('clientDistMonitor.colTarget')}</TableHead>
          <TableHead>{t('clientDistMonitor.colIp')}</TableHead>
          <TableHead>{t('clientDistMonitor.colStatus')}</TableHead>
          <TableHead>{t('clientDistMonitor.colErrCode')}</TableHead>
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
  if (rows.length === 0) return <p className="py-6 text-center text-sm text-muted-foreground">{t('clientDistMonitor.noRecentErrors')}</p>
  return (
    <Table>
      <TableHeader><TableRow><TableHead>{t('clientDistMonitor.colTime')}</TableHead><TableHead>{t('clientDistMonitor.colChannel')}</TableHead><TableHead>{t('clientDistMonitor.colErrCode')}</TableHead><TableHead>{t('clientDistMonitor.colMachine')}</TableHead></TableRow></TableHeader>
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

function LogsTab({ channelId, range, enabled, link, onClearLink }: { channelId?: string; range: string; enabled: boolean; link: RuntimeLink; onClearLink: () => void }) {
  const { t } = useTranslation()
  const [outcome, setOutcome] = useState<string>(ALL)
  const [kind, setKind] = useState<string>(ALL)
  const [detailId, setDetailId] = useState<number | null>(null)
  const { data, isError, isLoading } = useClientDistEventSearch({
    channelId,
    ...link,
    kind: kind === ALL ? undefined : kind,
    outcome: outcome === ALL ? '' : (outcome as 'success' | 'failure'),
    page: 1,
    pageSize: 100,
    enabled,
  })
  const events = data?.items ?? []
  const hasLink = Object.values(link).some((v) => v !== undefined && v !== '')
  const filters = (
    <div className="flex flex-wrap items-center gap-2">
      <Select value={outcome} onValueChange={setOutcome}>
        <SelectTrigger size="sm" className="w-32"><SelectValue /></SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL}>{t('clientDistMonitor.outcomeAll')}</SelectItem>
          <SelectItem value="success">{t('clientDistMonitor.outcomeSuccess')}</SelectItem>
          <SelectItem value="failure">{t('clientDistMonitor.outcomeFailure')}</SelectItem>
        </SelectContent>
      </Select>
      <Select value={kind} onValueChange={setKind}>
        <SelectTrigger size="sm" className="w-32"><SelectValue /></SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL}>{t('clientDistMonitor.allKinds')}</SelectItem>
          <SelectItem value="manifest">{t('clientDistMonitor.kindManifest')}</SelectItem>
          <SelectItem value="artifact">{t('clientDistMonitor.kindArtifact')}</SelectItem>
        </SelectContent>
      </Select>
      {hasLink && <Button type="button" variant="outline" size="sm" onClick={onClearLink}>{t('clientDistMonitor.clearLink')}</Button>}
      <ClientDistExportButton
        kind="dist-events"
        filters={{
          channelId,
          range,
          machineId: link.machineId,
          version: link.version,
          errCode: link.errCode,
          ip: link.ip,
          eventKind: kind === ALL ? undefined : (kind as 'manifest' | 'artifact'),
          outcome: outcome === ALL ? undefined : outcome,
        }}
      />
    </div>
  )

  return (
    <>
      <Panel title={t('clientDistMonitor.logsTitle')} actions={filters}>
        <p className="mb-3 text-xs text-muted-foreground">{t('clientDistMonitor.logsHint')}</p>
        {hasLink && <LinkedFilterHint link={link} />}
        {isError ? (
          <p className="py-10 text-center text-sm text-muted-foreground">{t('clientDistMonitor.eventsError')}</p>
        ) : events.length === 0 ? (
          <p className="py-10 text-center text-sm text-muted-foreground">{isLoading ? t('common.loading') : t('clientDistMonitor.eventsEmpty')}</p>
        ) : (
          <EventTable events={events} onDetail={setDetailId} />
        )}
      </Panel>
      <EventDetailDialog id={detailId} open={!!detailId} onOpenChange={(open) => !open && setDetailId(null)} />
    </>
  )
}

function LinkedFilterHint({ link }: { link: RuntimeLink }) {
  const { t } = useTranslation()
  const [searchParams] = useSearchParams()
  const securityHref = buildClientDistHref('/client-dist-security', searchParams, { tab: 'logs' })
  const channelHref = buildClientDistHref('/client-channels', searchParams, { tab: 'stats' })
  return (
    <div className="mb-3 flex flex-wrap items-center gap-2 rounded-lg border bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
      <span>{t('clientDistMonitor.linkedFilter')}</span>
      {link.machineId && <Badge variant="outline" className="font-mono">machine={link.machineId}</Badge>}
      {link.runtimeVersion !== undefined && <Badge variant="outline">version=v{link.runtimeVersion}</Badge>}
      {link.version !== undefined && <Badge variant="outline">version=v{link.version}</Badge>}
      {link.coreVersion && <Badge variant="outline">core={link.coreVersion}</Badge>}
      {link.platform && <Badge variant="outline">platform={platformLabel(link.platform)}</Badge>}
      {link.lag !== undefined && <Badge variant="outline">lag={link.lag}</Badge>}
      {link.errCode && <Badge variant="outline" className="font-mono">errCode={link.errCode}</Badge>}
      {link.ip && <Badge variant="outline" className="font-mono">ip={link.ip}</Badge>}
      <Link className="font-medium text-primary hover:underline" to={securityHref}>打开安全中心</Link>
      <Link className="font-medium text-primary hover:underline" to={channelHref}>打开频道工作台</Link>
    </div>
  )
}

function EventTable({ events, onDetail }: { events: ClientDistEvent[]; onDetail: (id: number) => void }) {
  const { t } = useTranslation()
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t('clientDistMonitor.colTime')}</TableHead>
          <TableHead>{t('clientDistMonitor.colChannel')}</TableHead>
          <TableHead>{t('clientDistMonitor.colPlayer', '玩家名')}</TableHead>
          <TableHead>{t('clientDistMonitor.colMachine')}</TableHead>
          <TableHead>{t('clientDistMonitor.colCoreVersion', 'Core 版本')}</TableHead>
          <TableHead>{t('clientDistMonitor.colKind')}</TableHead>
          <TableHead>{t('clientDistMonitor.colTarget')}</TableHead>
          <TableHead>{t('clientDistMonitor.colIp')}</TableHead>
          <TableHead>{t('clientDistMonitor.colBytes', '字节')}</TableHead>
          <TableHead>{t('clientDistMonitor.colDuration', '耗时')}</TableHead>
          <TableHead>{t('clientDistMonitor.colStatus')}</TableHead>
          <TableHead>{t('clientDistMonitor.colResult')}</TableHead>
          <TableHead>{t('clientDistMonitor.colErrCode')}</TableHead>
          <TableHead>{t('clientDistMonitor.colDetail')}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {events.map((e) => (
          <TableRow key={e.id}>
            <TableCell className="tabular-nums text-muted-foreground">{fmtTime(e.createdAt)}</TableCell>
            <TableCell>{e.channelId || '—'}</TableCell>
            <TableCell className="text-xs">{e.playerName || '—'}</TableCell>
            <TableCell className="font-mono text-xs">{e.machineId || '—'}</TableCell>
            <TableCell className="font-mono text-xs">{e.coreVersion || '—'}</TableCell>
            <TableCell>{kindLabel(e.kind, t)}</TableCell>
            <TableCell className="font-mono text-xs">{targetOf(e)}</TableCell>
            <TableCell className="tabular-nums">{e.ip || '—'}</TableCell>
            <TableCell className="tabular-nums">{e.bytes}</TableCell>
            <TableCell className="tabular-nums">{e.durationMs}ms</TableCell>
            <TableCell className="tabular-nums">{e.status}</TableCell>
            <TableCell><ResultBadge status={e.status} /></TableCell>
            <TableCell>{e.errCode ? <Badge variant="outline" className="font-mono text-xs">{e.errCode}</Badge> : <span className="text-muted-foreground">—</span>}</TableCell>
            <TableCell><Button type="button" variant="outline" size="xs" onClick={() => onDetail(e.id)}>{t('clientDistMonitor.viewDetail')}</Button></TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}

function EventDetailDialog({ id, open, onOpenChange }: { id: number | null; open: boolean; onOpenChange: (open: boolean) => void }) {
  const { t } = useTranslation()
  const { data, isLoading, isError } = useClientDistEventDetail(id, open)
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('clientDistMonitor.detailTitle')}</DialogTitle>
          <DialogDescription>{t('clientDistMonitor.detailHint')}</DialogDescription>
        </DialogHeader>
        {isError ? <p className="text-sm text-muted-foreground">{t('clientDistMonitor.detailError')}</p> : null}
        {isLoading ? <p className="text-sm text-muted-foreground">{t('common.loading')}</p> : data ? <EventDetailBody detail={data} /> : null}
      </DialogContent>
    </Dialog>
  )
}

function EventDetailBody({ detail }: { detail: ClientDistEventDetail }) {
  const { t } = useTranslation()
  return (
    <div className="space-y-4 text-sm">
      <div className="grid grid-cols-2 gap-2 rounded-lg border p-3 text-xs">
        <DetailLine label={t('clientDistMonitor.colMethod')} value={detail.method || '—'} />
        <DetailLine label={t('clientDistMonitor.colPath')} value={detail.path || '—'} mono />
        <DetailLine label={t('clientDistMonitor.colStatus')} value={String(detail.status)} />
        <DetailLine label={t('clientDistMonitor.colEtag')} value={detail.etag || '—'} mono />
      </div>
      <HeaderList title={t('clientDistMonitor.requestHeaders')} rows={detail.requestHeaders} />
      <BodyBlock title={t('clientDistMonitor.requestBody', '请求体')} body={detail.requestBody} />
      <HeaderList title={t('clientDistMonitor.responseHeaders')} rows={detail.responseHeaders} />
      <BodyBlock title={t('clientDistMonitor.responseBody', '响应体')} body={detail.responseBody} />
    </div>
  )
}

function DetailLine({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <div className="text-muted-foreground">{label}</div>
      <div className={mono ? 'truncate font-mono' : 'truncate'}>{value}</div>
    </div>
  )
}

function BodyBlock({ title, body }: { title: string; body?: string }) {
  return (
    <div>
      <h4 className="mb-2 text-xs font-semibold">{title}</h4>
      {body ? (
        <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all rounded-lg border p-3 text-xs font-mono">{body}</pre>
      ) : (
        <p className="rounded-lg border p-3 text-xs text-muted-foreground">—</p>
      )}
    </div>
  )
}

function HeaderList({ title, rows }: { title: string; rows: Record<string, string> }) {
  const entries = Object.entries(rows ?? {})
  return (
    <div>
      <h4 className="mb-2 text-xs font-semibold">{title}</h4>
      {entries.length === 0 ? (
        <p className="rounded-lg border p-3 text-xs text-muted-foreground">—</p>
      ) : (
        <div className="overflow-hidden rounded-lg border">
          {entries.map(([k, v]) => (
            <div key={k} className="grid grid-cols-[11rem_1fr] border-b px-3 py-2 text-xs last:border-b-0">
              <span className="font-mono text-muted-foreground">{k}</span>
              <span className="min-w-0 truncate font-mono">{v}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function ClientsTab({ overview, isError, onLink }: { overview?: ClientRuntimeOverview; isError: boolean; onLink: (link: RuntimeLink) => void }) {
  const { t } = useTranslation()
  const updateSeries = runtimeUpdateSeries(overview?.updateResultSeries ?? [], t)
  const runtimeBuckets = distBuckets(overview?.runtimeVersionDist ?? [], (v) => v.count, (v) => `v${v.version}`)
  const coreBuckets = distBuckets(overview?.coreVersionDist ?? [], (v) => v.count, (v) => v.value)
  const platformBuckets = distBuckets(overview?.platformDist ?? [], (v) => v.count, (v) => platformLabel(v.value))
  const lagBuckets = distBuckets(overview?.lagDist ?? [], (v) => v.count, (v) => lagLabel(v.lag, t))

  if (isError) return <ErrorPanel title={t('clientDistMonitor.tabClients')} message={t('clientDistMonitor.clientsError')} />
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <StatCard icon={<Clock className="size-3.5" />} label={t('clientDistMonitor.recentStarted')} value={String(overview?.summary.recentStarted ?? 0)} sub={t('clientDistMonitor.last5m')} />
        <StatCard icon={<Clock className="size-3.5" />} label={t('clientDistMonitor.todayStarted')} value={String(overview?.summary.todayStarted ?? 0)} />
        <StatCard icon={<RefreshCw className="size-3.5" />} tone="success" label={t(KPI_I18N.updateSuccessRate, t('clientDistMonitor.updateSuccessRate'))} value={fmtRate(overview?.summary.updateSuccessRate ?? 0)} />
        <StatCard icon={<AlertTriangle className="size-3.5" />} tone="warning" label={t('clientDistMonitor.updateFailureRate')} value={fmtRate(overview?.summary.updateFailureRate ?? 0)} />
      </div>
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <TrendCard title={t('clientDistMonitor.updateResultTrend')} series={updateSeries} valueFormatter={(v) => String(Math.round(v))} empty={t('clientDistMonitor.emptyClients')} />
        <RuntimeTable items={overview?.items ?? []} onLink={onLink} />
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <LinkableDistPanel title={t('clientDistMonitor.runtimeVersionDist')} buckets={runtimeBuckets} empty={t('clientDistMonitor.emptyClients')} onPick={(key) => onLink({ runtimeVersion: Number(key.replace(/^v/, '')) })} />
        <LinkableDistPanel title={t('clientDistMonitor.coreVersionDist')} buckets={coreBuckets} empty={t('clientDistMonitor.emptyClients')} onPick={(key) => onLink({ coreVersion: key })} />
        <LinkableDistPanel title={t('clientDistMonitor.platformDist')} buckets={platformBuckets} empty={t('clientDistMonitor.emptyClients')} onPick={(key) => onLink({ platform: reversePlatformLabel(key) })} />
        <LinkableDistPanel title={t('clientDistMonitor.lagDist')} buckets={lagBuckets} empty={t('clientDistMonitor.emptyClients')} onPick={(key) => onLink({ lag: parseLagLabel(key) })} />
      </div>
    </div>
  )
}

function RuntimeTable({ items, onLink }: { items: ClientRuntimeState[]; onLink: (link: RuntimeLink) => void }) {
  const { t } = useTranslation()
  return (
    <Panel title={t('clientDistMonitor.runtimeClients')}>
      {items.length === 0 ? (
        <p className="py-6 text-center text-sm text-muted-foreground">{t('clientDistMonitor.emptyClients')}</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('clientDistMonitor.colMachine')}</TableHead>
              <TableHead>{t('clientDistMonitor.colChannel')}</TableHead>
              <TableHead>{t('clientDistMonitor.colRuntimeVersion')}</TableHead>
              <TableHead>{t('clientDistMonitor.colPlatform')}</TableHead>
              <TableHead>{t('clientDistMonitor.colLastHeartbeat')}</TableHead>
              <TableHead>{t('clientDistMonitor.colLogs')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((it) => (
              <TableRow key={`${it.channelId}-${it.machineId}`}>
                <TableCell className="font-mono text-xs">{it.machineId}</TableCell>
                <TableCell>{it.channelId || '—'}</TableCell>
                <TableCell>v{it.localVersion}</TableCell>
                <TableCell>{platformLabel(it.platform)}</TableCell>
                <TableCell className="tabular-nums text-muted-foreground">{fmtTime(it.lastHeartbeatAt)}</TableCell>
                <TableCell><Button type="button" size="xs" variant="outline" onClick={() => onLink({ machineId: it.machineId })}>{t('clientDistMonitor.viewLogs')}</Button></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </Panel>
  )
}

function ErrorPanel({ title, message }: { title: string; message: string }) {
  return (
    <Panel title={title}>
      <p className="py-10 text-center text-sm text-muted-foreground">{message}</p>
    </Panel>
  )
}

export default function ClientDistMonitoringPage() {
  const { t } = useTranslation()
  const [searchParams, setSearchParams] = useSearchParams()
  const query = readClientDistQuery(searchParams)
  const [range, setRange] = useState<MetricRange>('7d')
  const [tab, setTab] = useTabParam<PageTab>('tab', 'statistics', ['statistics', 'monitor', 'logs', 'clients'])
  const [runtimeLinkExtra, setRuntimeLinkExtra] = useState<RuntimeLink>({})
  const channel = query.channelId ?? ALL_CHANNELS
  const runtimeLink: RuntimeLink = {
    ...runtimeLinkExtra,
    machineId: query.machineId,
    version: query.version ? Number(query.version) : undefined,
    errCode: query.errCode,
    ip: query.ip,
  }

  const isPlatformAdmin = useAuthStore((s) => s.role) === ROLE_PLATFORM_ADMIN
  const { data: channels } = useClientChannels()
  const channelId = query.channelId
  const statsQuery = useClientStats(channelId, toStatsDays(range), { enabled: isPlatformAdmin })
  const realtimeQuery = useClientDistRealtime({ channelId, enabled: isPlatformAdmin })
  const runtimeQuery = useClientRuntimeOverview({ channelId, range: toApiRange(range), enabled: isPlatformAdmin })
  const errorSummaryQuery = useClientDistErrorSummary({
    channelId,
    range: toApiRange(range),
    enabled: isPlatformAdmin && (tab === 'monitor' || tab === 'statistics'),
  })

  const openLogsWithLink = (link: RuntimeLink) => {
    setRuntimeLinkExtra((prev) => ({ ...prev, ...link }))
    setSearchParams(updateClientDistQuery(searchParams, {
      machineId: link.machineId,
      version: link.version,
      errCode: link.errCode,
      ip: link.ip,
      tab: 'logs',
    }), { replace: true })
  }

  const clearRuntimeLink = () => {
    setRuntimeLinkExtra({})
    setSearchParams(updateClientDistQuery(searchParams, {
      ip: null,
      machineId: null,
      errCode: null,
      version: null,
    }), { replace: true })
  }

  const channelPicker = (
    <Select
      value={channel}
      onValueChange={(value) => setSearchParams(updateClientDistQuery(searchParams, {
        channelId: value === ALL_CHANNELS ? null : value,
      }), { replace: true })}
    >
      <SelectTrigger size="sm" className="w-44"><SelectValue /></SelectTrigger>
      <SelectContent>
        <SelectItem value={ALL_CHANNELS}>{t('clientDistMonitor.allChannels')}</SelectItem>
        {(channels ?? []).map((c) => <SelectItem key={c.channelId} value={c.channelId}>{c.name}</SelectItem>)}
      </SelectContent>
    </Select>
  )

  return (
    <div data-page="client-dist-monitor" className="jm-page-stack space-y-4">
      <div className="jm-page-header flex-wrap">
        <div>
          <h1 className="jm-page-title">{t('clientDistMonitor.title')}</h1>
          <p className="jm-page-subtitle">{t('clientDistMonitor.subtitle')}</p>
        </div>
        <div className="flex items-center gap-2">
          {isPlatformAdmin && channelPicker}
          {isPlatformAdmin && <RangePicker value={range} onChange={setRange} />}
          {isPlatformAdmin && <ClientDistExportButton kind="stats-summary" filters={{ channelId, range: toApiRange(range) }} />}
        </div>
      </div>

      {!isPlatformAdmin ? (
        <Panel title={t('clientDistMonitor.title')}>
          <p className="py-10 text-center text-sm text-muted-foreground">{t('clientDistMonitor.adminOnly')}</p>
        </Panel>
      ) : (
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList variant="line">
            <TabsTrigger value="statistics"><Search className="size-3.5" />{t('clientDistMonitor.tabStatistics')}</TabsTrigger>
            <TabsTrigger value="monitor"><Activity className="size-3.5" />{t('clientDistMonitor.tabMonitor')}</TabsTrigger>
            <TabsTrigger value="logs"><Server className="size-3.5" />{t('clientDistMonitor.tabLogs')}</TabsTrigger>
            <TabsTrigger value="clients"><Users className="size-3.5" />{t('clientDistMonitor.tabClients')}</TabsTrigger>
          </TabsList>
          <TabsContent value="statistics" className="space-y-4">
            <StatisticsTab
              stats={statsQuery.data}
              isError={statsQuery.isError}
              isLoading={statsQuery.isLoading}
              onLink={openLogsWithLink}
            />
          </TabsContent>
          <TabsContent value="monitor" className="space-y-4">
            <MonitorTab
              realtime={realtimeQuery.data}
              errors={errorSummaryQuery.data}
              isError={realtimeQuery.isError || errorSummaryQuery.isError}
              onLink={openLogsWithLink}
            />
          </TabsContent>
          <TabsContent value="logs" className="space-y-4">
            <LogsTab channelId={channelId} range={toApiRange(range)} enabled={isPlatformAdmin} link={runtimeLink} onClearLink={clearRuntimeLink} />
          </TabsContent>
          <TabsContent value="clients" className="space-y-4">
            <ClientsTab overview={runtimeQuery.data} isError={runtimeQuery.isError} onLink={openLogsWithLink} />
          </TabsContent>
        </Tabs>
      )}
    </div>
  )
}

function kindRequests(stats: ClientDistStats | undefined, kind: 'manifest' | 'artifact'): number {
  if (kind === 'manifest') return (stats?.versions ?? []).reduce((sum, v) => sum + v.requests, 0)
  return Math.max(0, (stats?.downloads ?? []).reduce((sum, d) => sum + d.requests, 0) - kindRequests(stats, 'manifest'))
}

function totalBytes(stats: ClientDistStats | undefined): number {
  return (stats?.downloads ?? []).reduce((sum, p) => sum + p.bytes, 0)
}

function resultLabel(result: string, t: (key: string) => string): string {
  if (result === 'success') return t('clientDistMonitor.resultSuccess')
  if (result === 'failure') return t('clientDistMonitor.resultFailure')
  return result || '—'
}

function lagLabel(lag: number, t: (key: string, opts?: Record<string, number>) => string): string {
  return lag === 0 ? t('clientDistMonitor.lagLatest') : t('clientDistMonitor.lagBehind', { n: lag })
}

function parseLagLabel(label: string): number {
  const n = Number(label.replace(/\D+/g, ''))
  return Number.isFinite(n) ? n : 0
}

function reversePlatformLabel(label: string): string {
  const map: Record<string, string> = { Windows: 'windows', macOS: 'macos', Linux: 'linux' }
  return map[label] ?? label
}

function runtimeUpdateSeries(points: RuntimeUpdateSeriesPoint[], t: (key: string) => string): ChartSeries[] {
  return [
    { key: 'success', name: t('clientDistMonitor.updateSuccess'), points: points.map((p) => ({ ts: p.ts, value: p.success })) },
    { key: 'failStatic', name: t('clientDistMonitor.updateFailStatic'), points: points.map((p) => ({ ts: p.ts, value: p.failStatic })) },
    { key: 'rolledBack', name: t('clientDistMonitor.updateRolledBack'), points: points.map((p) => ({ ts: p.ts, value: p.rolledBack })) },
    { key: 'error', name: t('clientDistMonitor.updateError'), points: points.map((p) => ({ ts: p.ts, value: p.error })) },
  ]
}
