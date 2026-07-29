import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router'
import { AlertTriangle, GitBranch, Loader2, ShieldCheck } from 'lucide-react'
import { useNodes } from '@/api/nodes'
import { useInstances } from '@/api/instances'
import { useBotRuntimeMetrics, useManagedProcessAction, useManagedProcessDetail, useMetricOverview, useMetricSeries, useProcessTop, type ManagedProcessAction, type ManagedProcessDetail, type ManagedProcessInfo, type ProcessTopItem } from '@/api/metrics'
import DangerConfirm from '@/components/DangerConfirm'
import { Panel } from '@jianmanager/ui/components/panel'
import { Button } from '@jianmanager/ui/components/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { RangePicker, ResolutionPicker, type MetricRange, type MetricResolution } from '@jianmanager/ui'
import { MonitorSkeleton, type MonitorSource } from '@jianmanager/ui'
import { MetricsOverviewStrip } from '@jianmanager/ui'
import { MetricComparePanel } from '@/components/charts/MetricComparePanel'
import { DrillTargetPicker, targetKey, type DrillTarget } from '@/components/charts/DrillTargetPicker'
import { useTargetSeries } from '@/components/charts/use-target-series'
import {
  NODE_CHART_DEFS,
  INSTANCE_CHART_DEFS,
  PLATFORM_CHART_DEFS,
  type MetricChartDef,
  type RawSeries,
} from '@jianmanager/ui'

/** 据 target 选用的图定义集（平台 4 / 节点 6 / 实例 6）。 */
function defsFor(kind: DrillTarget['kind']): MetricChartDef[] {
  if (kind === 'node') return NODE_CHART_DEFS
  if (kind === 'instance') return INSTANCE_CHART_DEFS
  return PLATFORM_CHART_DEFS
}

function defaultCompareMetric(kind: DrillTarget['kind']): string {
  if (kind === 'instance') return 'inst_tps'
  return 'node_cpu_pct'
}

function targetFromSearch(searchParams: URLSearchParams): DrillTarget {
  const instance = searchParams.get('instance')
  if (instance) return { kind: 'instance', uuid: instance }
  const node = searchParams.get('node')
  if (node) return { kind: 'node', uuid: node }
  return { kind: 'platform' }
}

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '--'
  const units = ['B', 'KiB', 'MiB', 'GiB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}

function formatRate(read: number, write: number): string {
  const total = read + write
  return total > 0 ? `${formatBytes(total)}/s` : '--'
}

function formatTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '--' : date.toLocaleString()
}

function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return '--'
  if (seconds < 60) return `${Math.round(seconds)}s`
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`
  return `${(seconds / 3600).toFixed(1)}h`
}

function ProcessTopPanel({ rows, onInspect }: { rows: ProcessTopItem[]; onInspect: (row: ProcessTopItem) => void }) {
  const { t } = useTranslation()
  return (
    <Panel title={t('monitor.processTop', '进程 TOP10')}>
      <p className="mb-2 flex items-center gap-1.5 text-[11px] text-muted-foreground">
        <ShieldCheck className="size-3.5" />
        {t('monitor.processManagedOnly', '仅展示 JianManager 受管实例进程树，命令摘要已脱敏。')}
      </p>
      {rows.length === 0 ? (
        <p className="py-5 text-center text-sm text-muted-foreground">{t('monitor.processTopEmpty', '暂无进程采样')}</p>
      ) : (
        <div className="divide-y divide-border text-xs">
          {rows.map((row) => (
            <div
              key={`${row.pid}-${row.sampledAt}`}
              tabIndex={0}
              data-testid="process-top-row"
              aria-label={t('monitor.processTopRow', { pid: row.pid, name: row.name, defaultValue: `进程 ${row.pid} ${row.name}` })}
              className="group relative grid grid-cols-[72px_80px_1fr_80px_90px_90px_72px] items-center gap-3 py-2 outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <span className="font-mono text-muted-foreground">PID {row.pid}</span>
              <span className="truncate text-muted-foreground" title={row.user || '--'}>{row.user || '--'}</span>
              <span className="min-w-0 truncate" title={row.commandSummary || row.name}>
                {row.commandSummary || row.name || '--'}
              </span>
              <span className="font-mono tabular-nums">{row.cpuPercent.toFixed(1)}%</span>
              <span className="font-mono tabular-nums">{formatBytes(row.rssBytes)}</span>
              <span className="font-mono tabular-nums">{formatRate(row.readBytesPerSec, row.writeBytesPerSec)}</span>
              <Button type="button" variant="outline" size="xs" onClick={() => onInspect(row)}>
                {t('monitor.inspectProcess', '探查')}
              </Button>
              <div
                data-testid="process-top-hover"
                className="pointer-events-none absolute right-0 top-[calc(100%-0.25rem)] z-30 w-80 rounded-md border bg-popover p-3 text-popover-foreground opacity-0 shadow-soft transition-opacity duration-150 group-hover:opacity-100 group-focus-within:opacity-100"
              >
                <div className="mb-2 min-w-0 truncate font-medium">{row.commandSummary || row.name || '--'}</div>
                <dl className="grid grid-cols-[92px_1fr] gap-x-3 gap-y-1 text-[11px]">
                  <dt className="text-muted-foreground">{t('monitor.processPid', 'PID')}</dt>
                  <dd className="font-mono tabular-nums">{row.pid}</dd>
                  <dt className="text-muted-foreground">{t('monitor.processUser', '用户')}</dt>
                  <dd>{row.user || '--'}</dd>
                  <dt className="text-muted-foreground">{t('monitor.processSampledAt', '采样时间')}</dt>
                  <dd>{formatTime(row.sampledAt)}</dd>
                  <dt className="text-muted-foreground">{t('monitor.processCpu', 'CPU')}</dt>
                  <dd className="font-mono tabular-nums">{row.cpuPercent.toFixed(1)}%</dd>
                  <dt className="text-muted-foreground">{t('monitor.processMemory', '内存')}</dt>
                  <dd className="font-mono tabular-nums">{formatBytes(row.rssBytes)}</dd>
                  <dt className="text-muted-foreground">{t('monitor.processIo', 'IO 读/写')}</dt>
                  <dd className="font-mono tabular-nums">
                    {formatBytes(row.readBytesPerSec)}/s · {formatBytes(row.writeBytesPerSec)}/s
                  </dd>
                </dl>
              </div>
            </div>
          ))}
        </div>
      )}
    </Panel>
  )
}

function BotRuntimePanel({ reason }: { reason?: string }) {
  const { t } = useTranslation()
  return (
    <Panel title={t('monitor.botRuntime.title')}>
      <p className="text-sm text-muted-foreground">{t('monitor.botRuntime.notice')}</p>
      {reason && <p className="mt-2 text-sm text-amber-600">{t('monitor.botRuntime.unavailable', { reason })}</p>}
    </Panel>
  )
}

function ProcessDetailDialog({
  open,
  detail,
  isError,
  onOpenChange,
  onAction,
  onRefresh,
  actionPending,
  refreshing,
}: {
  open: boolean
  detail?: ManagedProcessDetail
  isError: boolean
  onOpenChange: (open: boolean) => void
  onAction: (action: ManagedProcessAction) => void
  onRefresh: () => void
  actionPending: boolean
  refreshing: boolean
}) {
  const { t } = useTranslation()
  const target = detail?.target
  const children = detail?.children ?? []
  const ancestors = detail?.ancestors ?? []

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <GitBranch className="size-5 text-primary" />
            {target ? t('monitor.processDetailTitle', { pid: target.pid, defaultValue: `进程 ${target.pid} 详情` }) : t('monitor.processDetailTitleFallback', '进程详情')}
          </DialogTitle>
          <DialogDescription>{t('monitor.processDetailHint', '只探查当前实例的受管进程树，处置前会再次校验 PID 归属。')}</DialogDescription>
          <Button type="button" variant="outline" size="xs" className="w-fit" disabled={refreshing} onClick={onRefresh}>
            {refreshing && <Loader2 className="size-3.5 animate-spin" />}
            {t('common.refresh', '刷新')}
          </Button>
        </DialogHeader>
        {!detail || !target ? (
          <p className="text-sm text-muted-foreground">{isError ? t('monitor.processDetailError', '进程详情暂时不可用') : t('common.loading')}</p>
        ) : (
          <div className="space-y-4">
            {isError && <p className="text-sm text-muted-foreground">{t('monitor.processDetailError', '进程详情暂时不可用')}</p>}
            <div className="grid gap-3 md:grid-cols-2">
              <Panel title={t('monitor.processSummary', '摘要')} bodyClassName="space-y-2">
                <ProcessField label={t('monitor.processPid', 'PID')} value={String(target.pid)} mono />
                <ProcessField label={t('monitor.processParentPid', '父 PID')} value={String(target.parentPid)} mono />
                <ProcessField label={t('monitor.processUser', '用户')} value={target.user || '--'} />
                <ProcessField label={t('monitor.processThreadCount', '线程数')} value={String(target.threadCount)} mono />
                <ProcessField label={t('monitor.processUptime', '运行时长')} value={formatDuration(target.uptimeSeconds)} mono />
                <ProcessField label={t('monitor.processSampledAt', '采样时间')} value={formatTime(target.sampledAt)} />
                <ProcessField label={t('monitor.processCommand', '命令摘要')} value={target.commandSummary || '--'} mono />
                {target.unavailableReason && <p className="text-xs text-amber-600">{target.unavailableReason}</p>}
              </Panel>
              <Panel title={t('monitor.processHistory', '最近窗口')} bodyClassName="space-y-2">
                <ProcessField label={t('monitor.processWindow', '窗口')} value={t('monitor.processHistoryWindow', '{{seconds}} 秒', { seconds: detail.history.windowSeconds })} />
                <ProcessField label={t('monitor.processSamples', '样本数')} value={String(detail.history.sampleCount)} mono />
                <ProcessField label={t('monitor.processAvgCpu', '平均 CPU')} value={`${detail.history.avgCpuPercent.toFixed(1)}%`} mono />
                <ProcessField label={t('monitor.processAvgWrite', '平均写入')} value={`${formatBytes(detail.history.avgWriteBytesPerSec)}/s`} mono />
                <ProcessField label={t('monitor.processRssDelta', 'RSS 变化')} value={formatBytes(detail.history.rssDeltaBytes)} mono />
                <ProcessField label={t('monitor.processLatestSample', '最近采样')} value={formatTime(detail.history.latestSampledAt)} />
              </Panel>
            </div>

            {detail.diagnostics.length > 0 && (
              <Panel title={t('monitor.processDiagnostics', '诊断建议')}>
                <div className="space-y-2">
                  {detail.diagnostics.map((diag) => (
                    <div key={diag.code} className="rounded-md border bg-muted/20 p-3 text-sm">
                      <p className="font-medium">{diag.title}</p>
                      <p className="mt-1 text-muted-foreground">{diag.evidence}</p>
                      <p className="mt-1 text-muted-foreground">{diag.suggestion}</p>
                    </div>
                  ))}
                </div>
              </Panel>
            )}

            <div className="grid gap-3 md:grid-cols-2">
              <Panel title={t('monitor.processAncestors', '祖先链')}>
                {ancestors.length === 0 ? <p className="text-sm text-muted-foreground">{t('monitor.processNoAncestors', '无')}</p> : ancestors.map((item) => <ProcessNodeItem key={`ancestor-${item.pid}`} item={item} />)}
              </Panel>
              <Panel title={t('monitor.processChildren', '后代进程')}>
                {children.length === 0 ? <p className="text-sm text-muted-foreground">{t('monitor.processNoChildren', '无')}</p> : children.map((item) => <ProcessNodeItem key={`child-${item.pid}`} item={item} />)}
              </Panel>
            </div>

            <div className="flex flex-wrap justify-end gap-2">
              <Button type="button" variant="outline" disabled={actionPending || target.isRoot} onClick={() => onAction('terminate')}>
                <AlertTriangle className="size-3.5" />
                {t('monitor.processTerminate', '温和终止')}
              </Button>
              <Button type="button" variant="destructive" disabled={actionPending || target.isRoot} onClick={() => onAction('kill_tree')}>
                {actionPending && <Loader2 className="size-3.5 animate-spin" />}
                {t('monitor.processKillTree', '强制终止树')}
              </Button>
            </div>
            {target.isRoot && <p className="text-xs text-amber-600">{t('monitor.processRootDenied', '根进程不支持 PID 级处置，请使用实例级停止/重启/强制终止。')}</p>}
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

function ProcessField({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="grid grid-cols-[96px_1fr] gap-3 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className={mono ? 'font-mono break-all' : 'break-words'}>{value}</span>
    </div>
  )
}

function ProcessNodeItem({ item }: { item: ManagedProcessInfo }) {
  const { t } = useTranslation()
  return (
    <div className="rounded-md border px-3 py-2 text-sm">
      <div className="flex items-center justify-between gap-2">
        <strong className="font-mono">PID {item.pid}</strong>
        {item.isRoot && <span className="rounded-full bg-muted px-2 py-0.5 text-[11px] text-muted-foreground">{t('monitor.processRoot', '根')}</span>}
      </div>
      <p className="mt-1 truncate text-muted-foreground">{item.commandSummary || item.name || '--'}</p>
      <p className="mt-1 text-xs text-muted-foreground">
        {item.cpuPercent.toFixed(1)}% · {formatBytes(item.rssBytes)} · {formatDuration(item.uptimeSeconds)}
      </p>
    </div>
  )
}

function useMonitorSeries(
  source: MonitorSource,
  range: MetricRange,
  resolution: MetricResolution,
): { series: RawSeries[]; isLoading: boolean } {
  const isPlatform = source.kind === 'platform'
  const targetId = isPlatform ? '' : source.uuid
  const scope = source.kind === 'instance' ? 'instance' : 'node'

  const overview = useMetricOverview(range, resolution)
  const seriesQ = useMetricSeries({ scope, targetId, range, resolution, enabled: !isPlatform && !!targetId })

  if (isPlatform) {
    const series: RawSeries[] = (overview.data?.trends ?? []).map((trend) => ({
      metricKey: trend.metricKey,
      points: trend.points.map((point) => ({ ts: point.ts, value: point.avg })),
    }))
    return { series, isLoading: overview.isLoading }
  }

  const series: RawSeries[] = (seriesQ.data?.series ?? []).map((item) => ({
    metricKey: item.metricKey,
    world: item.world,
    points: item.points.map((point) => ({ ts: point.ts, value: point.avg })),
  }))
  return { series, isLoading: seriesQ.isLoading }
}

/**
 * 统一监控页（FR-169 + FR-221 时序剖析增强）。在既有 FR-060/061 时序底座上加四个剖析维度：
 * 1. 关键指标概览——一屏看当前值 + sparkline 趋势缩略；
 * 2. 自定义聚合粒度 + 时间范围——页级 RangePicker + ResolutionPicker（auto/30s/5m/1h，ADR-013 三档）；
 * 3. 多指标对比/叠加——勾选多条指标在同一图叠加对比；
 * 4. 下钻——平台→节点→实例→世界 逐级钻取（面包屑 + 各层下拉）。
 * 仅纯前端消费既有 /metrics/series、/metrics/overview，不改后端、不另立 ADR（沿用 ADR-013）。
 */
export default function MonitoringPage() {
  const { t } = useTranslation()
  const [searchParams] = useSearchParams()
  const { data: nodes } = useNodes()
  const { data: instances } = useInstances()
  const [target, setTarget] = useState<DrillTarget>(() => targetFromSearch(searchParams))
  // 页级范围 + 粒度（驱动概览/对比；主图网格各图另有独立范围，但共享该页级粒度）。
  const [range, setRange] = useState<MetricRange>('24h')
  const [resolution, setResolution] = useState<MetricResolution>('auto')
  // 多指标对比默认选中一个关键指标；随 target 切换重置。
  const [compareSel, setCompareSel] = useState<string[]>(() => [defaultCompareMetric(targetFromSearch(searchParams).kind)])
  const [selectedProcess, setSelectedProcess] = useState<ProcessTopItem | null>(null)
  const [pendingAction, setPendingAction] = useState<ManagedProcessAction | null>(null)

  const tKey = targetKey(target)
  const currentInstance = target.kind === 'instance' ? (instances ?? []).find((i) => i.uuid === target.uuid) : undefined
  const currentNode = target.kind === 'node' ? (nodes ?? []).find((node) => node.uuid === target.uuid) : undefined
  const currentNodeUUID = currentNode?.uuid
  const { data: processTop = [] } = useProcessTop({
    instanceId: currentInstance?.id,
    nodeId: currentNodeUUID,
    enabled: target.kind !== 'instance' || !!currentInstance,
  })
  const selectedInstanceId = selectedProcess?.instanceId
  const selectedPid = selectedProcess?.pid
  const processDetail = useManagedProcessDetail(selectedInstanceId, selectedPid, !!selectedProcess)
  const processAction = useManagedProcessAction()
  const botRuntime = useBotRuntimeMetrics({
    nodeId: currentNode?.id,
    range,
    resolution,
    enabled: target.kind === 'node' && !!currentNode,
  })
  const botRuntimeReason = botRuntime.data?.unavailable.find((item) => item.nodeId === currentNode?.id)?.reason

  // 概览/对比/主图共享的数据源描述（MonitorSource 与 SeriesTarget 同构）。
  const source: MonitorSource =
    target.kind === 'platform' ? { kind: 'platform' } : { kind: target.kind, uuid: target.uuid }
  // 概览/对比共享的原始序列（页级范围 + 粒度）。
  const { series: raw, isLoading } = useTargetSeries(source, range, resolution)

  // 当前实例的世界名列表（来自其分世界序列），供下钻到世界的下拉。
  const worlds = useMemo(() => {
    if (target.kind !== 'instance') return []
    const set = new Set<string>()
    for (const s of raw) if (s.world) set.add(s.world)
    return [...set].sort()
  }, [raw, target.kind])

  const worldFilter = target.kind === 'instance' ? target.world : undefined

  const toggleCompare = (metricKey: string) =>
    setCompareSel((prev) =>
      prev.includes(metricKey) ? prev.filter((k) => k !== metricKey) : [...prev, metricKey],
    )

  // target 切换时重置对比选择（不同 target 指标目录不同）。
  const onChangeTarget = (next: DrillTarget) => {
    if (next.kind !== target.kind) setCompareSel([defaultCompareMetric(next.kind)])
    setTarget(next)
  }

  const confirmAction = () => {
    if (!pendingAction || !selectedProcess) return
    processAction.mutate(
      { instanceId: selectedProcess.instanceId, pid: selectedProcess.pid, action: pendingAction },
      { onSuccess: () => setPendingAction(null) },
    )
  }

  const actionInstanceName = processDetail.data?.instance.name || selectedProcess?.instanceUuid || '--'
  const actionScope = pendingAction === 'kill_tree' ? '目标 PID 及其子进程树' : '仅目标 PID'
  const actionMode = pendingAction === 'kill_tree' ? '强制终止树' : '温和终止'
  const actionDescription = selectedProcess
    ? `实例 ${actionInstanceName}，PID ${selectedProcess.pid}，模式 ${actionMode}，影响范围：${actionScope}。后端会再次确认 PID 归属并要求 confirm=true。`
    : t('monitor.processActionDescription', '将只作用于该实例当前受管进程树内的目标 PID；后端会再次确认 PID 归属并要求 confirm=true。')

  return (
    <div data-page="monitoring" className="jm-page-stack space-y-4">
      <div className="jm-page-header flex-wrap">
        <h1 className="jm-page-title">{t('monitor.title')}</h1>
        <div className="flex flex-wrap items-center gap-2">
          <ResolutionPicker value={resolution} onChange={setResolution} />
          <RangePicker value={range} onChange={setRange} />
        </div>
      </div>

      {/* 下钻：平台 → 节点 → 实例 → 世界 */}
      <Panel bodyClassName="px-3 py-2">
        <DrillTargetPicker
          target={target}
          onChange={onChangeTarget}
          nodes={nodes ?? []}
          instances={instances ?? []}
          worlds={worlds}
        />
      </Panel>

      {/* 关键指标概览：当前值 + sparkline 趋势缩略 */}
      <Panel title={t('monitor.overview.title')}>
        <MetricsOverviewStrip kind={target.kind} raw={raw} isLoading={isLoading} />
      </Panel>

      {/* 多指标对比/叠加 */}
      <Panel title={t('monitor.compare.title')}>
        <MetricComparePanel kind={target.kind} raw={raw} selected={compareSel} onToggle={toggleCompare} />
      </Panel>

      <ProcessTopPanel rows={processTop} onInspect={setSelectedProcess} />

      <ProcessDetailDialog
        open={!!selectedProcess}
        detail={processDetail.data}
        isError={processDetail.isError}
        actionPending={processAction.isPending}
        refreshing={processDetail.isFetching}
        onAction={setPendingAction}
        onRefresh={() => processDetail.refetch()}
        onOpenChange={(open) => {
          if (!open) {
            setSelectedProcess(null)
            setPendingAction(null)
          }
        }}
      />
      <DangerConfirm
        open={pendingAction !== null}
        title={pendingAction === 'kill_tree' ? t('monitor.processKillTreeConfirm', '强制终止进程树') : t('monitor.processTerminateConfirm', '终止受管子进程')}
        description={actionDescription}
        confirmLabel={pendingAction === 'kill_tree' ? t('monitor.processKillTree', '强制终止树') : t('monitor.processTerminate', '温和终止')}
        pending={processAction.isPending}
        onConfirm={confirmAction}
        onCancel={() => setPendingAction(null)}
      />

      {target.kind === 'node' && <BotRuntimePanel reason={botRuntimeReason} />}

      <Panel bodyClassName="px-3 py-2 text-[11px] text-muted-foreground">{t('monitor.hint')}</Panel>

      {/* 主图网格（每图独立时间筛选 + brush + hover），共享页级粒度与下钻世界聚焦 */}
      <MonitorSkeleton
        key={tKey}
        defs={defsFor(target.kind)}
        source={source}
        defaultRange={range}
        resolution={resolution}
        worldFilter={worldFilter}
        useSeries={useMonitorSeries}
      />
    </div>
  )
}
