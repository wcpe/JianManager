import { useTranslation } from 'react-i18next'
import { AlertTriangle, Clock, RefreshCw } from 'lucide-react'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { Button } from '@jianmanager/ui/components/button'
import { Panel } from '@jianmanager/ui/components/panel'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import type { ChartSeries } from '@jianmanager/ui'
import type { ClientRuntimeOverview, ClientRuntimeState } from '@/api/clientRuntimeStates'
import { ObsOverviewSection } from './ObsOverviewSection'
import type { ObsWindow } from './obs-window'
import {
  LinkableDistPanel,
  TrendCard,
  ErrorPanel,
  distBuckets,
  fmtTime,
  fmtRate,
  lagLabel,
  parseLagLabel,
  platformLabel,
  reversePlatformLabel,
  runtimeUpdateSeries,
  type RuntimeLink,
} from './ops-shared'
import { KPI_I18N } from '@/lib/client-dist-kpi'

/**
 * 页面 B · 机器 / 客户端 Tab（FR-430，迁自旧监控页 `ClientsTab` + `ObsOverviewSection`）。
 * 更新侧总览（洞察卡 + 热力图 + 机器排行）+ 运行态 KPI/分布/明细；
 * 维护 FR-265「统计/监控=请求侧」边界——本 Tab 只走更新侧与运行态数据。
 */
export default function OpsClientsTab({
  channelId,
  window,
  overview,
  isError,
  onLink,
}: {
  channelId?: string
  window: ObsWindow
  overview?: ClientRuntimeOverview
  isError: boolean
  onLink: (link: RuntimeLink) => void
}) {
  const { t } = useTranslation()
  const updateSeries: ChartSeries[] = runtimeUpdateSeries(overview?.updateResultSeries ?? [], t)
  const runtimeBuckets = distBuckets(overview?.runtimeVersionDist ?? [], (v) => v.count, (v) => `v${v.version}`)
  const coreBuckets = distBuckets(overview?.coreVersionDist ?? [], (v) => v.count, (v) => v.value)
  const platformBuckets = distBuckets(overview?.platformDist ?? [], (v) => v.count, (v) => platformLabel(v.value))
  const lagBuckets = distBuckets(overview?.lagDist ?? [], (v) => v.count, (v) => lagLabel(v.lag, t))

  return (
    <div className="space-y-4">
      {/* 更新侧总览（洞察卡 + 热力图 + 机器排行），维持「本 Tab=更新侧」边界。 */}
      <ObsOverviewSection channelId={channelId} window={window} />
      {isError ? (
        <ErrorPanel title={t('clientDistOps.tabClients')} message={t('clientDistOps.clientsError')} />
      ) : (
        <>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <StatCard icon={<Clock className="size-3.5" />} label={t('clientDistOps.recentStarted')} value={String(overview?.summary.recentStarted ?? 0)} sub={t('clientDistOps.last5m')} />
            <StatCard icon={<Clock className="size-3.5" />} label={t('clientDistOps.todayStarted')} value={String(overview?.summary.todayStarted ?? 0)} />
            <StatCard icon={<RefreshCw className="size-3.5" />} tone="success" label={t(KPI_I18N.updateSuccessRate, t('clientDistOps.updateSuccessRate'))} value={fmtRate(overview?.summary.updateSuccessRate ?? 0)} />
            <StatCard icon={<AlertTriangle className="size-3.5" />} tone="warning" label={t('clientDistOps.updateFailureRate')} value={fmtRate(overview?.summary.updateFailureRate ?? 0)} />
          </div>
          <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
            <TrendCard title={t('clientDistOps.updateResultTrend')} series={updateSeries} valueFormatter={(v) => String(Math.round(v))} empty={t('clientDistOps.emptyClients')} />
            <RuntimeTable items={overview?.items ?? []} onLink={onLink} />
          </div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <LinkableDistPanel title={t('clientDistOps.runtimeVersionDist')} buckets={runtimeBuckets} empty={t('clientDistOps.emptyClients')} onPick={(key) => onLink({ runtimeVersion: Number(key.replace(/^v/, '')) })} />
            <LinkableDistPanel title={t('clientDistOps.coreVersionDist')} buckets={coreBuckets} empty={t('clientDistOps.emptyClients')} onPick={(coreVersion) => onLink({ coreVersion })} />
            <LinkableDistPanel title={t('clientDistOps.platformDist')} buckets={platformBuckets} empty={t('clientDistOps.emptyClients')} onPick={(key) => onLink({ platform: reversePlatformLabel(key) })} />
            <LinkableDistPanel title={t('clientDistOps.lagDist')} buckets={lagBuckets} empty={t('clientDistOps.emptyClients')} onPick={(key) => onLink({ lag: parseLagLabel(key) })} />
          </div>
        </>
      )}
    </div>
  )
}

function RuntimeTable({ items, onLink }: { items: ClientRuntimeState[]; onLink: (link: RuntimeLink) => void }) {
  const { t } = useTranslation()
  return (
    <Panel title={t('clientDistOps.runtimeClients')}>
      {items.length === 0 ? (
        <p className="py-6 text-center text-sm text-muted-foreground">{t('clientDistOps.emptyClients')}</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('clientDistOps.colMachine')}</TableHead>
              <TableHead>{t('clientDistOps.colChannel')}</TableHead>
              <TableHead>{t('clientDistOps.colRuntimeVersion')}</TableHead>
              <TableHead>{t('clientDistOps.colPlatform')}</TableHead>
              <TableHead>{t('clientDistOps.colLastHeartbeat')}</TableHead>
              <TableHead>{t('clientDistOps.colLogs')}</TableHead>
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
                <TableCell><Button type="button" size="xs" variant="outline" onClick={() => onLink({ machineId: it.machineId })}>{t('clientDistOps.viewLogs')}</Button></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </Panel>
  )
}
