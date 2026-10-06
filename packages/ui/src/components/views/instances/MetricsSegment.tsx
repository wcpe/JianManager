import { useTranslation } from 'react-i18next'
import { Loader2 } from 'lucide-react'
import type { ReactNode } from 'react'

import { Panel } from '@jianmanager/ui/components/panel'
import { Button } from '@jianmanager/ui/components/button'
import { TimeSeriesChart, type ChartReferenceLine, type ChartSeries } from '@jianmanager/ui'
import { RangePicker, type MetricRange } from '@jianmanager/ui'
import { cn } from '@jianmanager/ui'
import { activeMetricSources } from '@jianmanager/ui/lib/metrics-availability'
import { MetricSourceChips } from '@jianmanager/ui/components/views/instances/MetricSourceChips'
import type { MetricSeries } from '@jianmanager/ui/lib/metric-series'
import type { InstanceMetricsData } from '@jianmanager/ui/lib/instance-metrics'

/** 提示通道：视图算好文案交外壳展示（本包不弹 toast）。 */
export type MetricsNotice = (kind: 'success' | 'error', message: string) => void

/** 探针更新卡所需的全部数据；`null` 表示不渲染（能力不适用或状态未就绪）。 */
export interface ProbeUpdateCardData {
  /** 插件桥 WS 名册是否连上。 */
  bridgeConnected: boolean
  /** 运行时 metrics 是否采得到（与 TPS 同源）。 */
  metricsActive: boolean
  /** 当前解析版本。 */
  version: string
  /** 版本来源（继承/实例自选等）。 */
  versionOrigin?: string
  /** 上次下发时间。 */
  lastPushedAt?: string
  /** 可用版本 id；0 表示无可用版本。 */
  versionId: number
  /** 无可用版本时的原因。 */
  versionError?: string
  /** 可切换的已缓存版本。 */
  selectableVersions: { id: number; version: string }[]
  /** 实例级选择：0=继承。 */
  selectionVersionId: number
  /** 继承时解析到的版本号。 */
  resolvedVersion: string
  /** 更新在途。 */
  updating: boolean
  /** 版本切换在途。 */
  changing: boolean
}

/** 资源限额卡数据；`null` 表示不渲染（非 docker 或未设上限）。 */
export interface ResourceLimitData {
  cpuLimit: number
  memLimitMb: number
  diskLimitMb: number
  /** 实际内存占用（MiB）；未运行或取不到为 null。 */
  memUsedMb: number | null
}

/**
 * 探针在线更新卡（FR-068/409）：展示探针连接状态、当前解析版本和上次下发时间。
 * 实例可显式选择已缓存版本或恢复继承；切换后立即通知 Worker 拉取，但只在下次重启生效。
 *
 * 连接态分两路（真机 F2 修正）：
 * - 插件桥 WS 名册（st.probeConnected）：玩家事件/业务写依赖
 * - 运行时 metrics.probeAvailable：Worker 抓 /metrics 成功即视为探针在跑（控制台 TPS 同源）
 * 二者任一为真 → 展示「运行中」；仅桥连成功 → 「已连接」；否则「未连接」。
 * 未选择可用版本不影响「探针已在实例内运行」的事实。
 */
function ProbeUpdateCard({
  instanceId,
  data,
  onUpdate,
  onVersionChange,
  notify,
}: {
  instanceId: number
  data: ProbeUpdateCardData
  onUpdate: (restart: boolean) => Promise<{ restarted: boolean }>
  onVersionChange: (versionId: number) => Promise<void>
  notify: MetricsNotice
}) {
  const { t } = useTranslation()
  const probeRunning = data.bridgeConnected || data.metricsActive
  const statusLabel = data.bridgeConnected
    ? t('probe.connected')
    : data.metricsActive
      ? t('probe.metricsActive')
      : t('probe.disconnected')

  const doUpdate = async (restart: boolean) => {
    try {
      const r = await onUpdate(restart)
      notify('success', r.restarted ? t('probe.updatedRestarted') : t('probe.updatedPending'))
    } catch (e) {
      // 失败必须带服务端原因（真机：toast 只显「更新探针失败」干壳，422 的具体原因被吞）。
      const msg = (e as { response?: { data?: { message?: string } } })?.response?.data?.message
      notify('error', msg ? `${t('probe.updateFailed')}：${msg}` : t('probe.updateFailed'))
    }
  }
  const changeVersion = async (value: string) => {
    try {
      await onVersionChange(Number(value))
      notify('success', t('probe.versionChanged'))
    } catch (e) {
      const msg = (e as { response?: { data?: { message?: string } } })?.response?.data?.message
      notify('error', msg || t('probe.versionChangeFailed'))
    }
  }

  return (
    <Panel title={t('probe.title')}>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 p-2 text-xs">
        <span className={probeRunning ? 'font-medium text-green-600 dark:text-green-400' : 'text-muted-foreground'}>
          {statusLabel}
        </span>
        <span className="text-muted-foreground">
          {t('probe.selectedVersion')}: {data.version || '—'}
          {data.versionOrigin ? ` · ${t(`probe.origin.${data.versionOrigin}`)}` : ''}
        </span>
        {data.lastPushedAt && (
          <span className="text-muted-foreground">
            {t('probe.lastPushed')}: {new Date(data.lastPushedAt).toLocaleString()}
          </span>
        )}
        <div className="ml-auto flex gap-2">
          <Button size="sm" variant="outline" className="gap-1" disabled={!data.versionId || data.updating} onClick={() => void doUpdate(false)}>
            {data.updating && <Loader2 className="size-3.5 animate-spin" />}
            {t('probe.update')}
          </Button>
          <Button size="sm" variant="outline" className="gap-1" disabled={!data.versionId || data.updating} onClick={() => void doUpdate(true)}>
            {data.updating && <Loader2 className="size-3.5 animate-spin" />}
            {t('probe.updateRestart')}
          </Button>
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2 px-2 pb-2 text-xs">
        <label className="text-muted-foreground" htmlFor={`probe-version-${instanceId}`}>{t('probe.instanceVersion')}</label>
        <select
          id={`probe-version-${instanceId}`}
          className="h-8 min-w-52 rounded-md border bg-background px-2 text-xs"
          value={String(data.selectionVersionId)}
          disabled={data.changing || data.selectableVersions.length === 0}
          onChange={(event) => void changeVersion(event.target.value)}
        >
          <option value="0">
            {t('probe.inheritVersion', { version: data.resolvedVersion || data.version || '—' })}
          </option>
          {data.selectableVersions.map((version) => (
            <option key={version.id} value={version.id}>{version.version}</option>
          ))}
        </select>
        <span className="text-muted-foreground">{t('probe.switchHint')}</span>
      </div>
      {!data.versionId && (
        <div className="px-2 pb-2 text-xs text-status-warning">{data.versionError || t('probe.noVersion')}</div>
      )}
      {!probeRunning && data.versionId > 0 && (
        <div className="mx-2 mb-2 rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200">
          {t('probe.installHint')}
        </div>
      )}
      {/* FR-411 真机排障：桥已连接 ≠ 指标通道健康。桥通而 /metrics 不采（probeAvailable=false）
          时历史曲线与实时 TPS 全空，用户极易把「探针已连接」误读为监控正常——显式点破断链与去向。 */}
      {data.bridgeConnected && !data.metricsActive && (
        <div className="mx-2 mb-2 rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200">
          {t('probe.metricsGapHint')}
        </div>
      )}
    </Panel>
  )
}

/**
 * 资源限额对比卡（FR-079）：docker 模式实例的实际占用 vs 设定上限，超限标红。
 * - 内存：实际占用（探针 memoryMb）对比内存上限 MiB；占用越限标红。
 * - CPU：上限为核数；探针 cpuPercent 为进程 CPU%，无与核数可比的实际占用，仅展示设定上限（数据不足）。
 * - 磁盘：v1 仅持久化展示上限，无实际占用来源。
 * 实例非 docker 或未设任何上限时不渲染（由外壳判定后传 null）。
 */
function ResourceLimitCard({ data }: { data: ResourceLimitData }) {
  const { t } = useTranslation()
  const memUsed = data.memUsedMb
  const memOver = memUsed != null && data.memLimitMb > 0 && memUsed > data.memLimitMb

  return (
    <Panel title={t('metrics.resourceLimit')}>
      <div className="grid grid-cols-1 gap-2 p-2 text-xs sm:grid-cols-3">
        <div className="rounded-md border p-2">
          <p className="text-muted-foreground">{t('metrics.cpuLimit')}</p>
          <p className="mt-1 font-semibold">
            {data.cpuLimit > 0 ? t('metrics.cpuCores', { n: data.cpuLimit }) : t('metrics.unlimited')}
          </p>
        </div>
        <div className={`rounded-md border p-2 ${memOver ? 'border-destructive bg-destructive/10' : ''}`}>
          <p className="text-muted-foreground">{t('metrics.memLimit')}</p>
          <p className={`mt-1 font-semibold ${memOver ? 'text-destructive' : ''}`}>
            {data.memLimitMb > 0 ? (
              <>
                {memUsed != null ? `${memUsed} / ${data.memLimitMb} MiB` : `— / ${data.memLimitMb} MiB`}
                {memOver && <span className="ml-1">⚠ {t('metrics.overLimit')}</span>}
              </>
            ) : (
              t('metrics.unlimited')
            )}
          </p>
        </div>
        <div className="rounded-md border p-2">
          <p className="text-muted-foreground">{t('metrics.diskLimit')}</p>
          <p className="mt-1 font-semibold">
            {data.diskLimitMb > 0 ? `${data.diskLimitMb} MiB` : t('metrics.unlimited')}
          </p>
        </div>
      </div>
    </Panel>
  )
}

/**
 * 直探信息卡（FR-446 / FR-447）：展示 SLP / Query 直探回填的基础信息——
 * MOTD / 版本 / 在线人数 / 最大人数 / 玩家名单 / 插件列表 / 地图名。
 *
 * 每项按**可用性位**渲染：有值显示值，缺测显示「不可用」，不以 0 / 空串冒充。
 * 三源皆无（探针未装、SLP/Query 均不可达）时整卡不渲染——诚实标记由探针卡与健康条承载。
 */
function DirectProbeCard({ metrics, isRunning }: { metrics?: InstanceMetricsData; isRunning: boolean }) {
  const { t } = useTranslation()
  if (!isRunning || !metrics) return null
  if (activeMetricSources(metrics).length === 0) return null
  const unavailable = t('metrics.unavailable')
  const names = metrics.playerNames ?? []
  const plugins = metrics.plugins ?? []
  const field = (label: string, available: boolean | undefined, value: string) => (
    <div className="flex items-baseline justify-between gap-2 rounded-md border px-2 py-1.5">
      <span className="shrink-0 text-muted-foreground">{label}</span>
      <span
        className={cn('min-w-0 truncate text-right font-medium', (available ?? false) || 'text-muted-foreground')}
        title={available ? value : unavailable}
      >
        {available ? value : unavailable}
      </span>
    </div>
  )
  const playerNamesValue = names.length
    ? `${names.join('、')}${metrics.playerNamesPartial ? ` ${t('metrics.playerNamesPartial')}` : ''}`
    : t('metrics.emptyList')
  const pluginsValue = plugins.length ? plugins.join('、') : t('metrics.emptyList')
  return (
    <Panel title={t('metrics.directProbe')}>
      <div className="grid grid-cols-1 gap-2 p-2 text-xs sm:grid-cols-2">
        {field(t('metrics.motd'), metrics.motdAvailable, metrics.motd)}
        {field(t('metrics.version'), metrics.versionAvailable, metrics.version)}
        {field(t('metrics.players'), metrics.playersAvailable, `${metrics.onlinePlayers}/${metrics.maxPlayersAvailable ? metrics.maxPlayers : '—'}`)}
        {field(t('metrics.map'), metrics.mapAvailable, metrics.map)}
        {field(t('metrics.playerNames'), metrics.playerNamesAvailable, playerNamesValue)}
        {field(t('metrics.plugins'), metrics.pluginsAvailable, pluginsValue)}
      </div>
    </Panel>
  )
}

/** 当前健康条：仅运行中渲染；TPS/MSPT 探针不可用时显「不可用」，不以 0 冒充。 */
function HealthStrip({ metrics, isRunning }: { metrics?: InstanceMetricsData; isRunning: boolean }) {
  const { t } = useTranslation()
  if (!isRunning) {
    return (
      <Panel title={t('metrics.currentHealth')}>
        <div className="p-3 text-xs text-muted-foreground">{t('metrics.stoppedFolded')}</div>
      </Panel>
    )
  }
  if (!metrics) return null
  // TPS/MSPT 仅探针可得（FR-447）：探针不可用显「不可用」，不以 0/0.0 冒充真实值。
  const probeOk = metrics.probeAvailable
  const unavailable = t('metrics.unavailable')
  return (
    <Panel title={t('metrics.currentHealth')}>
      <div className="grid grid-cols-1 gap-2 p-2 text-xs sm:grid-cols-3">
        <HealthPill label={t('metrics.tps')} value={probeOk ? metrics.tps.toFixed(1) : unavailable} level={tpsLevel(metrics.tps)} unavailable={!probeOk} />
        <HealthPill label={t('metrics.mspt')} value={probeOk ? `${metrics.msptMillis.toFixed(0)}ms` : unavailable} level={msptLevel(metrics.msptMillis)} unavailable={!probeOk} />
        <HealthPill label={t('metrics.cpu')} value={`${metrics.cpuPercent.toFixed(0)}%`} level={cpuLevel(metrics.cpuPercent)} />
      </div>
    </Panel>
  )
}

function HealthPill({ label, value, level, unavailable }: { label: string; value: string; level: HealthLevel; unavailable?: boolean }) {
  const tone = unavailable
    ? 'border-border bg-muted/60 text-muted-foreground'
    : {
        ok: 'border-status-success/40 bg-status-success/10 text-status-success',
        warn: 'border-amber-400 bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-200',
        danger: 'border-status-danger/40 bg-status-danger/10 text-status-danger',
      }[level]
  return (
    <div className={cn('rounded-md border px-3 py-2', tone)} data-health-level={unavailable ? 'unavailable' : level}>
      <p className="text-muted-foreground">{label}</p>
      <p className="mt-1 text-base font-semibold tabular-nums">{value}</p>
    </div>
  )
}

type HealthLevel = 'ok' | 'warn' | 'danger'

function tpsLevel(value: number): HealthLevel {
  if (value < 16) return 'danger'
  if (value < 18) return 'warn'
  return 'ok'
}

function msptLevel(value: number): HealthLevel {
  if (value > 75) return 'danger'
  if (value > 50) return 'warn'
  return 'ok'
}

function cpuLevel(value: number): HealthLevel {
  if (value >= 90) return 'danger'
  if (value >= 75) return 'warn'
  return 'ok'
}

const tpsThresholds = (t: ReturnType<typeof useTranslation>['t']): ChartReferenceLine[] => [
  { value: 18, label: t('metrics.thresholdTpsWarn'), color: 'var(--status-warning)' },
  { value: 16, label: t('metrics.thresholdTpsDanger'), color: 'var(--status-danger)' },
]

const msptThresholds = (t: ReturnType<typeof useTranslation>['t']): ChartReferenceLine[] => [
  { value: 50, label: t('metrics.thresholdMsptWarn'), color: 'var(--status-warning)' },
  { value: 75, label: t('metrics.thresholdMsptDanger'), color: 'var(--status-danger)' },
]

const cpuThresholds = (t: ReturnType<typeof useTranslation>['t']): ChartReferenceLine[] => [
  { value: 75, label: t('metrics.thresholdCpuWarn'), color: 'var(--status-warning)' },
  { value: 90, label: t('metrics.thresholdCpuDanger'), color: 'var(--status-danger)' },
]

/** 字节 → G/M/K。 */
function fmtBytes(b: number): string {
  if (!Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1e9) return `${(b / 1024 / 1024 / 1024).toFixed(1)}G`
  if (b >= 1e6) return `${(b / 1024 / 1024).toFixed(0)}M`
  return `${(b / 1024).toFixed(0)}K`
}

/** 序列是否有任何非空点：全空等价于「无数据」，用于把空图收成一行字（FR-423 §3.2）。 */
function hasSeriesData(series: ChartSeries[]): boolean {
  return series.some((s) => s.points.some((p) => p.value != null))
}

/**
 * 图表卡（FR-423）：有数据渲染曲线，无数据只留一行灰字。
 *
 * {@link TimeSeriesChart} 自带的空态会按 `height` 占满整块（本页 160px），9 张图全空就是
 * 一屏半的「暂无数据」——spec §1 记的正是这种「空态占固定高度块」。故空判提到卡这一层。
 */
function ChartPanel({
  title,
  series,
  valueFormatter,
  referenceLines,
}: {
  title: string
  series: ChartSeries[]
  valueFormatter?: (v: number) => string
  referenceLines?: ChartReferenceLine[]
}) {
  const { t } = useTranslation()
  return (
    <Panel className="flex-none" title={title}>
      {hasSeriesData(series) ? (
        <TimeSeriesChart series={series} height={160} valueFormatter={valueFormatter} referenceLines={referenceLines} />
      ) : (
        <p className="text-xs text-muted-foreground">{t('common.noData')}</p>
      )}
    </Panel>
  )
}

/**
 * 实例监控段（FR-060/FR-061）：消费历史曲线渲染 TPS/MSPT/堆/在线/线程/CPU + GC 次数/GC 暂停
 * （FR-465）+ 分世界区块。探针不可用时段渲染为断点。
 *
 * 布局（FR-423）：本页不左右分栏——图表是等宽同构的，按 spec §3.1 的 62:38 切开只会把
 * 同一族曲线拆成两种宽度。改为「实例指标上下两行（xl 四列 × 8 张）+ 世界统计独立一段」，
 * 头部（标题 + 区间选择）`flex-none` 常驻，其余内容在下方单一滚动容器内滚（FR-422 骨架）。
 *
 * 受控视图（ADR-097 a 范式）：不取数——历史序列、实时指标、探针卡与限额卡数据都由外壳注入，
 * 区间与三个动作经回调上报。曲线装配、阈值线、空图收口留在视图内。
 */
export interface MetricsSegmentProps {
  /** 实例 DB id（探针卡与限额卡的表单 id 用）。 */
  instanceId: number
  /** 统计区间。 */
  range: MetricRange
  onRangeChange: (r: MetricRange) => void
  /** 历史序列。 */
  series: MetricSeries[]
  /** 序列加载中。 */
  loading?: boolean
  /** 实时指标（直探卡 / 健康条 / 来源标注同源）。 */
  metrics?: InstanceMetricsData
  /** 实例是否运行中。 */
  running: boolean
  /** 探针更新卡数据；null 或缺省表示不渲染。 */
  probeCard?: ProbeUpdateCardData | null
  /** 更新探针（是否顺带重启）。 */
  onProbeUpdate: (restart: boolean) => Promise<{ restarted: boolean }>
  /** 切换实例级探针版本（0=继承）。 */
  onProbeVersionChange: (versionId: number) => Promise<void>
  /** 资源限额卡数据；null 或缺省表示不渲染。 */
  resourceLimit?: ResourceLimitData | null
  /** 性能归因卡插槽（外壳用其接线层渲染，它要自己按需取数）。 */
  attributionSlot: ReactNode
  /** 容量预测卡插槽（同上）。 */
  forecastSlot: ReactNode
  /** 提示通道。 */
  notify: MetricsNotice
}

export default function MetricsSegment({
  instanceId,
  range,
  onRangeChange,
  series,
  loading = false,
  metrics,
  running,
  probeCard,
  onProbeUpdate,
  onProbeVersionChange,
  resourceLimit,
  attributionSlot,
  forecastSlot,
  notify,
}: MetricsSegmentProps) {
  const { t } = useTranslation()

  const one = (metricKey: string, name: string): ChartSeries[] => {
    const s = series.find((x) => x.metricKey === metricKey && x.world === '')
    if (!s) return []
    return [{ key: metricKey, name, points: s.points.map((p) => ({ ts: p.ts, value: p.avg })) }]
  }
  // 多指标同图（如堆 used·max 叠加）
  const many = (...pairs: [string, string][]): ChartSeries[] => pairs.flatMap(([k, n]) => one(k, n))
  // 分世界：同一 metricKey 下每个 world 一条线
  const byWorld = (metricKey: string): ChartSeries[] =>
    series
      .filter((x) => x.metricKey === metricKey && x.world !== '')
      .map((s) => ({ key: s.world, name: s.world, points: s.points.map((p) => ({ ts: p.ts, value: p.avg })) }))

  if (loading) {
    return <div className="p-4 text-sm text-muted-foreground">{t('common.loading')}</div>
  }

  const worldChunks = byWorld('world_loaded_chunks')
  const worldEntities = byWorld('world_entities')
  const worldTiles = byWorld('world_tile_entities')
  // 分世界指标要么整族有（探针上报世界维度）、要么整族无，故按族判空收成一行字，
  // 而不是留三张空图各占 160px。
  const hasWorldData = [worldChunks, worldEntities, worldTiles].some(hasSeriesData)

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 p-4">
      {/* 区间选择常驻：改区间是本页主操作，不该被曲线滚出视野。 */}
      <div className="flex flex-none items-center justify-between">
        <div className="flex items-center gap-2">
          <h3 className="text-sm font-semibold">{t('metrics.title')}</h3>
          {/* 数据来源标注（FR-447）：探针 / SLP 直探 / Query 直探。 */}
          <MetricSourceChips metrics={metrics ?? {}} />
        </div>
        <RangePicker value={range} onChange={onRangeChange} />
      </div>
      {/* 本页唯一滚动容器（FR-422：滚动收口在卡内，顶栏与 Tab 栏不滚）。 */}
      <div className="min-h-0 flex-1 space-y-3 overflow-auto">
        {probeCard && (
          <ProbeUpdateCard
            instanceId={instanceId}
            data={probeCard}
            onUpdate={onProbeUpdate}
            onVersionChange={onProbeVersionChange}
            notify={notify}
          />
        )}
        <DirectProbeCard metrics={metrics} isRunning={running} />
        <HealthStrip metrics={metrics} isRunning={running} />
        {resourceLimit && <ResourceLimitCard data={resourceLimit} />}
        {/* 实例指标：xl 四列 × 8 张 = 上下两行（spec §3.1 监控行）。
            列数取 4 而非 3：本 grid 内实为 8 张卡（含 FR-465 新增的 2 张 GC 曲线），
            三列会排成 3+3+2、末行留一个空位，与「上下两行」的布局意图不符（自审 M10）。
            items-start 是必需的（spec §3.2）：grid 默认 stretch 会把「暂无数据」的一行字卡
            拉到同排曲线卡的高度（实测被拉成 222px，卡内 141px 死区）。 */}
        <div className="grid grid-cols-1 items-start gap-3 lg:grid-cols-2 xl:grid-cols-4">
          <ChartPanel
            title={t('metrics.tps')}
            series={one('inst_tps', t('metrics.tps'))}
            valueFormatter={(v) => v.toFixed(1)}
            referenceLines={tpsThresholds(t)}
          />
          <ChartPanel
            title={t('metrics.mspt')}
            series={one('inst_mspt', t('metrics.mspt'))}
            valueFormatter={(v) => `${v.toFixed(1)}ms`}
            referenceLines={msptThresholds(t)}
          />
          <ChartPanel
            title={t('metrics.heap')}
            series={many(['inst_heap_used', t('metrics.heapUsed')], ['inst_heap_max', t('metrics.heapMax')])}
            valueFormatter={fmtBytes}
          />
          <ChartPanel
            title={t('metrics.players')}
            series={one('inst_players_online', t('metrics.players'))}
            valueFormatter={(v) => v.toFixed(0)}
          />
          <ChartPanel
            title={t('metrics.threads')}
            series={one('inst_threads', t('metrics.threads'))}
            valueFormatter={(v) => v.toFixed(0)}
          />
          <ChartPanel
            title={t('metrics.cpu')}
            series={one('inst_cpu_pct', t('metrics.cpu'))}
            valueFormatter={(v) => `${v.toFixed(0)}%`}
            referenceLines={cpuThresholds(t)}
          />
          {/* GC（FR-465）：探针累计 counter 经 CP 相邻心跳差推导的速率（次数/s、暂停 ms/s）。
              标题走 `metrics.*`：与本 grid 其余 6 张卡同命名空间，避免与「性能归因」卡片耦合
              （改归因措辞不应顺带改监控页图表标题；自审 M4）。 */}
          <ChartPanel
            title={t('metrics.gcCount')}
            series={one('inst_gc_count', t('metrics.gcCount'))}
            valueFormatter={(v) => `${v.toFixed(2)}/s`}
          />
          <ChartPanel
            title={t('metrics.gcTime')}
            series={one('inst_gc_time_ms', t('metrics.gcTime'))}
            valueFormatter={(v) => `${v.toFixed(1)}ms/s`}
          />
        </div>
        {/* 性能归因 + 容量预测（FR-465 / FR-464）：按需查询，与曲线区并列展示。
            两卡的接线层由外壳注入（它们要各自取数），故此处以插槽渲染。 */}
        <div className="grid grid-cols-1 items-start gap-3 lg:grid-cols-2">
          {attributionSlot}
          {forecastSlot}
        </div>
        {/* 世界统计：与实例指标分段，无世界维度数据时整段收成一行字。 */}
        {hasWorldData ? (
          <div className="grid grid-cols-1 items-start gap-3 lg:grid-cols-2 xl:grid-cols-3">
            <ChartPanel title={t('metrics.worldChunks')} series={worldChunks} valueFormatter={(v) => v.toFixed(0)} />
            <ChartPanel title={t('metrics.worldEntities')} series={worldEntities} valueFormatter={(v) => v.toFixed(0)} />
            <ChartPanel title={t('metrics.worldTileEntities')} series={worldTiles} valueFormatter={(v) => v.toFixed(0)} />
          </div>
        ) : (
          <Panel className="flex-none" title={t('metrics.worldStats')}>
            <p className="text-xs text-muted-foreground">{t('metrics.noWorldData')}</p>
          </Panel>
        )}
      </div>
    </div>
  )
}
