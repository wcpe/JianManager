import { useTranslation } from 'react-i18next'
import {
  Activity,
  CircleHelp,
  Database,
  Download,
  HardDrive,
  LoaderCircle,
  Play,
  RotateCw,
  Square,
  Upload,
} from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import { RefreshButton } from '@jianmanager/ui/components/refresh-button'
import { Sparkline } from '@jianmanager/ui/charts/Sparkline'
import { formatFileSize } from '@/lib/shared/format-file-size'

/**
 * 日志平台运行时（节点页「日志平台」分段）。
 *
 * 【为什么是**一个**实例卡片，而不是 HOT/COLD/Rehydrate 三层流水线】
 * 2026-10-29 起冷热双层已废除：`vlsup` 只保留唯一的受管 VL 实例
 * （`NamespaceHot`，见 internal/worker/logs/vlsup/cmdline.go:16-32，
 *  `LogRuntimeStatus` 也只遍历那一个命名空间）。
 * 上一版按「三层流水线」画，是照旧语义做的——它会让用户以为有三套存储要分别起停，
 * 而实际上后端起停、健康、查询就绪全部只针对那一个实例。
 *
 * 现在：
 *  - 主体是**一个**运行时卡片：状态 / 监听地址 / 健康 / 可查询 / 磁盘容量 / 趋势；
 *  - 日常启停（启动·停止·重启）在卡片内；
 *  - 「人工应急动作」（归档回灌、缺口核销）收进独立的应急区，与日常启停分开，
 *    因为它们依赖部署是否装配了 planner/migrator，未装配时后端会明确拒绝。
 *
 * 【受控视图】不取数、不发请求、不弹 toast；磁盘读数由外壳注入。
 */

/** 受管 VL 实例（后端 LogRuntimeInstance 的前端视图）。 */
export interface LogRuntimeInstanceView {
  /** 命名空间。当前后端只返回 'hot'（冷热双层已废除），保留字段以适应后续变化。 */
  namespace: string
  state?: string
  health_ok?: boolean
  query_ready?: boolean
  partition_recovery_complete?: boolean
  listen_addr?: string
  asset_tag?: string
  last_error?: string
  pid?: number
  /** 数据目录占用字节（节点级 log_vl_disk_used）。 */
  diskUsedBytes?: number | null
  /** 占用百分比（算不出来为 null——不要用 0 冒充「空盘」）。 */
  diskPct?: number | null
  /** 存储上限字节（未配为 null）。 */
  diskLimitBytes?: number | null
  /** 占用趋势（sparkline，数值为字节或百分比，与 diskPct 同源）。 */
  diskTrend?: number[]
  /** 当前日分区数（log_vl_partitions）。 */
  partitions?: number | null
  /** 超保留期丢弃速率 rows/sec（log_vl_rows_dropped）。 */
  rowsDropped?: number | null
}

export interface LogRuntimeFlowView {
  os: string
  arch: string
  online: boolean
  /** 受管实例（通常 1 个）。空数组 = 尚未启用的部署。 */
  instances: LogRuntimeInstanceView[]
  /** 审批资产是否已在本节点缓存。 */
  assetCached: boolean
  assetTag?: string
  assetSha?: string
  /** 查询/资产错误。 */
  error?: string | null
}

export interface LogRuntimeFlowProps {
  view: LogRuntimeFlowView
  busy?: boolean
  pending?: { upload?: boolean; install?: boolean; control?: boolean }
  fetching?: boolean
  onRefresh: () => void
  onUpload: (file: File) => void
  onInstall: () => void
  onControl: (namespace: string, action: 'start' | 'stop' | 'restart') => void
}

/** 状态 → 徽章色调。RUNNING 且健康才是绿；ERROR/FAIL 是红；其余中性。 */
function stateTone(inst: LogRuntimeInstanceView | undefined, online: boolean): 'ok' | 'warn' | 'danger' | 'idle' {
  if (!online || !inst) return 'idle'
  if (inst.state === 'RUNNING') {
    if (inst.last_error || inst.health_ok === false) return 'warn'
    return 'ok'
  }
  if (inst.state && /FAIL|ERROR|EXIT/i.test(inst.state)) return 'danger'
  return 'idle'
}

const TONE_BADGE: Record<'ok' | 'warn' | 'danger' | 'idle', string> = {
  ok: 'border-status-success/40 text-status-success',
  warn: 'border-status-warning/40 text-status-warning',
  danger: 'border-status-destructive/40 text-status-danger',
  idle: 'text-muted-foreground',
}

/**
 * 单个受管 VL 实例的卡片。
 *
 * 【信息层级】用户对这块只问三件事，按顺序排：
 *  ① 在不在跑（状态 + 健康 + 可查询）；
 *  ② 还能放多少（磁盘条 + 已用/上限 + 趋势）；
 *  ③ 会不会丢数据（分区数 + 丢弃速率）——这是 VL 独有的风险信号，放最下一行。
 */
function InstanceCard({
  inst,
  online,
  busy,
  pending,
  onControl,
}: {
  inst: LogRuntimeInstanceView
  online: boolean
  busy: boolean
  pending?: LogRuntimeFlowProps['pending']
  onControl: (namespace: string, action: 'start' | 'stop' | 'restart') => void
}) {
  const { t } = useTranslation()
  const running = inst.state === 'RUNNING'
  const tone = stateTone(inst, online)
  // pct 为 null 时不画条：0% 的条会被读成「磁盘全空」，而事实是「没有读数」。
  const hasPct = typeof inst.diskPct === 'number'
  const trend = (inst.diskTrend ?? []).filter((v) => Number.isFinite(v))

  return (
    <div className="rounded-lg border bg-background/60" data-testid="log-runtime-instance">
      {/* 第一行：身份 + 状态 + 启停 */}
      <div className="flex flex-wrap items-center gap-2 border-b px-3 py-2">
        <Database className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <span className="text-sm font-semibold">{t('logsRuntime.instance.title', 'VictoriaLogs 实例')}</span>
        <Badge variant="outline" className={cn('h-5 px-1.5 text-[10px]', TONE_BADGE[tone])} data-testid="log-runtime-state">
          {inst.state ?? (online ? t('logsRuntime.namespace.notReady', '未就绪') : t('logsRuntime.namespace.offline', '离线'))}
        </Badge>
        {inst.health_ok && (
          <span className="text-[10px] text-status-success" data-testid="log-runtime-health">
            {t('logsRuntime.health.ok', '进程健康')}
          </span>
        )}
        {inst.query_ready && (
          <span className="text-[10px] text-status-success" data-testid="log-runtime-queryready">
            {t('logsRuntime.health.queryable', '可查询')}
          </span>
        )}
        {inst.state === 'RUNNING' && !inst.query_ready && (
          <span className="text-[10px] text-muted-foreground" title={t('logsRuntime.health.recovering', '进程健康但查询面尚未就绪（投影未发布）')}>
            {t('logsRuntime.health.recovering', '查询面就绪中')}
          </span>
        )}

        <div className="ml-auto flex items-center gap-1">
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            aria-label={t('logsRuntime.action.start', '启动')}
            title={t('logsRuntime.action.start', '启动')}
            disabled={busy || !online || running}
            data-testid="log-runtime-start"
            onClick={() => onControl(inst.namespace, 'start')}
          >
            <Play className="size-4" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            aria-label={t('logsRuntime.action.stop', '停止')}
            title={t('logsRuntime.action.stop', '停止')}
            disabled={busy || !online || !running}
            data-testid="log-runtime-stop"
            onClick={() => onControl(inst.namespace, 'stop')}
          >
            <Square className="size-4" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            aria-label={t('logsRuntime.action.restart', '重启')}
            title={t('logsRuntime.action.restart', '重启')}
            disabled={busy || !online || !running}
            data-testid="log-runtime-restart"
            onClick={() => onControl(inst.namespace, 'restart')}
          >
            <RotateCw className="size-4" />
          </Button>
          {pending?.control && <LoaderCircle className="size-3 animate-spin text-muted-foreground" aria-hidden />}
          {inst.pid ? (
            <span className="ml-1 font-mono text-[10px] text-muted-foreground/70">pid {inst.pid}</span>
          ) : null}
        </div>
      </div>

      {/* 第二行：容量 */}
      <div className="grid gap-2 px-3 py-2 sm:grid-cols-2">
        <div className="min-w-0">
          <div className="mb-1 flex items-baseline justify-between gap-2">
            <span className="flex items-center gap-1 text-[11px] font-medium text-muted-foreground">
              <HardDrive className="size-3" aria-hidden />
              {t('logsRuntime.storage.title', '数据目录')}
            </span>
            <span className="text-[11px] text-muted-foreground tabular-nums">
              {inst.diskUsedBytes != null ? formatFileSize(inst.diskUsedBytes) : '--'}
              {inst.diskLimitBytes ? ` / ${formatFileSize(inst.diskLimitBytes)}` : ''}
              {hasPct ? ` · ${Math.round(inst.diskPct!)}%` : ''}
            </span>
          </div>
          {hasPct ? (
            <MiniBar value={inst.diskPct!} className="h-1.5" />
          ) : (
            <p className="text-[10px] text-muted-foreground/70">
              {inst.diskUsedBytes
                ? t('logsRuntime.storage.noLimit', '未配置上限，仅显示占用绝对值')
                : t('logsRuntime.storage.noData', '暂无容量读数')}
            </p>
          )}
          {trend.length > 1 && (
            <div className="mt-1 h-[22px]" data-testid="log-runtime-trend">
              <Sparkline
                points={trend.map((v) => ({ value: v }))}
                ariaLabel={t('logsRuntime.storage.trend', '数据目录占用趋势')}
              />
            </div>
          )}
        </div>

        {/* 丢数据风险：VL 自身的两个信号。分区数 ≤2 时即使超上限也不会真丢，所以并列展示。 */}
        <div className="min-w-0">
          <div className="mb-1 flex items-center gap-1 text-[11px] font-medium text-muted-foreground">
            <Activity className="size-3" aria-hidden />
            {t('logsRuntime.risk.title', '数据完整性')}
          </div>
          <dl className="space-y-0.5 text-[11px]">
            <div className="flex items-baseline justify-between gap-2">
              <dt className="text-muted-foreground">{t('logsRuntime.risk.partitions', '日分区数')}</dt>
              <dd className="tabular-nums">{inst.partitions ?? '--'}</dd>
            </div>
            <div className="flex items-baseline justify-between gap-2">
              <dt className="text-muted-foreground">{t('logsRuntime.risk.dropped', '超保留期丢弃')}</dt>
              <dd
                className={cn(
                  'tabular-nums',
                  (inst.rowsDropped ?? 0) > 0 ? 'font-medium text-status-danger' : 'text-muted-foreground',
                )}
                title={t('logsRuntime.risk.droppedHint', '因超保留期被 VictoriaLogs 丢弃的行速率（rows/sec）')}
              >
                {inst.rowsDropped != null ? `${inst.rowsDropped.toFixed(2)} /s` : '--'}
              </dd>
            </div>
            <div className="flex items-baseline justify-between gap-2">
              <dt className="text-muted-foreground">{t('logsRuntime.risk.listen', '监听')}</dt>
              <dd className="truncate font-mono" title={inst.listen_addr}>
                {inst.listen_addr ?? '--'}
              </dd>
            </div>
          </dl>
        </div>
      </div>

      {inst.last_error && (
        <p className="border-t px-3 py-1.5 text-[11px] text-status-danger" title={inst.last_error}>
          {inst.last_error}
        </p>
      )}
    </div>
  )
}

export function LogRuntimeFlow({
  view,
  busy = false,
  pending,
  fetching = false,
  onRefresh,
  onUpload,
  onInstall,
  onControl,
}: LogRuntimeFlowProps) {
  const { t } = useTranslation()
  const fileInputId = `log-asset-upload-${view.os}-${view.arch}`
  const primary = view.instances[0]

  return (
    <section className="space-y-2" aria-label={t('logsRuntime.title', '日志平台运行时')} data-testid="log-runtime-flow">
      {/* 资产条：审批包是整块日志平台的**前置**，所以它单独一行、在运行时之上。 */}
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
            {pending?.upload
              ? t('logsRuntime.asset.uploading', '上传中…')
              : t('logsRuntime.asset.chooseFile', '上传审批包')}
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
            {pending?.install
              ? t('logsRuntime.asset.dispatching', '下发中…')
              : t('logsRuntime.asset.dispatch', '下发资产')}
          </Button>
          <RefreshButton variant="outline" size="sm" pending={fetching} onClick={onRefresh} title={t('common.refresh', '刷新')} />
        </div>
      </div>

      {view.error && (
        <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 px-2.5 py-1.5 text-[11px] text-destructive">
          {view.error}
        </p>
      )}

      {/* 主体：唯一的受管实例。未启用时给出可执行的下一步，而不是空表格。 */}
      {primary ? (
        <InstanceCard
          inst={primary}
          online={view.online}
          busy={busy}
          pending={pending}
          onControl={onControl}
        />
      ) : (
        <div className="rounded-lg border border-dashed bg-muted/20 px-3 py-6 text-center">
          <p className="text-sm text-muted-foreground">
            {t('logsRuntime.instance.notEnabled', '本节点尚未启用日志平台运行时')}
          </p>
          <p className="mt-1 text-[11px] text-muted-foreground/70">
            {view.assetCached
              ? t('logsRuntime.instance.dispatchFirst', '审批资产已缓存，可点上方「下发资产」后再启动实例')
              : t('logsRuntime.instance.uploadFirst', '需先上传审批包并下发资产，之后才能启动运行时')}
          </p>
        </div>
      )}


      {/* 一句常驻说明：冷热双层已废除这件事对用户不显然，写清楚避免误解。 */}
      <p className="flex items-start gap-1 text-[10px] leading-snug text-muted-foreground/80">
        <CircleHelp className="mt-px size-3 shrink-0" aria-hidden />
        <span>
          {t(
            'logsRuntime.instance.hint',
            '日志平台只运行一个 VictoriaLogs 实例，按 UTC 日分区保存近期日志并按保留期自动淘汰；不再区分冷热双层存储。',
          )}
        </span>
      </p>
    </section>
  )
}

export default LogRuntimeFlow
