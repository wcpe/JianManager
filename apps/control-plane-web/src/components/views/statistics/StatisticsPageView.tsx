/**
 * @file StatisticsPageView：观测·统计页的受控视图，五个查询的结果、平台管理员权限与两个区块插槽由应用容器注入。
 * @input lib/node-summary（节点在线/维护汇总）、lib/platform-stats（分桶/占比与探针可达汇总）、
 *        lib/node-types（NodeInfo）、lib/player（OnlinePlayersResult）、
 *        lib/client-dist-stats-contracts（ClientDistObservability）、
 *        Panel/StatCard/MiniBar 原语、layout（PageShell/PageHeader）、charts/RangePicker、翻译上下文
 * @output StatisticsPageView、StatisticsPageViewProps、StatisticsOverviewTotals、StatisticsInstanceCounts
 * @sync apps/control-plane-web/src/pages/StatisticsPage.tsx、apps/control-plane-web/src/pages/StatisticsPage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-220 平台级聚合统计 + FR-463 可用性区块 + FR-469 玩家趋势 +
 *        FR-217/FR-428 客户端分发观测 + FR-496 阶段 6 布局原语）
 */
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Server, Boxes, Users, Download, AlertTriangle } from 'lucide-react'
import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Panel } from '@jianmanager/ui/components/panel'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { RangePicker, type MetricRange } from '@jianmanager/ui'
import type { ClientDistObservability } from '@/lib/client-dist/client-dist-stats-contracts'
import { summarizeNodes } from '@/lib/nodes/node-summary'
import type { NodeInfo } from '@/lib/nodes/node-types'
import type { OnlinePlayersResult } from '@/lib/players/player'
import { bucketsFromCounts, summarizeProbeReachability, tallyBy } from '@/lib/overview/platform-stats'
import type { DistBucket } from '@/lib/overview/platform-stats'

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
 * `/metrics/overview` 的 totals 中本页用到的字段（结构性最小集）。
 * 刻意不照搬应用侧 `@/api/metrics` 的类型：容器直接传该响应的 `totals` 也结构兼容，
 * 而包内无需认识整份观测响应（同 `ScheduleLogRow`、`TaskNodeOption` 的取舍）。
 */
export interface StatisticsOverviewTotals {
  /** 节点总数（含离线）。 */
  nodeCount: number
  /** 在线节点数。 */
  onlineNodeCount: number
  /** 运行中实例数。 */
  runningInstances: number
  /** 在线玩家数。 */
  onlinePlayers: number
}

/**
 * `/instances/aggregate` 中本页用到的计数（后端聚合，FR-220）。
 * 有了它，实例维度的分布不必再拉全量实例列表（千级约 1MB/轮且带兜底轮询）。
 */
export interface StatisticsInstanceCounts {
  /** 实例总数。 */
  total: number
  /** 按状态的计数（RUNNING / STOPPED / CRASHED…）。 */
  byStatus: Record<string, number>
  /** 按角色（backend / proxy / universal…）的计数。 */
  byRole: Record<string, number>
  /** 按进程类型（direct / daemon / docker / rcon）的计数。 */
  byProcessType: Record<string, number>
}

/**
 * 受控边界（ADR-097 a 范式）：**不取数、不发请求、不弹 toast**。
 * - 五个查询的结果与错误态经 props 注入（容器调 `useMetricOverview` / `useNodes` / `useInstanceAggregate` /
 *   `useOnlinePlayers` / `useClientDistObservability`）；分桶、占比与探针可达汇总都是纯展示派生，留本组件；
 * - **归容器**的受控状态：统计窗口 `range`——它是上述查询的查询键，一变就触发重新取数，
 *   且分发观测端点只认「小时档归 24h / 180d 由 90d 升」的区间枚举（换算属取数口径，归容器）；
 *   平台管理员判定（`useAuthStore`）亦然：非管理员必须**不发起**分发观测请求，故门禁不能留在视图；
 * - **留本组件**的纯 UI 状态：无（本页没有展开项、草稿之类）；
 * - 两个区块（可用性 SLO、玩家在线趋势）各自带应用侧接线层与取数 hook，故以插槽注入：
 *   包内不取数，也不认识它们的查询键；插槽缺省不渲染该区块。
 */
export interface StatisticsPageViewProps {
  /** 统计窗口（受控：全部查询的查询键，变更即重新取数）。 */
  range: MetricRange
  /** 统计窗口变更上报。 */
  onRangeChange: (range: MetricRange) => void
  /** `/metrics/overview` 的 totals；缺省时节点/实例/玩家 KPI 回退到本地聚合口径。 */
  overviewTotals?: StatisticsOverviewTotals
  /** `/nodes` 全量节点（视图内做在线/维护汇总与 OS/arch 分桶）。 */
  nodes?: NodeInfo[]
  /** `/instances/aggregate` 结果（实例总数与三组分布计数）。 */
  instanceCounts?: StatisticsInstanceCounts
  /** `/players` 结果（在线玩家数 + 各后端探针可达明细）。 */
  players?: OnlinePlayersResult
  /** 是否平台管理员：决定分发观测区块展示汇总还是权限提示（包内不持鉴权状态）。 */
  isPlatformAdmin: boolean
  /** 分发观测数据（容器按权限取数后注入；非管理员请勿取数，此处留空即降级为权限提示）。 */
  distribution?: ClientDistObservability
  /** 分发观测取数失败：管理员态下局部降级为错误文案，其余维度照常。 */
  distributionError?: boolean
  /** 可用性区块插槽（FR-463）：取数在应用侧接线层，插槽缺省不渲染该区块。 */
  renderSloSection?: () => ReactNode
  /** 玩家在线趋势插槽（FR-469）：同上。 */
  renderPlayerTrend?: () => ReactNode
}

/**
 * 观测·统计页（FR-220）：平台级聚合统计——节点 / 实例 / 玩家 / 客户端分发多维计数 + 构成分布。
 * 全复用既有端点（/metrics/overview、/nodes、/instances、/players、/client-dist/observability），
 * 状态构成（含 CRASHED）由 /instances 列表前端聚合得出，无后端改动。
 * 与 `/`（OverviewPage 实时资源仪表盘）互补：本页偏整体规模与构成，非此刻资源水位。
 */
export function StatisticsPageView({
  range,
  onRangeChange,
  overviewTotals: totals,
  nodes,
  instanceCounts,
  players,
  isPlatformAdmin,
  distribution,
  distributionError = false,
  renderSloSection,
  renderPlayerTrend,
}: StatisticsPageViewProps) {
  const { t } = useTranslation()

  const nodeSum = summarizeNodes(nodes ?? [])
  // 实例计数与分布均来自后端聚合（见上），不再本地遍历列表。
  const instSum = {
    total: instanceCounts?.total ?? 0,
    running: instanceCounts?.byStatus.RUNNING ?? 0,
    stopped: instanceCounts?.byStatus.STOPPED ?? 0,
    crashed: instanceCounts?.byStatus.CRASHED ?? 0,
  }
  const probe = summarizeProbeReachability(players?.backends ?? [])

  const instByRole = bucketsFromCounts(instanceCounts?.byRole)
  const instByProcess = bucketsFromCounts(instanceCounts?.byProcessType)
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

  const dist = distribution
  const distVersions = tallyBy(dist?.versionDist ?? [], (v) => `v${v.version}`)
  const distPlatforms = tallyBy(dist?.platformDist ?? [], (p) => p.os ?? '—')

  return (
    // 阶段 6 页面迁移：外壳与页头改用布局层原语（PageShell / PageHeader）。
    // 本页原为裸 `space-y-4` + 手写页头（其 h1 类名与 PageHeader 的完全一致，映射干净）；
    // 原先没有 data-page，迁移时补上——e2e 的就绪信号依赖它。
    <PageShell data-page="statistics">
      <PageHeader
        title={t('statistics.title')}
        actions={<RangePicker value={range} onChange={onRangeChange} />}
      />

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

      {/* 可用性（FR-463）：平台级窗口可用率/故障次数/MTTR/MTBF/误差预算。取数在应用侧接线层，经插槽注入。 */}
      {renderSloSection?.()}

      {/* 玩家在线趋势与时段分布（FR-469）。同上，取数在应用侧接线层。 */}
      {renderPlayerTrend?.()}

      {/* 客户端分发概览（平台管理员） */}
      {isPlatformAdmin ? (
        distributionError ? (
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
    </PageShell>
  )
}

export default StatisticsPageView
