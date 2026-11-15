import { useTranslation } from 'react-i18next'
import {
  ArrowRight,
  CircleHelp,
  Download,
  LoaderCircle,
  Play,
  RotateCw,
  Square,
  Upload,
  Wrench,
} from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import { RefreshButton } from '@jianmanager/ui/components/refresh-button'
import { Sparkline } from '@jianmanager/ui/charts/Sparkline'
import { formatFileSize } from '@/lib/shared/format-file-size'

/**
 * 日志平台数据流（HOT → COLD → Rehydrate）。
 *
 * 【为什么不是三行列表】改造前是三个外形完全相同的 `div` 竖排，每行一个状态徽章
 * 和「启动/停止/重启」三个按钮。但 HOT/COLD/Rehydrate 不是三个并列的开关——
 * 它们是一条**流水线**：日志先写 HOT，按 UTC 日迁到 COLD，需要时再从归档回灌 Rehydrate。
 * 「迁移分区」这个动作在语义上属于 HOT→COLD 这条**边**，不属于任何一层；
 * 画成列表就只能把它塞进底部的按钮堆里，于是不可逆的危险操作和日常启停挤在一排。
 *
 * 现在层之间留出通道，把动作挂到它真正所属的边上：
 *   采集 ──写入──▶ HOT ──迁移──▶ COLD ──回灌──▶ Rehydrate
 * 每层还带上容量读数（后端 log_vl_* 指标），因为「这层还放得下吗」是该层最重要的状态。
 *
 * 【受控视图】不取数、不发请求、不弹 toast；磁盘读数由外壳注入。
 * 移动端（<lg）退化为纵向堆叠，箭头转为向下——横向流水线在窄屏会被压成一团。
 */

/** 三个存储层（与 proto 命名空间对齐）。 */
export type LogNamespace = 'hot' | 'cold' | 'rehydrate'

/** 单层运行时视图。 */
export interface LogRuntimeLayerView {
  namespace: LogNamespace
  state?: string
  health_ok?: boolean
  query_ready?: boolean
  listen_addr?: string
  asset_tag?: string
  last_error?: string
  /** 该层磁盘已用字节（后端指标）。 */
  diskUsedBytes?: number | null
  /** 该层磁盘占用百分比（后端指标；算不出来为 null）。 */
  diskPct?: number | null
  /** 24h 占用趋势（sparkline）。 */
  diskTrend?: number[]
}

/** 日志平台整块视图。 */
export interface LogRuntimeFlowView {
  os: string
  arch: string
  online: boolean
  layers: LogRuntimeLayerView[]
  /** 审批资产是否已在本节点缓存。 */
  assetCached: boolean
  /** 审批版本号（可空）。 */
  assetTag?: string
  /** 资产 sha256（展示前 8 位）。 */
  assetSha?: string
  /** 归档层（回灌来源）状态：可取回的归档对象数。 */
  archiveObjects?: number | null
  /** 运行时/资产查询的错误（展示用）。 */
  error?: string | null
}

export interface LogRuntimeFlowProps {
  view: LogRuntimeFlowView
  busy?: boolean
  pending?: { upload?: boolean; install?: boolean; control?: boolean; migrate?: boolean; gaps?: boolean }
  fetching?: boolean
  canMigrate?: boolean
  onRefresh: () => void
  onUpload: (file: File) => void
  onInstall: () => void
  onControl: (ns: LogNamespace, action: 'start' | 'stop' | 'restart') => void
  /** 打开迁移分区（日期选择在对话框里做，因为它是不可逆操作）。 */
  onOpenMigrate: () => void
  onResolveGaps: () => void
  /** 回灌归档（把 COLD 归档对象恢复到可查面）。 */
  onRehydrate: () => void
}

const NAMESPACE_LABEL: Record<LogNamespace, string> = {
  hot: 'HOT',
  cold: 'COLD',
  rehydrate: 'Rehydrate',
}

/** 层状态 → 徽章色调。RUNNING 且健康才是绿；ERROR/空是中性。 */
function stateTone(layer: LogRuntimeLayerView | undefined, online: boolean): 'ok' | 'warn' | 'danger' | 'idle' {
  if (!online) return 'idle'
  if (!layer) return 'idle'
  if (layer.state === 'RUNNING') {
    if (layer.last_error) return 'warn'
    if (layer.health_ok === false) return 'warn'
    return 'ok'
  }
  if (layer.state && /FAIL|ERROR|EXIT/i.test(layer.state)) return 'danger'
  return 'idle'
}

const TONE_BADGE: Record<'ok' | 'warn' | 'danger' | 'idle', string> = {
  ok: 'border-status-success/40 text-status-success',
  warn: 'border-status-warning/40 text-status-warning',
  danger: 'border-status-destructive/40 text-status-danger',
  idle: 'text-muted-foreground',
}

/** 单层卡片。 */
function LayerCard({
  layer,
  online,
  busy,
  pending,
  onControl,
}: {
  layer: LogRuntimeLayerView
  online: boolean
  busy: boolean
  pending?: LogRuntimeFlowProps['pending']
  onControl: (ns: LogNamespace, action: 'start' | 'stop' | 'restart') => void
}) {
  const { t } = useTranslation()
  const running = layer.state === 'RUNNING'
  const tone = stateTone(layer, online)
  // pct 为 null 时不画条：给 0% 的条会被读成「磁盘全空」，而事实是「没有读数」。
  const hasPct = typeof layer.diskPct === 'number'
  const trend = (layer.diskTrend ?? []).filter((v) => Number.isFinite(v))

  return (
    <div
      className="flex min-w-0 flex-1 flex-col gap-1.5 rounded-lg border bg-background/60 p-2.5"
      data-testid={`log-layer-${layer.namespace}`}
    >
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="text-xs font-semibold">{NAMESPACE_LABEL[layer.namespace]}</span>
        <Badge variant="outline" className={cn('h-5 px-1.5 text-[10px]', TONE_BADGE[tone])} data-testid={`log-layer-state-${layer.namespace}`}>
          {layer.state ?? (online ? t('logsRuntime.namespace.notReady', '未就绪') : t('logsRuntime.namespace.offline', '离线'))}
        </Badge>
        {layer.health_ok && (
          <span className="text-[10px] text-status-success">{t('logsRuntime.health.ok', '进程健康')}</span>
        )}
        {layer.query_ready && (
          <span className="text-[10px] text-status-success">{t('logsRuntime.health.queryable', '可查询')}</span>
        )}
      </div>

      {/* 容量：有 pct 就画条（含阈值色），只有字节就报绝对值。 */}
      {hasPct || layer.diskUsedBytes ? (
        <div className="flex items-center gap-1.5">
          <MiniBar value={layer.diskPct ?? 0} className="h-1 flex-1" />
          <span className="shrink-0 text-[10px] text-muted-foreground tabular-nums">
            {layer.diskUsedBytes ? formatFileSize(layer.diskUsedBytes) : '--'}
            {hasPct ? ` · ${Math.round(layer.diskPct!)}%` : ''}
          </span>
        </div>
      ) : null}

      {trend.length > 1 && (
        // Sparkline 用固定 viewBox + 百分比铺满，高度由容器给（它自己不收 height）。
        <div className="h-[22px]" data-testid={`log-layer-trend-${layer.namespace}`}>
          <Sparkline points={trend.map((v) => ({ value: v }))} ariaLabel={`${NAMESPACE_LABEL[layer.namespace]} 磁盘占用趋势`} />
        </div>
      )}

      <div className="truncate font-mono text-[10px] text-muted-foreground" title={`${layer.listen_addr ?? '—'} · ${layer.asset_tag ?? '—'}`}>
        {layer.listen_addr ?? '—'}
      </div>
      {layer.last_error && (
        <p className="line-clamp-2 text-[10px] text-status-danger" title={layer.last_error}>
          {layer.last_error}
        </p>
      )}

      <div className="mt-auto flex items-center gap-1 pt-0.5">
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          aria-label={`${t('logsRuntime.action.start', '启动')} ${NAMESPACE_LABEL[layer.namespace]}`}
          title={`${t('logsRuntime.action.start', '启动')} ${NAMESPACE_LABEL[layer.namespace]}`}
          disabled={busy || !online || running}
          onClick={() => onControl(layer.namespace, 'start')}
        >
          <Play className="size-3.5" />
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          aria-label={`${t('logsRuntime.action.stop', '停止')} ${NAMESPACE_LABEL[layer.namespace]}`}
          title={`${t('logsRuntime.action.stop', '停止')} ${NAMESPACE_LABEL[layer.namespace]}`}
          disabled={busy || !online || !running}
          onClick={() => onControl(layer.namespace, 'stop')}
        >
          <Square className="size-3.5" />
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          aria-label={`${t('logsRuntime.action.restart', '重启')} ${NAMESPACE_LABEL[layer.namespace]}`}
          title={`${t('logsRuntime.action.restart', '重启')} ${NAMESPACE_LABEL[layer.namespace]}`}
          disabled={busy || !online || !running}
          onClick={() => onControl(layer.namespace, 'restart')}
        >
          <RotateCw className="size-3.5" />
        </Button>
        {pending?.control && <LoaderCircle className="size-3 animate-spin text-muted-foreground" aria-hidden />}
      </div>
    </div>
  )
}

/**
 * 层间的「边」：把动作放到它语义所属的位置。
 * 横向时是一条带箭头的通道，纵向时是一段竖直的连接线（由容器 class 控制方向）。
 */
function FlowEdge({
  label,
  action,
  hint,
  disabled,
  danger,
  onClick,
  testId,
}: {
  label: string
  action: string
  hint: string
  disabled?: boolean
  danger?: boolean
  onClick: () => void
  testId: string
}) {
  return (
    <div className="flex shrink-0 flex-row items-center justify-center gap-1 px-1 lg:flex-col lg:px-0">
      <ArrowRight className="hidden size-3.5 text-muted-foreground/60 lg:block" aria-hidden />
      <Button
        type="button"
        variant="outline"
        size="sm"
        className={cn('h-6 gap-1 whitespace-nowrap px-2 text-[10px]', danger && 'border-status-warning/50 text-status-warning')}
        disabled={disabled}
        title={hint}
        data-testid={testId}
        onClick={onClick}
      >
        {label}
      </Button>
      <span className="text-[10px] text-muted-foreground/70">{action}</span>
    </div>
  )
}

export function LogRuntimeFlow({
  view,
  busy = false,
  pending,
  fetching = false,
  canMigrate = true,
  onRefresh,
  onUpload,
  onInstall,
  onControl,
  onOpenMigrate,
  onResolveGaps,
  onRehydrate,
}: LogRuntimeFlowProps) {
  const { t } = useTranslation()
  const byNs = (ns: LogNamespace): LogRuntimeLayerView =>
    view.layers.find((l) => l.namespace === ns) ?? { namespace: ns }

  const hot = byNs('hot')
  const cold = byNs('cold')
  const rehydrate = byNs('rehydrate')
  const hotRunning = hot.state === 'RUNNING'
  const coldRunning = cold.state === 'RUNNING'

  const fileInputId = `log-asset-upload-${view.os}-${view.arch}`

  return (
    <section className="space-y-2" aria-label={t('logsRuntime.title', '日志平台运行时')} data-testid="log-runtime-flow">
      {/* 资产条：审批包是整块日志平台的**前置**，所以它单独一行、在流水线上游。 */}
      <div className="flex flex-wrap items-center gap-2 rounded-md border bg-muted/20 px-2.5 py-1.5">
        <Download className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
        <span className="text-[11px] text-muted-foreground">
          {view.os} / {view.arch}
        </span>
        {view.assetCached ? (
          <Badge variant="outline" className="h-5 border-status-success/40 px-1.5 text-[10px] text-status-success">
            {t('logsRuntime.approvalCached', '审批包已缓存')}
            {view.assetTag ? ` · ${view.assetTag}` : ''}
            {view.assetSha ? ` · ${view.assetSha.slice(0, 8)}` : ''}
          </Badge>
        ) : (
          <Badge variant="outline" className="h-5 border-status-warning/40 px-1.5 text-[10px] text-status-warning">
            {t('logsRuntime.approvalNotCached', '审批包未缓存')}
          </Badge>
        )}
        <div className="ml-auto flex items-center gap-1">
          <input
            id={fileInputId}
            type="file"
            accept=".gz,.zip"
            className="sr-only"
            aria-label={t('logsRuntime.asset.chooseFile', '选择 VictoriaLogs 压缩包')}
            onChange={(e) => {
              const file = e.currentTarget.files?.[0]
              // 先清空 value：否则重选同一个文件不会触发 change。
              e.currentTarget.value = ''
              if (file) onUpload(file)
            }}
          />
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="h-6 px-2 text-[10px]"
            disabled={busy}
            title={t('logsRuntime.asset.help', '上传官方压缩包作为审批包')}
            onClick={() => document.getElementById(fileInputId)?.click()}
          >
            {pending?.upload ? <LoaderCircle className="mr-1 size-3 animate-spin" /> : <Upload className="mr-1 size-3" />}
            {t('logsRuntime.asset.chooseFile', '上传审批包')}
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="h-6 px-2 text-[10px]"
            disabled={busy || !view.online || !view.assetCached}
            title={t('logsRuntime.asset.dispatchHelp', '把审批过的资产下发到该节点并校验哈希')}
            data-testid="log-asset-dispatch"
            onClick={onInstall}
          >
            {pending?.install ? <LoaderCircle className="mr-1 size-3 animate-spin" /> : <Download className="mr-1 size-3" />}
            {t('logsRuntime.asset.dispatch', '下发资产')}
          </Button>
          <RefreshButton variant="outline" size="sm" pending={fetching} onClick={onRefresh} title={t('common.refresh', '刷新')} />
        </div>
      </div>

      {view.error && (
        <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 px-2.5 py-1.5 text-[11px] text-destructive">
          {view.error}
        </p>
      )}

      {/* 流水线本体。窄屏纵向（箭头朝下），lg 起横向。 */}
      <div className="flex flex-col items-stretch gap-1 lg:flex-row lg:items-stretch">
        <LayerCard layer={hot} online={view.online} busy={busy} pending={pending} onControl={onControl} />

        {/* HOT → COLD 这条边：迁移是不可逆操作，按钮统一走确认对话框（日期在选择框里定）。 */}
        <div className="flex flex-col items-center justify-center gap-0.5 px-1">
          <FlowEdge
            label={t('logsRuntime.namespace.migrate', '迁移分区')}
            action="HOT → COLD"
            hint={canMigrate ? t('logsRuntime.namespace.migrateHelp', '把某个分区从 HOT 迁到 COLD') : t('logsRuntime.migration.denied', '仅平台管理员可用')}
            danger
            disabled={busy || !view.online || !hotRunning || !canMigrate}
            onClick={onOpenMigrate}
            testId="log-flow-migrate"
          />
          {/* 采集侧缺口核销：它处理的是「写入 HOT 时留下的缺口」，所以挂在入口处。 */}
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-5 gap-1 px-1 text-[10px] text-muted-foreground"
            disabled={busy || !view.online}
            title={t('logsRuntime.gaps.help', '采集中断留下的缺口，可投影证明完整后核销')}
            data-testid="log-flow-resolve-gaps"
            onClick={onResolveGaps}
          >
            {pending?.gaps ? <LoaderCircle className="size-3 animate-spin" /> : <Wrench className="size-3" />}
            {t('logsRuntime.gaps.resolve', '核销缺口')}
          </Button>
        </div>

        <LayerCard layer={cold} online={view.online} busy={busy} pending={pending} onControl={onControl} />

        {/* COLD → Rehydrate 这条边：回灌把归档对象恢复到可查面。 */}
        <div className="flex flex-col items-center justify-center gap-0.5 px-1">
          <FlowEdge
            label={t('logsRuntime.archive.rehydrate', '回灌')}
            action="COLD → Rehydrate"
            hint={t('logsRuntime.archive.help', '把归档对象读取并发布回可查询面')}
            disabled={busy || !view.online || !coldRunning}
            onClick={onRehydrate}
            testId="log-flow-rehydrate"
          />
          {typeof view.archiveObjects === 'number' && (
            <span className="text-[10px] text-muted-foreground tabular-nums">
              {t('logsRuntime.archive.objects', { count: view.archiveObjects, defaultValue: '{{count}} 个归档对象' })}
            </span>
          )}
        </div>

        <LayerCard layer={rehydrate} online={view.online} busy={busy} pending={pending} onControl={onControl} />
      </div>

      {/* 说明行：三层语义对新手不显然，给一句常驻解释（不是 hover 才有）。 */}
      <p className="flex items-start gap-1 text-[10px] leading-snug text-muted-foreground/80">
        <CircleHelp className="mt-px size-3 shrink-0" aria-hidden />
        <span>
          {t(
            'logsRuntime.namespace.pipeline',
            'HOT=近期热数据（查询最快）；COLD=迁出的历史数据（稍慢但仍可查）；Rehydrate=把归档回灌成可查副本的临时层。三层按配置启用，未启用显示「未就绪」。',
          )}
        </span>
      </p>
    </section>
  )
}
