import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Server, Boxes, Users, Download, AlertTriangle } from 'lucide-react'
import { useNodes } from '@/api/nodes'
import { useInstances } from '@/api/instances'
import { useOnlinePlayers } from '@/api/players'
import { useMetricOverview } from '@/api/metrics'
import { useClientDistObservability } from '@/api/clientStats'
import { useAuthStore } from '@/stores/auth'
import { Panel } from '@/components/ui/panel'
import { StatCard } from '@/components/ui/stat-card'
import { MiniBar } from '@/components/ui/mini-bar'
import { RangePicker, type MetricRange } from '@/components/charts/RangePicker'
import { summarizeInstances } from '@/lib/instance-summary'
import { summarizeNodes } from '@/lib/node-summary'
import { tallyBy, summarizeProbeReachability, type DistBucket } from '@/lib/platform-stats'

const ROLE_PLATFORM_ADMIN = 10

/** 客户端分发观测端点的区间枚举（24h/7d/30d/90d/180d，ADR-049）。 */
type ObsRange = '24h' | '7d' | '30d' | '90d' | '180d'

/** 把页面统一的 MetricRange 映射到分发观测端点支持的区间枚举（小时档归 24h，180d 由 90d 升）。 */
function toObsRange(r: MetricRange): ObsRange {
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
    default:
      return '7d'
  }
}

/** 字节 → 紧凑可读（G/M/K）。 */
function fmtBytes(b: number): string {
  if (!Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1e9) return `${(b / 1024 / 1024 / 1024).toFixed(1)}G`
  if (b >= 1e6) return `${(b / 1024 / 1024).toFixed(0)}M`
  if (b >= 1e3) return `${(b / 1024).toFixed(0)}K`
  return String(b)
}

/** 率（0~1 小数）→ 百分比字符串。 */
function fmtRate(r: number): string {
  return `${((Number.isFinite(r) ? r : 0) * 100).toFixed(1)}%`
}

/** 构成分布面板：标签 + 计数 + 占比条（信息色，占比高不视作异常）。空集显占位。 */
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

/**
 * 观测·统计页（FR-220）：平台级聚合统计——节点 / 实例 / 玩家 / 客户端分发多维计数 + 构成分布。
 * 全复用既有端点（/metrics/overview、/nodes、/instances、/players、/client-dist/observability），
 * 状态构成（含 CRASHED）由 /instances 列表前端聚合得出，无后端改动。
 * 与 `/`（OverviewPage 实时资源仪表盘）互补：本页偏整体规模与构成，非此刻资源水位。
 */
export default function StatisticsPage() {
  const { t } = useTranslation()
  const [range, setRange] = useState<MetricRange>('7d')

  const isPlatformAdmin = useAuthStore((s) => s.role) === ROLE_PLATFORM_ADMIN

  const { data: overview } = useMetricOverview(range)
  const { data: nodes } = useNodes()
  const { data: instances } = useInstances()
  const { data: players } = useOnlinePlayers()
  // 分发观测仅平台管理员可查；非管理员不发起请求（enabled=false），UI 整区降级。
  const distQuery = useClientDistObservability({ range: toObsRange(range), enabled: isPlatformAdmin })

  const totals = overview?.totals
  const nodeSum = summarizeNodes(nodes ?? [])
  const instSum = summarizeInstances(instances ?? [])
  const probe = summarizeProbeReachability(players?.backends ?? [])

  // 构成分布（前端从列表聚合）
  const instByRole = tallyBy(instances ?? [], (i) => i.role)
  const instByProcess = tallyBy(instances ?? [], (i) => i.processType)
  const nodeByOs = tallyBy(nodes ?? [], (n) => n.os)
  const nodeByArch = tallyBy(nodes ?? [], (n) => n.arch)

  // 实例状态分布（含 CRASHED，danger 提示在 KPI 卡上单列）
  const instByStatus: DistBucket[] = (() => {
    const total = instSum.total
    const mk = (key: string, count: number): DistBucket => ({ key, count, pct: total > 0 ? count / total : 0 })
    return [
      mk(t('statistics.statusRunning'), instSum.running),
      mk(t('statistics.statusStopped'), instSum.stopped),
      mk(t('statistics.statusCrashed'), instSum.crashed),
    ].filter((b) => b.count > 0)
  })()

  const dist = distQuery.data
  const distVersions = tallyBy(dist?.versionDist ?? [], (v) => `v${v.version}`)
  const distPlatforms = tallyBy(dist?.platformDist ?? [], (p) => p.os ?? '—')

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-bold">{t('statistics.title')}</h1>
        <RangePicker value={range} onChange={setRange} />
      </div>

      {/* KPI 行：节点 / 实例 / 玩家 + 崩溃单列 */}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
        <StatCard
          icon={<Server className="size-3.5" />}
          label={t('statistics.nodes')}
          value={`${totals?.onlineNodeCount ?? nodeSum.online}/${totals?.nodeCount ?? nodeSum.total}`}
          sub={t('dashboard.online')}
        />
        <StatCard
          icon={<Boxes className="size-3.5" />}
          label={t('statistics.instances')}
          value={`${totals?.runningInstances ?? instSum.running}/${instSum.total}`}
          sub={t('statistics.running')}
        />
        <StatCard
          icon={<AlertTriangle className="size-3.5" />}
          tone={instSum.crashed > 0 ? 'danger' : 'neutral'}
          label={t('statistics.statusCrashed')}
          value={String(instSum.crashed)}
          sub={t('nav.instances')}
        />
        <StatCard
          icon={<Users className="size-3.5" />}
          label={t('statistics.onlinePlayers')}
          value={String(totals?.onlinePlayers ?? players?.players.length ?? 0)}
          sub={t('nav.players')}
        />
        <StatCard
          icon={<Server className="size-3.5" />}
          tone="info"
          label={t('statistics.probeReach')}
          value={fmtRate(probe.pct)}
          sub={`${probe.available}/${probe.total}`}
        />
        <StatCard
          icon={<Server className="size-3.5" />}
          tone="warning"
          label={t('statistics.maintenance')}
          value={String(nodeSum.maintenance)}
          sub={t('nav.nodes')}
        />
      </div>

      {/* 构成分布区 */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <DistPanel title={t('statistics.distByStatus')} buckets={instByStatus} empty={t('instances.empty')} />
        <DistPanel title={t('statistics.distByRole')} buckets={instByRole} empty={t('instances.empty')} />
        <DistPanel title={t('statistics.distByProcess')} buckets={instByProcess} empty={t('instances.empty')} />
        <DistPanel title={t('statistics.distByOs')} buckets={nodeByOs} empty={t('nodes.empty')} />
        <DistPanel title={t('statistics.distByArch')} buckets={nodeByArch} empty={t('nodes.empty')} />
      </div>

      {/* 客户端分发概览（平台管理员） */}
      {isPlatformAdmin ? (
        distQuery.isError ? (
          <Panel title={t('statistics.distribution')}>
            <p className="py-6 text-center text-sm text-muted-foreground">{t('statistics.distError')}</p>
          </Panel>
        ) : (
          <>
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
              <StatCard
                icon={<Download className="size-3.5" />}
                label={t('statistics.manifestPulls')}
                value={String(dist?.summary.manifestPulls ?? 0)}
                sub={t('statistics.distribution')}
              />
              <StatCard
                icon={<Download className="size-3.5" />}
                label={t('statistics.artifactPulls')}
                value={String(dist?.summary.artifactPulls ?? 0)}
              />
              <StatCard
                icon={<Download className="size-3.5" />}
                label={t('statistics.downloadBytes')}
                value={fmtBytes(dist?.summary.downloadBytes ?? 0)}
              />
              <StatCard
                icon={<Users className="size-3.5" />}
                label={t('statistics.activeMachines')}
                value={String(dist?.summary.activeMachines ?? 0)}
                sub={dist?.summary.activeMachinesExact === false ? t('statistics.approx') : undefined}
              />
              <StatCard
                icon={<Download className="size-3.5" />}
                tone="success"
                label={t('statistics.successRate')}
                value={fmtRate(dist?.summary.successRate ?? 0)}
              />
              <StatCard
                icon={<AlertTriangle className="size-3.5" />}
                tone="warning"
                label={t('statistics.rollbackRate')}
                value={fmtRate(dist?.summary.rollbackRate ?? 0)}
              />
            </div>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <DistPanel title={t('statistics.distByVersion')} buckets={distVersions} empty={t('statistics.distEmpty')} />
              <DistPanel title={t('statistics.distByPlatform')} buckets={distPlatforms} empty={t('statistics.distEmpty')} />
            </div>
          </>
        )
      ) : (
        <Panel title={t('statistics.distribution')}>
          <p className="py-6 text-center text-sm text-muted-foreground">{t('statistics.distAdminOnly')}</p>
        </Panel>
      )}
    </div>
  )
}
