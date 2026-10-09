/**
 * @file ClientStatsPanelView：客户端分发统计看板（KPI 卡 + 趋势 + 分布 + 热力图 + 机器排行）的受控视图。
 *       两个端点的取数、窗口 → 查询参数映射（天数 / range 枚举）、URL 深链解析与机器排行容器，
 *       均由应用容器负责。
 * @input lib/client-dist-kpi（KPI 口径与空态判定）、lib/client-dist-stats-contracts（stats / observability 契约）、
 *        lib/obs-window（ObsWindow）、views/client-dist/InsightCards（洞察卡）、
 *        views/client-dist/UpdateHeatmap（更新热力图）、views/client-dist/ObsTimeRangePicker（时间窗）、
 *        TimeSeriesChart 图表原语、翻译上下文
 * @output ClientStatsPanelView、ClientStatsPanelViewProps
 * @sync apps/control-plane-web/src/components/ClientStatsPanel.tsx、
 *       apps/control-plane-web/src/components/ClientStatsPanel.dom.test.tsx
 * @since FR-502（面板受控化迁包；原 FR-095 统计看板、FR-217/219 观测维度、FR-356 KPI 口径、
 *        FR-425 统一时间窗、FR-426/427/428 机器排行 / 热力图 / 洞察卡）
 */
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { TimeSeriesChart, type ChartSeries } from '@jianmanager/ui'
import {
  KPI_I18N,
  activeClientsHintKey,
  clientDistEmptyI18nKey,
  formatKpiRate,
  resolveActiveClients,
  resolveClientDistEmptyKind,
  resolveUpdateRates,
} from '@/lib/client-dist-kpi'
import type { ObsWindow } from '@jianmanager/ui/lib/obs-window'
import type { ClientDistStats, ClientDistObservability } from '@jianmanager/ui/lib/client-dist-stats-contracts'
import { InsightCards } from '@/components/views/client-dist/InsightCards'
import { UpdateHeatmap } from '@/components/views/client-dist/UpdateHeatmap'
import { ObsTimeRangePicker } from '@/components/views/client-dist/ObsTimeRangePicker'

/** 字节数转人类可读。 */
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}

/** 平台 os 标识 → 展示名（空串=未知）。 */
function platformLabel(os: string): string {
  if (!os) return '—'
  const map: Record<string, string> = { windows: 'Windows', macos: 'macOS', linux: 'Linux' }
  return map[os] ?? os
}

/**
 * 统计看板的注入契约（受控视图，ADR-097 a 范式）。
 *
 * 受控边界（**不取数、不读路由**）：
 * - 请求侧统计与更新侧观测经 props 注入（容器按窗口调 `/client-dist/stats` 与
 *   `/client-dist/observability`），加载态一并注入；
 * - 时间窗 `window` 是**容器持有的受控值**：它同时决定两个端点的查询参数（天数 / range 或 from-to），
 *   视图只经 `onWindowChange` 上报选择（含自定义起止）；URL 深链的解析（与写回，若有）留在容器；
 * - 机器更新排行是**应用侧取数容器**（`MachineListPanel`，按频道 + 观测窗口自行取数），经
 *   `machineRankingSlot` 插槽注入，包内不 import 应用侧模块；
 * - KPI 口径计算与空态判定留视图（纯展示口径，见 FR-356：更新侧率只信 observability，
 *   请求侧率只信 stats），它们只消费注入的数据。
 */
export interface ClientStatsPanelViewProps {
  /** 请求侧统计（`/client-dist/stats`）；缺省按 0 / 空展示。 */
  stats?: ClientDistStats
  /** 更新侧观测（`/client-dist/observability`，窗口由容器映射）；缺省按空展示。 */
  observability?: ClientDistObservability
  /** 请求侧取数中（空态判定用）。 */
  statsLoading: boolean
  /** 更新侧取数中（分布/空态判定用）。 */
  obsLoading: boolean
  /** 统一时间窗（容器持有：它驱动两个端点的取数参数）。 */
  window: ObsWindow
  /** 时间窗变更上报（容器改状态；如需可同步 URL 深链）。 */
  onWindowChange: (w: ObsWindow) => void
  /** 机器更新排行槽（应用侧取数容器）；缺省不渲染。 */
  machineRankingSlot?: ReactNode
}

/**
 * 客户端分发统计看板（FR-095 + FR-217/FR-219 + FR-356，见 ADR-023/ADR-049）。
 * KPI 口径统一走 `lib/client-dist-kpi`：更新率只信 observability；活跃回退 stats 时不谎报精确去重。
 * i18n（FR-016）+ 暗/亮色（FR-026，图表用主题 token）。
 */
export function ClientStatsPanelView({
  stats,
  observability,
  statsLoading,
  obsLoading,
  window,
  onWindowChange,
  machineRankingSlot,
}: ClientStatsPanelViewProps) {
  const { t } = useTranslation()

  const downloadSeries: ChartSeries[] = [
    {
      key: 'requests',
      name: t(KPI_I18N.downloadRequests, '下载请求数'),
      points: (stats?.downloads ?? []).map((d) => ({ ts: d.day, value: d.requests })),
    },
  ]
  const bytesSeries: ChartSeries[] = [
    {
      key: 'bytes',
      name: t('clientStats.downloadBytes', '下载字节'),
      points: (stats?.downloads ?? []).map((d) => ({ ts: d.day, value: d.bytes })),
    },
  ]
  const maxIpReq = Math.max(1, ...(stats?.topIps ?? []).map((r) => r.count))

  // FR-356：共享 KPI 解析，禁止 stats.successRate（HTTP）冒充更新成功率。
  const active = resolveActiveClients(observability?.summary, stats)
  const updateRates = resolveUpdateRates(observability?.summary)
  const activeHintKey = activeClientsHintKey(active.exactness, active.source)
  const versionDist = observability?.versionDist ?? []
  const platformDist = observability?.platformDist ?? []
  const lagDist = observability?.lagDist ?? []
  const requestCount = (stats?.downloads ?? []).reduce((sum, d) => sum + d.requests, 0)
  const emptyKind = resolveClientDistEmptyKind({
    loading: statsLoading || obsLoading,
    requestCount,
    updateTotal: observability?.summary.updateTotal ?? 0,
    active,
  })
  const emptyKey = clientDistEmptyI18nKey(emptyKind)

  return (
    <div className="space-y-6" data-kpi-scope="client-stats-panel">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <p className="text-sm text-muted-foreground max-w-2xl">
          {t('clientStats.subtitle', '分发统计（来自拉取追踪与遥测聚合）。机器码不可信，仅作统计维度。')}
        </p>
        <ObsTimeRangePicker value={window} onChange={onWindowChange} />
      </div>

      {/* FR-428 洞察卡：同环比 + 异常标记 + 口径解释。 */}
      {observability ? <InsightCards summary={observability.summary} compare={observability.compare} /> : null}

      {emptyKey ? (
        <p
          data-testid="client-stats-empty-kind"
          data-empty-kind={emptyKind}
          className="rounded-md border border-dashed border-border/80 bg-muted/30 px-3 py-2 text-sm text-muted-foreground"
        >
          {t(emptyKey)}
        </p>
      ) : null}

      {/* 数字卡：FR-357 展示更新绝对数；率仍完全复用 FR-356 口径。 */}
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
        <StatCard label={t('clientStats.updateTotal', '更新总次数')} value={observability ? String(observability.summary.updateTotal) : '—'} />
        <StatCard label={t('clientStats.updateSuccess', '更新成功')} value={observability ? String(observability.summary.updateSuccess) : '—'} />
        <StatCard label={t('clientStats.updateFailStatic', 'fail-static')} value={observability ? String(observability.summary.updateFailStatic) : '—'} />
        <StatCard label={t('clientStats.updateRolledBack', '已回退')} value={observability ? String(observability.summary.updateRolledBack) : '—'} />
        <StatCard label={t('clientStats.updateError', '更新错误')} value={observability ? String(observability.summary.updateError) : '—'} />
        <StatCard
          label={t(KPI_I18N.activeClients, '活跃客户端')}
          value={String(active.value)}
          hint={activeHintKey ? t(activeHintKey) : undefined}
        />
        <StatCard
          label={t(KPI_I18N.updateSuccessRate, '更新成功率')}
          value={formatKpiRate(updateRates.successRate)}
          hint={updateRates.source === 'none' ? t(KPI_I18N.rateUnavailable, '需遥测窗口') : undefined}
        />
        <StatCard label={t(KPI_I18N.updateFailStaticRate, 'fail-static 率')} value={formatKpiRate(updateRates.failStaticRate)} hint={t(KPI_I18N.updateFailStaticHint, '断网兜底启动')} />
        <StatCard label={t(KPI_I18N.updateRollbackRate, '回退率')} value={formatKpiRate(updateRates.rollbackRate)} />
      </div>

      {/* 下载请求趋势（FR-095；标签语义=请求次数，非更新成功） */}
      <section className="space-y-2">
        <h3 className="text-sm font-medium">{t(KPI_I18N.downloadTrend, '下载请求趋势')}</h3>
        <div className="border rounded-lg p-3">
          <TimeSeriesChart
            series={downloadSeries}
            valueFormatter={(v) => String(Math.round(v))}
            emptyHint={t('clientStats.empty', '暂无数据')}
          />
        </div>
      </section>

      <section className="space-y-2">
        <h3 className="text-sm font-medium">{t('clientStats.downloadBytesTrend', '下载字节趋势')}</h3>
        <div className="border rounded-lg p-3">
          <TimeSeriesChart series={bytesSeries} valueFormatter={formatBytes} emptyHint={t('clientStats.empty', '暂无数据')} />
        </div>
      </section>

      {/* 版本分布（FR-217 占比，按拉取量降序） */}
      <section className="space-y-2">
        <h3 className="text-sm font-medium">{t('clientStats.versionDist', '版本分布')}</h3>
        <DistBars
          items={versionDist.map((v) => {
            const version = v.version ?? 0
            return { key: String(version), label: `v${version}`, count: v.count }
          })}
          loading={obsLoading}
          emptyHint={t('clientStats.empty', '暂无数据')}
        />
      </section>

      {/* 版本滞后分布（FR-217：current - toVersion，0=已最新） */}
      <section className="space-y-2">
        <h3 className="text-sm font-medium">{t('clientStats.lagDist', '版本滞后分布')}</h3>
        <DistBars
          items={lagDist.map((l) => {
            const lag = l.lag ?? 0
            return {
              key: String(lag),
              label: lag === 0 ? t('clientStats.lagLatest', '已最新') : t('clientStats.lagBehind', '落后 {{n}} 版', { n: lag }),
              count: l.count,
            }
          })}
          loading={obsLoading}
          emptyHint={t('clientStats.empty', '暂无数据')}
        />
      </section>

      {/* 平台分布（FR-217：来源遥测 os 占比） */}
      <section className="space-y-2">
        <h3 className="text-sm font-medium">{t('clientStats.platformDist', '平台分布')}</h3>
        <DistBars
          items={platformDist.map((p) => ({ key: p.os || 'unknown', label: platformLabel(p.os ?? ''), count: p.count }))}
          loading={obsLoading}
          emptyHint={t('clientStats.empty', '暂无数据')}
        />
      </section>

      {/* 来源 IP Top 10（FR-095） */}
      <section className="space-y-2">
        <h3 className="text-sm font-medium">{t('clientStats.topIps', '来源 IP（Top 10）')}</h3>
        <div className="space-y-2 rounded-lg border p-3">
          {(stats?.topIps ?? []).length === 0 && !statsLoading && (
            <p className="text-xs text-muted-foreground">{t('clientStats.empty', '暂无数据')}</p>
          )}
          {(stats?.topIps ?? []).map((row) => (
            <div key={row.ip} className="flex items-center gap-2 text-xs">
              <span className="w-28 shrink-0 truncate font-mono" title={row.ip}>{row.ip}</span>
              <div className="h-4 flex-1 overflow-hidden rounded bg-muted">
                <div className="h-full bg-primary" style={{ width: `${(row.count / maxIpReq) * 100}%` }} />
              </div>
              <span className="w-12 shrink-0 text-right tabular-nums">{row.count}</span>
            </div>
          ))}
        </div>
      </section>

      {/* 流量合计（信息性） */}
      {stats && stats.downloads.length > 0 && (
        <p className="text-xs text-muted-foreground">
          {t('clientStats.totalBytes', '窗口内流量合计')}{' '}
          {formatBytes(stats.downloads.reduce((s, d) => s + d.bytes, 0))}
        </p>
      )}

      {/* FR-427 更新活动热力图 + FR-426 机器更新排行（observability 窗口内）。 */}
      <UpdateHeatmap series={observability?.series ?? []} />
      {machineRankingSlot}
    </div>
  )
}

/** 数字统计卡（可带次级提示，如去重口径标注）。 */
function StatCard({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="border rounded-lg p-4">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="text-2xl font-semibold mt-1 tabular-nums">{value}</div>
      {hint && <div className="mt-0.5 text-[11px] text-muted-foreground">{hint}</div>}
    </div>
  )
}

/** 一项分布条数据。 */
interface DistItem {
  key: string
  label: string
  count: number
}

/**
 * 占比分布条列表（版本 / 滞后 / 平台共用）。
 * 条宽按相对最大值，右侧同时显示占总数百分比与绝对计数。
 */
function DistBars({
  items,
  loading,
  emptyHint,
}: {
  items: DistItem[]
  loading: boolean
  emptyHint: string
}) {
  const total = items.reduce((s, it) => s + it.count, 0)
  const max = Math.max(1, ...items.map((it) => it.count))
  if (items.length === 0) {
    return (
      <div className="border rounded-lg p-3">
        {!loading && <p className="text-xs text-muted-foreground">{emptyHint}</p>}
      </div>
    )
  }
  return (
    <div className="border rounded-lg p-3 space-y-2">
      {items.map((it) => (
        <div key={it.key} className="flex items-center gap-2 text-xs">
          <span className="w-20 shrink-0 truncate font-mono" title={it.label}>{it.label}</span>
          <div className="flex-1 bg-muted rounded h-4 overflow-hidden">
            <div className="bg-primary h-full" style={{ width: `${(it.count / max) * 100}%` }} />
          </div>
          <span className="w-12 text-right tabular-nums text-muted-foreground shrink-0">
            {total > 0 ? `${((it.count / total) * 100).toFixed(0)}%` : '—'}
          </span>
          <span className="w-12 text-right tabular-nums shrink-0">{it.count}</span>
        </div>
      ))}
    </div>
  )
}
