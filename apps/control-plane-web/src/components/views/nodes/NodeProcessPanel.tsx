import { useTranslation } from 'react-i18next'
import {
  Activity,
  AlertTriangle,
  ArrowDown,
  ArrowUp,
  Cpu,
  GitBranch,
  HardDrive,
  Info,
  MemoryStick,
  Search,
  TerminalSquare,
} from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Badge } from '@jianmanager/ui/components/badge'
import { Input } from '@jianmanager/ui/components/input'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { Segments, Segment } from '@jianmanager/ui/components/layout'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import { Panel } from '@jianmanager/ui/components/panel'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { cn } from '@jianmanager/ui'
import { formatFileSize } from '@/lib/shared/format-file-size'
import type { ProcessTopItem } from '@/api/metrics'
import type { ManagedProcessAction, ManagedProcessDetail } from '@/lib/console/managed-process'

/** 进程清单的排序维度（与后端 `sort` 参数一致，后端只支持这三档）。 */
export type ProcessSort = 'cpu' | 'memory' | 'io'

/** 实例归属的最小信息（清单里用来把 PID 归到某台实例）。 */
export interface ProcessInstanceRef {
  id: number
  name: string
  status: string
  role: string
}

export interface NodeProcessPanelProps {
  /** 当前排序维度（容器持有：它决定取数）。 */
  sort: ProcessSort
  onSortChange: (sort: ProcessSort) => void
  /** 进程行（已按 sort 由后端排序）。 */
  rows: ProcessTopItem[]
  /** instanceId → 实例信息（补名称/状态/角色）。 */
  instances: Map<number, ProcessInstanceRef>
  isLoading?: boolean
  isError?: boolean
  /** 搜索词（本地过滤 PID / 进程名 / 实例名 / 命令行）。 */
  query: string
  onQueryChange: (q: string) => void
  /** 打开某进程的详情弹窗。 */
  onInspect: (row: ProcessTopItem) => void
  /** 终止该进程（容器负责二次确认与请求）。 */
  onTerminate: (row: ProcessTopItem) => void
  /** 强制终止整棵进程树。 */
  onKillTree: (row: ProcessTopItem) => void
  /** 正在处置的 PID（按钮转圈/禁用）。 */
  pendingPid?: number | null
}

/**
 * 节点级「进程」面板：受管实例的进程清单 + 进程级处置入口。
 *
 * 【为什么按进程而不是按实例列】实例级清单只回答「哪台挂了」，
 * 真正要处置的是「哪个 PID 在吃 CPU / 卡住不退」。这里以进程为主语，
 * 每行都带实例归属，处置按钮与该进程一一对应。
 *
 * 【数据来源】`/metrics/processes/top?nodeId=` 只覆盖**受管实例进程**（游戏服本体及包装器），
 * 不是任意系统进程；因此这里没有「系统 other 进程」这类行。
 */
export function NodeProcessPanel({
  sort,
  onSortChange,
  rows,
  instances,
  isLoading,
  isError,
  query,
  onQueryChange,
  onInspect,
  onTerminate,
  onKillTree,
  pendingPid,
}: NodeProcessPanelProps) {
  const { t } = useTranslation()

  const q = query.trim().toLowerCase()
  const filtered = q
    ? rows.filter((r) => {
        const inst = instances.get(r.instanceId)
        return [String(r.pid), r.name, inst?.name ?? '', r.commandSummary, r.user]
          .join(' ')
          .toLowerCase()
          .includes(q)
      })
    : rows

  return (
    <Panel
      title={t('nodes.processTitle')}
      actions={
        <div className="flex flex-wrap items-center gap-2">
          {/* 排序维度决定「CPU 高的在前」还是「内存高的在前」，与后端同一套枚举。 */}
          <Segments aria-label={t('nodes.processSort')}>
            {(['cpu', 'memory', 'io'] as const).map((k) => (
              <Segment key={k} active={sort === k} onClick={() => onSortChange(k)}>
                {k === 'cpu' ? t('nodes.processSortCpu') : k === 'memory' ? t('nodes.processSortMemory') : t('nodes.processSortIo')}
              </Segment>
            ))}
          </Segments>
          <div className="relative">
            <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => onQueryChange(e.target.value)}
              placeholder={t('nodes.processSearch')}
              aria-label={t('nodes.processSearch')}
              className="h-8 w-48 pl-7 text-xs"
            />
          </div>
        </div>
      }
    >
      {isError ? (
        <p role="alert" className="py-6 text-center text-sm text-destructive">
          {t('nodes.processLoadFailed')}
        </p>
      ) : isLoading ? (
        <div className="space-y-1.5">
          <Skeleton className="h-8 w-full" />
          <Skeleton className="h-8 w-full" />
          <Skeleton className="h-8 w-full" />
        </div>
      ) : filtered.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">
          {rows.length === 0 ? t('nodes.processEmpty') : t('nodes.processNoMatch')}
        </p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('nodes.processCol')}</TableHead>
              <TableHead>{t('nodes.processInstance')}</TableHead>
              <TableHead className="text-right">{t('monitor.processCpu')}</TableHead>
              <TableHead className="text-right">{t('monitor.processMemory')}</TableHead>
              <TableHead className="text-right">{t('monitor.processIo')}</TableHead>
              <TableHead>{t('monitor.processUser')}</TableHead>
              <TableHead className="text-right">{t('common.actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {filtered.map((row) => {
              const inst = instances.get(row.instanceId)
              const busy = pendingPid === row.pid
              return (
                /*
                  整行可点开详情：看到可疑进程的第一反应是「点进去看」，只留一个「详情」按钮
                  等于把最常见的动作藏起来。行内操作按钮各自 stopPropagation，不会误触。
                */
                <TableRow
                  key={`${row.instanceId}:${row.pid}`}
                  data-testid="node-process-row"
                  className="cursor-pointer"
                  onClick={() => onInspect(row)}
                >
                  <TableCell className="max-w-[22rem]">
                    <div className="flex items-baseline gap-1.5">
                      <span className="font-mono text-[11px] text-muted-foreground">{row.pid}</span>
                      <span className="font-medium text-foreground">{row.name}</span>
                    </div>
                    {/* 命令行摘要：分辨「同一个 java 起的哪个服」最直接的线索。 */}
                    <div className="truncate text-[10px] text-muted-foreground" title={row.commandSummary}>
                      {row.commandSummary || '--'}
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-1.5">
                      <span className="truncate text-xs text-foreground">
                        {inst?.name ?? t('nodes.processInstanceUnknown', { uuid: row.instanceUuid.slice(0, 8) })}
                      </span>
                      {inst && inst.role !== 'backend' && (
                        <Badge variant="outline" className="h-4 px-1 text-[10px] text-muted-foreground">
                          {t(`networks.role_${inst.role}`, { defaultValue: inst.role })}
                        </Badge>
                      )}
                    </div>
                    {inst && (
                      <div className="text-[10px] text-muted-foreground">
                        {t(`instances.${instStatusKey(inst.status)}`, { defaultValue: inst.status })}
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">{row.cpuPercent.toFixed(1)}%</TableCell>
                  <TableCell className="text-right tabular-nums">{formatFileSize(row.rssBytes)}</TableCell>
                  <TableCell className="text-right tabular-nums text-xs text-muted-foreground">
                    {formatFileSize(row.readBytesPerSec)}/s · {formatFileSize(row.writeBytesPerSec)}/s
                  </TableCell>
                  <TableCell className="font-mono text-[11px] text-muted-foreground">{row.user || '--'}</TableCell>
                  <TableCell className="text-right">
                    <div className="flex items-center justify-end gap-1" onClick={(e) => e.stopPropagation()}>
                      <Button variant="ghost" size="xs" onClick={() => onInspect(row)}>
                        {t('common.details')}
                      </Button>
                      <Button variant="outline" size="xs" disabled={busy} onClick={() => onTerminate(row)}>
                        {t('monitor.processTerminate')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        disabled={busy}
                        className="text-destructive"
                        onClick={() => onKillTree(row)}
                      >
                        {t('monitor.processKillTree')}
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      )}
    </Panel>
  )
}

/** 进程启动时长：秒 → `3d 4h` / `2h 7m` / `45s`。 */
function formatUptime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return '--'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m`
  return `${Math.round(seconds)}s`
}

/** 采样时间：ISO → `YYYY-MM-DD HH:mm:ss`（截断秒后面的精度，便于对日志）。 */
function formatTime(value: string): string {
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value || '--'
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

/** 实例状态 → i18n 键后缀（与实例页同一套文案，避免第三份中文）。 */
function instStatusKey(status: string): string {
  switch (status) {
    case 'RUNNING':
      return 'running'
    case 'STARTING':
      return 'starting'
    case 'STOPPING':
      return 'stopping'
    case 'CRASHED':
      return 'crashed'
    default:
      return 'stopped'
  }
}

/** 进程详情弹窗所需数据（容器用 `useManagedProcessDetail` 取回后注入）。 */
export interface NodeProcessDetailDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 详情（未就绪时为空）。 */
  detail?: ManagedProcessDetail
  isLoading?: boolean
  isError?: boolean
  /** 处置在途。 */
  pending?: boolean
  onAction: (action: ManagedProcessAction) => void
  /** 该进程所属实例名（详情里也有一份，这里用于标题兜底）。 */
  fallbackName?: string
}

/**
 * 进程详情：目标 + 祖先链 + 后代 + 诊断 + 窗口统计，并提供 PID 级处置。
 *
 * 与实例控制台的进程弹窗同源同语义（都用 `/metrics/processes/:pid/detail`）：
 * 根进程不允许 PID 级处置——那是实例级停止/重启该管的事，从进程这里杀根会绕过平台的生命周期。
 */
export function NodeProcessDetailDialog({
  open,
  onOpenChange,
  detail,
  isLoading,
  isError,
  pending,
  onAction,
  fallbackName,
}: NodeProcessDetailDialogProps) {
  const { t } = useTranslation()
  const target = detail?.target
  /*
    根进程也允许 PID 级处置：进程卡死时必须有手段清理。
    安全性由后端保证——根进程退出会被退出监听捕获，实例随即标记为 CRASHED 并按 autoRestart 处理。
    但处置根进程等于「让这台实例停下来」，确认框里必须把这一点说明白。
  */
  const isRoot = target?.isRoot ?? false

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          {/* 标题带图标 + 进程名：一打开就知道「点开的是哪个进程」，不用再扫字段。 */}
          <DialogTitle className="flex items-center gap-2">
            <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
              <TerminalSquare className="size-4" aria-hidden />
            </span>
            <span className="min-w-0">
              <span className="block truncate">
                {target?.name ?? '--'}
                {target && <span className="ml-1.5 font-mono text-xs font-normal text-muted-foreground">PID {target.pid}</span>}
              </span>
              <span className="block text-[11px] font-normal text-muted-foreground">
                {detail?.instance.name || fallbackName || '--'}
                {target?.isRoot && (
                  <Badge variant="outline" className="ml-1.5 h-4 px-1 text-[10px] text-muted-foreground">
                    {t('nodes.processRootBadge')}
                  </Badge>
                )}
              </span>
            </span>
          </DialogTitle>
          <DialogDescription>{t('monitor.processDetailHint')}</DialogDescription>
        </DialogHeader>

        {isError ? (
          <p className="py-6 text-center text-sm text-destructive">{t('monitor.processDetailError')}</p>
        ) : isLoading || !detail || !target ? (
          <div className="space-y-2 py-2">
            <Skeleton className="h-4 w-56" />
            <Skeleton className="h-4 w-72" />
            <Skeleton className="h-4 w-64" />
          </div>
        ) : (
          <div className="max-h-[65vh] space-y-3 overflow-auto pr-1">
            {/* 关键指标：CPU / 内存 / IO 加大加粗——排障先看这三个。 */}
            <div className="grid grid-cols-3 gap-2">
              <MetricTile
                icon={<Cpu className="size-3.5" aria-hidden />}
                label={t('monitor.processCpu')}
                value={`${target.cpuPercent.toFixed(1)}%`}
                tone={target.cpuPercent >= 80 ? 'danger' : target.cpuPercent >= 50 ? 'warning' : 'default'}
              />
              <MetricTile
                icon={<MemoryStick className="size-3.5" aria-hidden />}
                label={t('monitor.processMemory')}
                value={formatFileSize(target.rssBytes)}
              />
              <MetricTile
                icon={<HardDrive className="size-3.5" aria-hidden />}
                label={t('monitor.processIo')}
                value={`${formatFileSize(target.readBytesPerSec)}/s · ${formatFileSize(target.writeBytesPerSec)}/s`}
              />
            </div>

            <section className="rounded-md border bg-card/60 p-2.5">
              <SectionTitle icon={<Info className="size-3.5" aria-hidden />}>{t('nodes.processSectionInfo')}</SectionTitle>
              <dl className="mt-1.5 grid grid-cols-2 gap-x-4 gap-y-1.5 sm:grid-cols-3">
                <Field label={t('monitor.processUser')} value={target.user || '--'} mono />
                <Field label={t('nodes.processThreads')} value={String(target.threadCount)} />
                <Field label={t('nodes.processUptime')} value={formatUptime(target.uptimeSeconds)} />
                <Field label={t('nodes.processParentPid')} value={target.parentPid ? String(target.parentPid) : '--'} mono />
                <Field label={t('nodes.processNode')} value={detail.instance.nodeName || '--'} />
                <Field label={t('nodes.processInstanceUuid')} value={detail.instance.uuid || '--'} mono truncate />
              </dl>
              <div className="mt-2 border-t pt-1.5">
                <div className="text-[10px] text-muted-foreground">{t('monitor.processCommand')}</div>
                <code className="mt-0.5 block break-all font-mono text-[11px] text-foreground/90">
                  {target.commandSummary || '--'}
                </code>
              </div>
              {target.unavailableReason && (
                <div className="mt-1.5 text-[11px] text-status-warning">{target.unavailableReason}</div>
              )}
            </section>

            {detail.diagnostics.length > 0 && (
              <section>
                <SectionTitle icon={<AlertTriangle className="size-3.5" aria-hidden />}>
                  {t('monitor.processDiagnostics')}
                </SectionTitle>
                <ul className="mt-1 space-y-1">
                  {detail.diagnostics.map((d) => (
                    <li key={d.code} className="rounded-md border px-2 py-1.5">
                      <div className="flex items-baseline gap-1.5">
                        <Badge
                          variant="outline"
                          className={cn(
                            'h-4 px-1 text-[10px]',
                            d.severity === 'danger'
                              ? 'border-destructive/50 text-destructive'
                              : d.severity === 'warning'
                                ? 'border-status-warning/50 text-status-warning'
                                : 'text-muted-foreground',
                          )}
                        >
                          {d.severity}
                        </Badge>
                        <span className="text-xs font-semibold text-foreground">{d.title}</span>
                      </div>
                      <div className="mt-0.5 text-[11px] text-muted-foreground">{d.evidence}</div>
                      <div className="text-[11px] text-muted-foreground">{d.suggestion}</div>
                    </li>
                  ))}
                </ul>
              </section>
            )}

            {(detail.ancestors.length > 0 || detail.children.length > 0) && (
              <section>
                <SectionTitle icon={<GitBranch className="size-3.5" aria-hidden />}>{t('nodes.processTree')}</SectionTitle>
                <ul className="mt-1 space-y-1">
                  {detail.ancestors.map((a) => (
                    <li key={`a-${a.pid}`} className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
                      <ArrowUp className="size-3 shrink-0" aria-hidden />
                      <span className="font-mono">{a.pid}</span>
                      <span className="truncate font-medium text-foreground/80">{a.name}</span>
                      <span className="tabular-nums">{formatFileSize(a.rssBytes)}</span>
                    </li>
                  ))}
                  <li className="flex items-center gap-1.5 text-[11px]">
                    <span className="size-1.5 shrink-0 rounded-full bg-primary" aria-hidden />
                    <span className="font-mono text-foreground">{target.pid}</span>
                    <span className="font-bold text-foreground">{target.name}</span>
                    <span className="tabular-nums text-muted-foreground">{formatFileSize(target.rssBytes)}</span>
                  </li>
                  {detail.children.map((c) => (
                    <li key={`c-${c.pid}`} className="flex items-center gap-1.5 pl-3 text-[11px] text-muted-foreground">
                      <ArrowDown className="size-3 shrink-0" aria-hidden />
                      <span className="font-mono">{c.pid}</span>
                      <span className="truncate font-medium text-foreground/80">{c.name}</span>
                      <span className="tabular-nums">{c.cpuPercent.toFixed(1)}%</span>
                      <span className="tabular-nums">{formatFileSize(c.rssBytes)}</span>
                    </li>
                  ))}
                </ul>
              </section>
            )}

            <section>
              <SectionTitle icon={<Activity className="size-3.5" aria-hidden />}>{t('nodes.processWindowStats')}</SectionTitle>
              <dl className="mt-1 grid grid-cols-2 gap-x-4 gap-y-1.5 sm:grid-cols-4">
                <Field label={t('monitor.processAvgCpu')} value={`${detail.history.avgCpuPercent.toFixed(1)}%`} />
                <Field label={t('monitor.processAvgWrite')} value={`${formatFileSize(detail.history.avgWriteBytesPerSec)}/s`} />
                <Field
                  label={t('nodes.processRssDelta')}
                  value={`${detail.history.rssDeltaBytes >= 0 ? '+' : '-'}${formatFileSize(Math.abs(detail.history.rssDeltaBytes))}`}
                />
                <Field label={t('nodes.processSamples')} value={String(detail.history.sampleCount)} />
              </dl>
              <div className="mt-1.5 text-[10px] text-muted-foreground">
                {t('monitor.processSampledAt')} {formatTime(target.sampledAt)}
              </div>
            </section>

            <div className="flex flex-wrap items-center justify-end gap-2 border-t pt-2">
              <Button type="button" variant="outline" size="sm" disabled={pending} onClick={() => onAction('terminate')}>
                {t('monitor.processTerminate')}
              </Button>
              <Button type="button" variant="destructive" size="sm" disabled={pending} onClick={() => onAction('kill_tree')}>
                {t('monitor.processKillTree')}
              </Button>
            </div>
            {isRoot && (
              <p className="flex items-start gap-1.5 text-[11px] text-status-warning">
                <Info className="mt-0.5 size-3 shrink-0" aria-hidden />
                {t('nodes.processRootKillHint')}
              </p>
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** 分区标题：图标 + 文案，弹窗内四个区块共用同一节奏。 */
function SectionTitle({ icon, children }: { icon: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-1.5 text-[11px] font-semibold text-foreground">
      <span className="text-primary/70">{icon}</span>
      {children}
    </div>
  )
}

/** 关键指标块：大号加粗数值 + 标签。 */
function MetricTile({
  icon,
  label,
  value,
  tone = 'default',
}: {
  icon: React.ReactNode
  label: string
  value: string
  tone?: 'default' | 'warning' | 'danger'
}) {
  return (
    <div className="rounded-md border bg-muted/30 px-2.5 py-2">
      <div className="flex items-center gap-1 text-[10px] text-muted-foreground">
        {icon}
        {label}
      </div>
      <div
        className={cn(
          // 不 truncate：IO 是「读 · 写」两段，截断后只剩半句，反而看不出量级。
          'mt-0.5 break-words text-sm font-bold tabular-nums leading-tight',
          tone === 'danger' ? 'text-destructive' : tone === 'warning' ? 'text-status-warning' : 'text-foreground',
        )}
      >
        {value}
      </div>
    </div>
  )
}

/** 明细字段：标签弱化、值加粗（PID/UUID 用等宽）。 */
function Field({
  label,
  value,
  mono,
  truncate,
}: {
  label: string
  value: string
  mono?: boolean
  truncate?: boolean
}) {
  return (
    <div className="flex min-w-0 items-baseline gap-1.5 text-[11px]">
      <dt className="shrink-0 text-[10px] text-muted-foreground">{label}</dt>
      <dd
        className={cn('min-w-0 font-semibold text-foreground', mono && 'font-mono text-[11px]', truncate && 'truncate')}
        title={truncate ? value : undefined}
      >
        {value}
      </dd>
    </div>
  )
}
