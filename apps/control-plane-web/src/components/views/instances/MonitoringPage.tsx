import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, GitBranch, Loader2, ShieldCheck } from 'lucide-react'

import { Panel } from '@jianmanager/ui/components/panel'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Button } from '@jianmanager/ui/components/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { RangePicker, ResolutionPicker } from '@jianmanager/ui/charts/RangePicker'
import type { MetricRange, MetricResolution } from '@jianmanager/ui/charts/RangePicker'
import { MonitorSkeleton, type MonitorSeriesHook, type MonitorSource } from '@jianmanager/ui/charts/MonitorSkeleton'
import type { MetricChartDef } from '@jianmanager/ui/lib/monitor-metrics'
import { MetricsOverviewStrip } from '@jianmanager/ui/charts/MetricsOverviewStrip'
import { MetricComparePanel } from '@jianmanager/ui/charts/MetricComparePanel'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import type { RawSeries } from '@jianmanager/ui/lib/monitor-metrics'
import type { DrillTarget } from '@/components/views/instances/DrillTargetPicker'
import type { ManagedProcessAction, ManagedProcessDetail, ManagedProcessInfo, ProcessTopItem } from '@/lib/console/managed-process'
import { formatFileSize } from '@/lib/shared/format-file-size'

/** 
 * 本页字节展示统一走共享实现。
 * 0/负值/非有限数沿用本页原有的 `--` 占位（进程 RSS 与读写速率为 0 时「无数据」比「0 B」更贴切）。
 */
const BYTES_FALLBACK = { fallback: '--' } as const

function formatRate(read: number, write: number): string {
  const total = read + write
  return total > 0 ? `${formatFileSize(total)}/s` : '--'
}

/**
 * 带符号的字节差（RSS 变化）：负值表示内存在下降，不能当“无数据”。
 * 共享格式化器只接受非负数（<=0 直接回落占位），故负数在此单独处理。
 */
function formatSignedFileSize(bytes: number): string {
  if (!Number.isFinite(bytes)) return '--'
  if (bytes === 0) return '0 B'
  const sign = bytes < 0 ? '-' : '+'
  return `${sign}${formatFileSize(Math.abs(bytes), BYTES_FALLBACK)}`
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

function ProcessTopPanel({ rows, onInspect, isError = false }: { rows: ProcessTopItem[]; onInspect: (row: ProcessTopItem) => void; isError?: boolean }) {
  const { t } = useTranslation()
  return (
    <Panel title={t('monitor.processTop', '进程 TOP10')}>
      <p className="mb-2 flex items-center gap-1.5 text-meta text-muted-foreground">
        <ShieldCheck className="size-3.5" />
        {t('monitor.processManagedOnly', '仅展示 JianManager 受管实例进程树，命令摘要已脱敏。')}
      </p>
      {isError ? (
        <p className="py-5 text-center text-sm text-muted-foreground">{t('monitor.processTopError', '进程数据加载失败')}</p>
      ) : rows.length === 0 ? (
        <p className="py-5 text-center text-sm text-muted-foreground">{t('monitor.processTopEmpty', '暂无进程采样')}</p>
      ) : (
        <div className="divide-y divide-border text-xs">
          {rows.map((row) => (
            <div
              // key 用「节点 + 实例 + PID」：PID 只在单台主机内唯一，平台榜会把多个节点的
              // 同号 PID 放在同一张表里（如各节点都有 4321）。只用 pid 会重复 key，
              // 且榜单变化时 React 会复用错行——用户会看到一条已经不存在的进程且仍能探查。
              key={`${row.nodeUuid}:${row.instanceUuid}:${row.pid}`}
              tabIndex={0}
              data-testid="process-top-row"
              aria-label={t('monitor.processTopRow', { pid: row.pid, name: row.name, defaultValue: `进程 ${row.pid} ${row.name}` })}
              // 原先用 7 条固定像素列（72/80/80/90/90/72px），合计 ~556px：窄屏（390px）
              // 必然横向溢出，要横滚才点得到「探查」。改为可换行的 flex 行——各指标格定宽
              // （各行同宽，列仍对齐），命令格 `min-w-0 flex-1` 收缩截断，装不下时整行换行，
              // 探查按钮始终可见。
              className="group relative flex flex-wrap items-center gap-x-3 gap-y-1 py-2 outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <span className="w-16 shrink-0 font-mono text-muted-foreground">PID {row.pid}</span>
              <span className="w-20 shrink-0 truncate text-muted-foreground" title={row.user || '--'}>{row.user || '--'}</span>
              <span className="min-w-0 flex-1 truncate" title={row.commandSummary || row.name}>
                {row.commandSummary || row.name || '--'}
              </span>
              <span className="w-16 shrink-0 text-right font-mono tabular-nums">{row.cpuPercent.toFixed(1)}%</span>
              <span className="w-20 shrink-0 text-right font-mono tabular-nums">{formatFileSize(row.rssBytes, BYTES_FALLBACK)}</span>
              <span className="w-20 shrink-0 text-right font-mono tabular-nums">{formatRate(row.readBytesPerSec, row.writeBytesPerSec)}</span>
              <Button type="button" variant="outline" size="xs" className="shrink-0" onClick={() => onInspect(row)}>
                {t('monitor.inspectProcess', '探查')}
              </Button>
              <div
                data-testid="process-top-hover"
                className="pointer-events-none absolute right-0 top-[calc(100%-0.25rem)] z-30 w-80 rounded-md border bg-popover p-3 text-popover-foreground opacity-0 shadow-soft transition-opacity duration-150 group-hover:opacity-100 group-focus-within:opacity-100"
              >
                <div className="mb-2 min-w-0 truncate font-medium">{row.commandSummary || row.name || '--'}</div>
                <dl className="grid grid-cols-[92px_1fr] gap-x-3 gap-y-1 text-meta">
                  <dt className="text-muted-foreground">{t('monitor.processPid', 'PID')}</dt>
                  <dd className="font-mono tabular-nums">{row.pid}</dd>
                  <dt className="text-muted-foreground">{t('monitor.processUser', '用户')}</dt>
                  <dd>{row.user || '--'}</dd>
                  <dt className="text-muted-foreground">{t('monitor.processSampledAt', '采样时间')}</dt>
                  <dd>{formatTime(row.sampledAt)}</dd>
                  <dt className="text-muted-foreground">{t('monitor.processCpu', 'CPU')}</dt>
                  <dd className="font-mono tabular-nums">{row.cpuPercent.toFixed(1)}%</dd>
                  <dt className="text-muted-foreground">{t('monitor.processMemory', '内存')}</dt>
                  <dd className="font-mono tabular-nums">{formatFileSize(row.rssBytes, BYTES_FALLBACK)}</dd>
                  <dt className="text-muted-foreground">{t('monitor.processIo', 'IO 读/写')}</dt>
                  <dd className="font-mono tabular-nums">
                    {formatFileSize(row.readBytesPerSec, BYTES_FALLBACK)}/s · {formatFileSize(row.writeBytesPerSec, BYTES_FALLBACK)}/s
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
        {item.isRoot && <span className="rounded-full bg-muted px-2 py-0.5 text-meta text-muted-foreground">{t('monitor.processRoot', '根')}</span>}
      </div>
      <p className="mt-1 truncate text-muted-foreground">{item.commandSummary || item.name || '--'}</p>
      <p className="mt-1 text-xs text-muted-foreground">
        {item.cpuPercent.toFixed(1)}% · {formatFileSize(item.rssBytes, BYTES_FALLBACK)} · {formatDuration(item.uptimeSeconds)}
      </p>
    </div>
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
      <DialogContent className="max-h-[calc(100dvh-2rem)] max-w-3xl overflow-y-auto">
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
                <ProcessField label={t('monitor.processAvgWrite', '平均写入')} value={`${formatFileSize(detail.history.avgWriteBytesPerSec, BYTES_FALLBACK)}/s`} mono />
                <ProcessField label={t('monitor.processRssDelta', 'RSS 变化')} value={formatSignedFileSize(detail.history.rssDeltaBytes)} mono />
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
              <Button type="button" variant="outline" disabled={actionPending} onClick={() => onAction('terminate')}>
                <AlertTriangle className="size-3.5" />
                {t('monitor.processTerminate', '温和终止')}
              </Button>
              <Button type="button" variant="destructive" disabled={actionPending} onClick={() => onAction('kill_tree')}>
                {actionPending && <Loader2 className="size-3.5 animate-spin" />}
                {t('monitor.processKillTree', '强制终止树')}
              </Button>
            </div>
            {/* 根进程同样可处置（卡死时需要清理），但要把「等于让实例停下」讲清楚。 */}
            {target.isRoot && <p className="text-xs text-amber-600">{t('nodes.processRootKillHint')}</p>}
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** 图表定义集与阈值线由外壳按 target 种类给出（它们在包内的 monitor-metrics 目录）。 */
export interface MonitoringPageProps {
  /** 当前下钻目标（视图只读，用于选图集与条件渲染；切换经 drillSlot 内的选择器）。 */
  target: DrillTarget
  /** 页级范围与粒度。 */
  range: MetricRange
  onRangeChange: (r: MetricRange) => void
  resolution: MetricResolution
  onResolutionChange: (r: MetricResolution) => void
  /** 多指标对比选中项。 */
  compareSel: string[]
  onCompareToggle: (metricKey: string) => void
  /** 概览/对比共享的原始序列。 */
  raw: RawSeries[]
  rawLoading?: boolean
  /** 概览/对比取数失败（403/404/5xx）。必须与「合法无数据」区分，否则权限与故障都被显示成空图。 */
  rawError?: boolean
  /** 进程 TOP。 */
  processTop: ProcessTopItem[]
  /** 进程榜取数失败（与「暂无采样」区分）。 */
  processTopError?: boolean
  /** 进程详情（弹窗内）。 */
  processDetail?: ManagedProcessDetail
  processDetailError?: boolean
  processDetailRefreshing?: boolean
  /** 处置动作在途。 */
  processActionPending?: boolean
  /** Bot 运行时不可用原因（node 层才显示）。 */
  botRuntimeReason?: string
  /** 当前选中的进程（null=弹窗关闭）。 */
  selectedProcess: ProcessTopItem | null
  onSelectedProcessChange: (p: ProcessTopItem | null) => void
  /** 待确认的处置动作。 */
  pendingAction: ManagedProcessAction | null
  onPendingActionChange: (a: ManagedProcessAction | null) => void
  /** 执行处置动作。 */
  onProcessAction: (payload: { instanceId: number; pid: number; action: ManagedProcessAction }) => Promise<void>
  /** 刷新进程详情。 */
  onRefreshProcessDetail: () => void
  /** 主图网格的序列 hook（外壳注入，它要按 target 取数）。 */
  useSeries: MonitorSeriesHook
  /** 确认弹窗的描述文案（由外壳按选中进程组装，含实例名等它才有的信息）。 */
  actionDescription: string
  /** 每个图表定义集（包内 monitor-metrics 目录，外壳按 target 种类给出）。 */
  chartDefs: MetricChartDef[]
  /** 全局排行面板插槽（外壳用其接线层渲染，它要自己取数）。 */
  rankingSlot: ReactNode
  /** 容量预测卡插槽（同上）。 */
  forecastSlot: ReactNode
  /** 可用性区块插槽（同上）。 */
  sloSlot: ReactNode
  /** 下钻选择器插槽（同上：它内嵌的实例选择器要外壳注入候选窗口）。 */
  drillSlot: ReactNode
}

/** 
 * 统一监控页（时序剖析增强）。在既有061 时序底座上加四个剖析维度：
 * 1. 关键指标概览——一屏看当前值 + sparkline 趋势缩略；
 * 2. 自定义聚合粒度 + 时间范围——页级 RangePicker + ResolutionPicker（auto/30s/5m/1h 三档）；
 * 3. 多指标对比/叠加——勾选多条指标在同一图叠加对比；
 * 4. 下钻——平台→节点→实例→世界 逐级钻取（面包屑 + 各层下拉）。
 * 仅纯前端消费既有 /metrics/series、/metrics/overview，不改后端、不另立 ADR。
 *
 * 受控视图：不取数——目标、范围、粒度、对比选中、序列与进程数据都由外壳注入。
 * 下钻面板、概览/对比、进程 TOP 与详情弹窗的呈现留在视图内。
 */
export default function MonitoringPage(props: MonitoringPageProps) {
  const {
    target,
    range,
    onRangeChange,
    resolution,
    onResolutionChange,
    compareSel,
    onCompareToggle,
    raw,
    rawLoading = false,
    rawError = false,
    processTop,
    processTopError = false,
    processDetail,
    processDetailError = false,
    processDetailRefreshing = false,
    processActionPending = false,
    botRuntimeReason,
    selectedProcess,
    onSelectedProcessChange,
    pendingAction,
    onPendingActionChange,
    onProcessAction,
    onRefreshProcessDetail,
    useSeries,
    actionDescription,
    chartDefs,
    rankingSlot,
    forecastSlot,
    sloSlot,
    drillSlot,
  } = props
  const { t } = useTranslation()
  /** 上一次处置是否被后端拒绝（在途/成功时复位）。 */
  const [actionFailed, setActionFailed] = useState(false)

  const worldFilter = target.kind === 'instance' ? target.world : undefined
  const source: MonitorSource =
    target.kind === 'platform' ? { kind: 'platform' } : { kind: target.kind, uuid: target.uuid }
  const tKey = `${target.kind}:${target.kind === 'platform' ? '' : target.uuid}`

  /** 
   * 处置确认：成功或失败都必须收起确认框。
   *
   * 原实现是 `void onProcessAction(...).then(() => onPendingActionChange(null))`——**没有 catch**。
   * 后端拒绝时（PID 归属校验失败 / 越权 / 进程已退出…）promise 落成未处理拒绝，
   * `pendingAction` 永远不为 null → 确认框卡在打开态，且本视图内没有任何失败反馈。
   * 这里补 catch：收起确认框 + 置失败标记，由下方 alert 条给出反馈。
   */
  const confirmAction = () => {
    if (!pendingAction || !selectedProcess) return
    setActionFailed(false)
    void onProcessAction({
      instanceId: selectedProcess.instanceId,
      pid: selectedProcess.pid,
      action: pendingAction,
    })
      .then(() => onPendingActionChange(null))
      .catch(() => {
        setActionFailed(true)
        onPendingActionChange(null)
      })
  }

  return (
    // 阶段 6 页面迁移：外壳与页头改用布局层原语（PageShell / PageHeader）。
    <PageShell data-page="monitoring">
      <PageHeader
        title={t('monitor.title')}
        description={t('monitor.subtitle')}
        actions={
          <>
            <ResolutionPicker value={resolution} onChange={onResolutionChange} />
            <RangePicker value={range} onChange={onRangeChange} />
          </>
        }
      />

      {/* 下钻：平台 → 节点 → 实例 → 世界 */}
      <Panel bodyClassName="px-3 py-2">
        {drillSlot}
      </Panel>

      {/* 关键指标概览：当前值 + sparkline 趋势缩略 */}
      <Panel title={t('monitor.overview.title')}>
        {rawError ? (
          <p className="px-2 py-3 text-xs text-muted-foreground">{t('monitor.overview.error', '关键指标加载失败')}</p>
        ) : (
          <MetricsOverviewStrip kind={target.kind} raw={raw} isLoading={rawLoading} />
        )}
      </Panel>

      {/* 多指标对比/叠加 */}
      <Panel title={t('monitor.compare.title')}>
        {rawError ? (
          <p className="px-2 py-3 text-xs text-muted-foreground">{t('monitor.compare.error', '指标加载失败')}</p>
        ) : (
          <MetricComparePanel kind={target.kind} raw={raw} selected={compareSel} onToggle={onCompareToggle} />
        )}
      </Panel>

      <ProcessTopPanel rows={processTop} onInspect={onSelectedProcessChange} isError={processTopError} />

      {/* 处置失败反馈：确认框已收起（见 confirmAction），失败原因在这里落地，
          否则用户只看到弹窗消失、进程还在，无从判断刚才那一下到底成没成。 */}
      {actionFailed && (
        <p role="alert" data-testid="process-action-error" className="text-sm text-destructive">
          {t('common.error')}
        </p>
      )}

      {/* 全局排行：跨节点全量实例排序 + 名次 + 下钻；非管理员只见可访问实例。 */}
      {rankingSlot}

      {/* 容量预测：仅在有明确目标时渲染（实例详情/节点详情语义下才有意义）。 */}
      {forecastSlot}

      {/* 可用性：平台级汇总；下钻到实例/节点时改为该目标口径。 */}
      {sloSlot}

      <ProcessDetailDialog
        open={!!selectedProcess}
        detail={processDetail}
        isError={processDetailError}
        actionPending={processActionPending}
        refreshing={processDetailRefreshing}
        onAction={onPendingActionChange}
        onRefresh={onRefreshProcessDetail}
        onOpenChange={(open) => {
          if (!open) {
            onSelectedProcessChange(null)
            onPendingActionChange(null)
          }
        }}
      />

      <DangerConfirm
        open={pendingAction !== null}
        title={pendingAction === 'kill_tree' ? t('monitor.processKillTreeConfirm', '强制终止进程树') : t('monitor.processTerminateConfirm', '终止受管子进程')}
        description={actionDescription}
        confirmLabel={pendingAction === 'kill_tree' ? t('monitor.processKillTree', '强制终止树') : t('monitor.processTerminate', '温和终止')}
        pending={processActionPending}
        onConfirm={confirmAction}
        onCancel={() => onPendingActionChange(null)}
      />

      {target.kind === 'node' && <BotRuntimePanel reason={botRuntimeReason} />}

      <Panel bodyClassName="px-3 py-2 text-meta text-muted-foreground">{t('monitor.hint')}</Panel>

      {/* 主图网格（每图独立时间筛选 + brush + hover），共享页级粒度与下钻世界聚焦 */}
      <MonitorSkeleton
        key={tKey}
        defs={chartDefs}
        source={source}
        defaultRange={range}
        resolution={resolution}
        worldFilter={worldFilter}
        useSeries={useSeries}
      />
    </PageShell>
  )
}
