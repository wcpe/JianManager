import { Activity, useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { Activity as ActivityIcon, AlertTriangle, Boxes, Gauge, HardDrive, Layers, Loader2, Play, RotateCw, Square, TerminalSquare, Users, type LucideIcon } from 'lucide-react'

import { useInstance, useKillInstance, useRestartInstance, useStartInstance, useStopInstance, isProvisioningInstance } from '@/api/instances'
import DangerConfirm from '@/components/DangerConfirm'
import { useInstanceMetrics } from '@/api/metrics'
import { useLogs } from '@/api/logs'
import { useNodes } from '@/api/nodes'
import { useServerState } from '@/api/serverState'
import { Button } from '@jianmanager/ui/components/button'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { cn, instanceStatusLevel } from '@jianmanager/ui'
import { instanceStatusGlowClass } from '@/lib/instance-glow'
import type { CardType } from '@/lib/workspace-card'
import CrashDiagnosticsCard from './CrashDiagnosticsCard'
import WorkspaceCardBody from './WorkspaceCardBody'
import { recordRecentServer } from './server-selection'

type TabKey = 'overview' | 'terminal' | 'resource' | 'metrics' | 'players' | 'plugins' | 'backup' | 'business' | 'bot'

const TAB_CARD_TYPE: Partial<Record<TabKey, CardType>> = {
  terminal: 'terminal',
  resource: 'resource',
  metrics: 'metrics',
  plugins: 'plugins',
  business: 'business',
  bot: 'bot',
}

const TAB_KEYS: TabKey[] = ['overview', 'terminal', 'resource', 'metrics', 'players', 'plugins', 'backup', 'business', 'bot']

const TAB_LABEL_KEY: Record<TabKey, string> = {
  overview: 'serverConsole.overview',
  terminal: 'serverConsole.console',
  resource: 'serverConsole.filesConfig',
  metrics: 'serverConsole.metrics',
  players: 'serverConsole.players',
  plugins: 'serverConsole.plugins',
  backup: 'serverConsole.backupSchedule',
  business: 'serverConsole.business',
  bot: 'serverConsole.bot',
}

interface InstanceConsolePageProps {
  instanceId: number
}

function readActiveTab(searchParams: URLSearchParams): TabKey {
  const tab = searchParams.get('tab')
  return TAB_KEYS.includes(tab as TabKey) ? (tab as TabKey) : 'overview'
}

/**
 * 服务器统一控制台（FR-269）：固定分区的单服默认入口。
 * 页签 keep-alive（FR-295，ADR-067）：访问过的页签进入 mountedTabs 全部渲染，
 * 非活跃者包 `<Activity mode="hidden">`——DOM 与本地状态保留、effects 卸载
 * （TanStack Query 订阅随之暂停 → 隐藏页签自动停轮询），切回瞬时呈现。
 */
export default function InstanceConsolePage({ instanceId }: InstanceConsolePageProps) {
  const { t } = useTranslation()
  const [searchParams, setSearchParams] = useSearchParams()
  const activeTab = readActiveTab(searchParams)
  // 访问过即保活：渲染期把新激活页签并入集合（React 官方「渲染期间调整状态」模式）。
  const [mountedTabs, setMountedTabs] = useState<TabKey[]>([activeTab])
  if (!mountedTabs.includes(activeTab)) {
    setMountedTabs((prev) => (prev.includes(activeTab) ? prev : [...prev, activeTab]))
  }
  const { data: instance } = useInstance(instanceId)
  const { data: nodes = [] } = useNodes({ refetchInterval: 30_000 })
  const { data: metrics } = useInstanceMetrics(instanceId, true)
  const { data: serverState } = useServerState(instanceId, true, 15_000)
  const { data: logs } = useLogs({ source: 'instance', instanceId, page: 1, pageSize: 8 }, { refetchInterval: 10_000 })
  const restart = useRestartInstance()
  const stop = useStopInstance()
  const start = useStartInstance()
  const kill = useKillInstance()
  // 强杀走统一危险操作确认（FR-059），不直发请求。
  const [killConfirmOpen, setKillConfirmOpen] = useState(false)

  // FR-293：直接经路由/深链进入实例也计入「最近打开」（与选择器/侧栏常驻列同一存储）；
  // store 侧对内容未变的写入不广播，轮询刷新不会造成订阅方空转。
  useEffect(() => {
    if (instance) recordRecentServer(instance)
  }, [instance])

  const node = nodes.find((n) => n.id === instance?.nodeId)
  const online = serverState?.state?.server?.onlinePlayers ?? metrics?.onlinePlayers ?? 0
  const maxPlayers = serverState?.state?.server?.maxPlayers ?? 200
  const watchItems = useMemo(() => buildWatchItems({ status: instance?.status, metrics, probeConnected: serverState?.connected }), [instance?.status, metrics, serverState?.connected])

  if (!instance) {
    return <div className="rounded-lg border bg-card p-6 text-sm text-muted-foreground shadow-soft">{t('serverConsole.noInstance')}</div>
  }

  const canStart = instance.status === 'STOPPED' || instance.status === 'CRASHED'
  const canControl = instance.status === 'RUNNING' || instance.status === 'STARTING' || instance.status === 'STOPPING'
  // 搭建中硬性禁启（FR-331）：provision 未终态期间启动按钮禁用 + tooltip 引导看任务中心，
  // 与后端启动闸（FR-319 二轮②）同一信号源（statusReason「搭建中」），任务终态自然解禁。
  const provisioning = isProvisioningInstance(instance)
  // 失败原因横幅（FR-312）：只看 statusReason 非空、不看 status——Worker 心跳会把 CRASHED
  // 冲回 STOPPED，若以状态为前置条件横幅会随之消失；再次启动时 CP transition 清空 reason，
  // 横幅纯受查询数据驱动消失，不留本地状态。
  // 搭建中的 statusReason 是进行时状态而非失败（FR-331）：不落红色失败横幅，走下方琥珀状态横幅。
  const startFailReason = provisioning ? undefined : instance.statusReason?.trim()
  const setActiveTab = (tab: TabKey) => {
    const next = new URLSearchParams(searchParams)
    if (tab === 'overview') next.delete('tab')
    else next.set('tab', tab)
    setSearchParams(next)
  }

  return (
    <div data-page="instance-console" className="jm-page-stack min-h-full w-full text-[13px] text-foreground">
      <div className="w-full space-y-3">
        {startFailReason && (
          <div
            role="alert"
            className="flex items-start gap-2 rounded-md border border-status-danger/40 bg-status-danger/10 px-3 py-2 text-xs text-status-danger"
          >
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
            <div className="min-w-0">
              <p className="font-semibold">{t('serverConsole.lastStartFailed')}</p>
              {/* 原因全文可读：不 truncate / line-clamp，长错误换行展示。 */}
              <p className="mt-0.5 whitespace-pre-wrap break-words">{startFailReason}</p>
            </div>
          </div>
        )}
        {/* 搭建中状态横幅（FR-331）：琥珀而非红（是进行时不是失败），随 provision 任务终态清 reason 自动消失。 */}
        {provisioning && (
          <div
            role="status"
            className="flex items-start gap-2 rounded-md border border-status-warning/40 bg-status-warning/10 px-3 py-2 text-xs text-status-warning"
          >
            <Loader2 className="mt-0.5 size-3.5 shrink-0 animate-spin" />
            <div className="min-w-0">
              <p className="font-semibold">{t('instances.provisioningTitle')}</p>
              <p className="mt-0.5 whitespace-pre-wrap break-words">{instance.statusReason}</p>
              <Link to="/tasks" className="mt-0.5 inline-block font-medium underline underline-offset-2">
                {t('instances.provisioningGoTasks')}
              </Link>
            </div>
          </div>
        )}
        <header className={cn('rounded-lg border bg-card/95 p-3 shadow-soft backdrop-blur-sm', instanceStatusGlowClass(instance.status))}>
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <StatusBadge
                  level={instanceStatusLevel(instance.status)}
                  label={t(`instances.${instance.status.toLowerCase()}`, instance.status)}
                  pulse={instance.status === 'STARTING' || instance.status === 'STOPPING'}
                />
                <h1 className="truncate text-lg font-semibold tracking-tight">{t('serverConsole.title')} / {instance.name}</h1>
              </div>
              <p className="mt-1 text-xs text-muted-foreground">{t('serverConsole.subtitle')}</p>
            </div>
            <div className="flex flex-wrap items-center gap-1.5">
              {canStart && (
                // 禁用按钮带 disabled:pointer-events-none，tooltip 由外层 span 承载（FR-331）。
                <span title={provisioning ? t('instances.provisioningBlocked') : undefined}>
                  <Button size="sm" disabled={provisioning} onClick={() => start.mutate(instance.id)}>
                    <Play className="size-3.5" />
                    {t('instances.start')}
                  </Button>
                </span>
              )}
              <Button size="sm" variant="outline" disabled={!canControl} onClick={() => restart.mutate(instance.id)}>
                <RotateCw className="size-3.5" />
                {t('serverConsole.restart')}
              </Button>
              <Button size="sm" variant="outline" disabled={!canControl} onClick={() => stop.mutate(instance.id)}>
                <Square className="size-3.5" />
                {t('serverConsole.stop')}
              </Button>
              <Button size="sm" variant="destructive" disabled={!canControl} onClick={() => setKillConfirmOpen(true)}>
                <AlertTriangle className="size-3.5" />
                {t('serverConsole.kill')}
              </Button>
              <Button size="sm" variant="outline" onClick={() => setActiveTab('terminal')}>
                <TerminalSquare className="size-3.5" />
                {t('serverConsole.openTerminal')}
              </Button>
            </div>
          </div>

          <div className="mt-3 grid gap-2 md:grid-cols-3 xl:grid-cols-6">
            <MetaCell label={t('serverConsole.node')} value={node?.name ?? t('console.unknownNode', { id: instance.nodeId })} />
            <MetaCell label={t('serverConsole.port')} value={`:${instance.serverPort || '—'}`} mono />
            <MetaCell label={t('serverConsole.online')} value={`${online}/${maxPlayers}`} mono />
            <MetaCell label={t('serverConsole.tps')} value={formatNumber(metrics?.tps, 1)} mono tone={metrics?.tps != null && metrics.tps < 18 ? 'warn' : 'ok'} />
            <MetaCell label={t('serverConsole.mspt')} value={`${formatNumber(metrics?.msptMillis, 0)}ms`} mono tone={metrics?.msptMillis != null && metrics.msptMillis > 50 ? 'danger' : 'ok'} />
            <MetaCell label="UUID" value={instance.uuid.slice(0, 8)} mono />
          </div>
        </header>

        <nav className="flex gap-1 overflow-x-auto rounded-lg border bg-card/95 px-2 pt-2 shadow-soft backdrop-blur-sm">
          {TAB_KEYS.map((key) => (
            <button
              key={key}
              type="button"
              onClick={() => setActiveTab(key)}
              aria-pressed={activeTab === key}
              className={cn(
                'shrink-0 border-b-2 px-3 py-2 text-xs font-medium transition-colors',
                activeTab === key
                  ? 'border-primary text-primary'
                  : 'border-transparent text-muted-foreground hover:text-foreground',
              )}
            >
              {t(TAB_LABEL_KEY[key])}
            </button>
          ))}
        </nav>

        {/* 页签 keep-alive（FR-295）：访问过的页签全部保持挂载，非活跃者 Activity 隐藏——
            DOM/本地状态（终端缓冲、文件树展开态、未保存草稿）保留，轮询自动暂停。 */}
        {mountedTabs.map((tab) => (
          <Activity key={tab} mode={tab === activeTab ? 'visible' : 'hidden'}>
            {tab === 'overview' ? (
              <OverviewPanel
                instanceId={instance.id}
                metrics={metrics}
                online={online}
                maxPlayers={maxPlayers}
                nodeDiskUsage={node?.diskUsage}
                logs={logs?.items ?? []}
                watchItems={watchItems}
                probeConnected={serverState?.connected ?? false}
              />
            ) : TAB_CARD_TYPE[tab] ? (
              <div className="min-h-[520px] rounded-lg border bg-card shadow-soft">
                {/* persistTerminal：终端连接由管理器常驻，页签隐藏/切换不断 WS（FR-295）。 */}
                <WorkspaceCardBody instanceId={instance.id} type={TAB_CARD_TYPE[tab]!} persistTerminal />
              </div>
            ) : (
              <PlaceholderPanel tab={tab} />
            )}
          </Activity>
        ))}
      </div>

      {/* 强杀二次确认（FR-059）：与实例列表页同款 DangerConfirm，组管理员及以上可确认。 */}
      <DangerConfirm
        open={killConfirmOpen}
        title={t('danger.killInstanceTitle', { name: instance.name })}
        description={t('danger.killInstanceDesc')}
        confirmLabel={t('instances.kill')}
        scope="group"
        onConfirm={() => { kill.mutate(instance.id); setKillConfirmOpen(false) }}
        onCancel={() => setKillConfirmOpen(false)}
      />
    </div>
  )
}

function MetaCell({ label, value, mono, tone }: { label: string; value: string; mono?: boolean; tone?: 'ok' | 'warn' | 'danger' }) {
  return (
    <div className="rounded-md border bg-muted/70 px-2 py-1.5">
      <p className="text-[11px] text-muted-foreground">{label}</p>
      <p className={cn('mt-0.5 truncate font-medium', mono && 'font-mono tabular-nums', tone === 'ok' && 'text-status-success', tone === 'warn' && 'text-status-warning', tone === 'danger' && 'text-status-danger')}>{value}</p>
    </div>
  )
}

function OverviewPanel({
  instanceId,
  metrics,
  online,
  maxPlayers,
  nodeDiskUsage,
  logs,
  watchItems,
  probeConnected,
}: {
  instanceId: number
  metrics?: { tps: number; msptMillis: number; memoryMb: number; heapMaxMb: number; cpuPercent: number; onlinePlayers: number; probeAvailable: boolean }
  online: number
  maxPlayers: number
  nodeDiskUsage?: number
  logs: Array<{ id: number; level: string; message: string; time: string }>
  watchItems: string[]
  probeConnected: boolean
}) {
  const { t } = useTranslation()
  const memoryPct = metrics?.heapMaxMb ? Math.min(100, Math.round((metrics.memoryMb / metrics.heapMaxMb) * 100)) : 0
  const cpuPct = Math.round(metrics?.cpuPercent ?? 0)
  const diskPct = Math.round(nodeDiskUsage ?? 0)
  const alertCount = watchItems.length
  const spark = buildSparkValues(metrics?.tps ?? 19.8, metrics?.msptMillis ?? 35)

  return (
    <div className="space-y-3">
      <div className="grid gap-2 md:grid-cols-3 xl:grid-cols-6">
        <KpiCard icon={Gauge} label={t('serverConsole.cpu')} value={`${cpuPct}%`} progress={cpuPct} />
        <KpiCard icon={HardDrive} label={t('serverConsole.memory')} value={`${memoryPct}%`} sub={`${formatNumber(metrics?.memoryMb, 0)} / ${formatNumber(metrics?.heapMaxMb, 0)} MB`} progress={memoryPct} />
        <KpiCard icon={ActivityIcon} label={t('serverConsole.tps')} value={formatNumber(metrics?.tps, 1)} progress={Math.min(100, ((metrics?.tps ?? 0) / 20) * 100)} />
        <KpiCard icon={Users} label={t('serverConsole.online')} value={`${online}/${maxPlayers}`} progress={maxPlayers > 0 ? (online / maxPlayers) * 100 : 0} />
        <KpiCard icon={Layers} label={t('serverConsole.disk')} value={`${diskPct}%`} progress={diskPct} />
        <KpiCard icon={AlertTriangle} label={t('serverConsole.alerts')} value={String(alertCount)} danger={alertCount > 0} progress={alertCount > 0 ? 100 : 0} />
      </div>

      {!probeConnected && (
        <div className="rounded-md border border-status-warning/40 bg-status-warning/10 px-3 py-2 text-xs text-status-warning">
          {t('serverConsole.probeUnavailable')}
        </div>
      )}

      <div className="grid gap-3 xl:grid-cols-[1.2fr_1fr_0.9fr]">
        <section className="rounded-lg border bg-card p-3 shadow-soft">
          <div className="mb-2 flex items-center justify-between">
            <h2 className="text-sm font-semibold">TPS / MSPT</h2>
            <span className="font-mono text-[11px] text-muted-foreground">mock-api</span>
          </div>
          <div className="grid h-44 grid-cols-24 items-end gap-1 border border-dashed bg-muted/70 p-2">
            {spark.map((v, i) => (
              <span key={i} className="rounded-t-sm bg-primary/75" style={{ height: `${v}%` }} />
            ))}
          </div>
        </section>

        <section className="rounded-lg border bg-card p-3 shadow-soft">
          <h2 className="mb-2 text-sm font-semibold">{t('serverConsole.recentEvents')}</h2>
          <div className="space-y-1.5">
            {logs.slice(0, 5).map((log) => (
              <div key={log.id} className="flex items-start gap-2 rounded-md border bg-muted/70 px-2 py-1.5 text-xs">
                <span className={cn('mt-1 size-1.5 shrink-0 rounded-full', log.level === 'error' ? 'bg-status-danger' : log.level === 'warn' ? 'bg-status-warning' : 'bg-status-info')} />
                <div className="min-w-0 flex-1">
                  <p className="truncate">{log.message}</p>
                  <p className="font-mono text-[10px] text-muted-foreground">{new Date(log.time).toLocaleTimeString()}</p>
                </div>
              </div>
            ))}
            {logs.length === 0 && <p className="text-xs text-muted-foreground">{t('serverConsole.noLogs')}</p>}
          </div>
        </section>

        <section className="rounded-lg border bg-card p-3 shadow-soft">
          <h2 className="mb-2 text-sm font-semibold">{t('serverConsole.watchItems')}</h2>
          <div className="space-y-1.5">
            {watchItems.map((item) => (
              <div key={item} className="flex items-center gap-2 rounded-md border bg-muted/70 px-2 py-1.5 text-xs">
                <AlertTriangle className="size-3.5 text-status-warning" />
                <span>{item}</span>
              </div>
            ))}
            {watchItems.length === 0 && (
              <div className="flex items-center gap-2 rounded-sm border border-status-success/35 bg-status-success/10 px-2 py-1.5 text-xs text-status-success">
                <span className="size-1.5 rounded-full bg-status-success" />
                <span>运行状态正常</span>
              </div>
            )}
          </div>
        </section>
      </div>

      <section className="rounded-lg border bg-card p-3 shadow-soft">
        <div className="mb-2 flex items-center justify-between">
          <h2 className="text-sm font-semibold">{t('serverConsole.logsPreview')}</h2>
          <span className="text-[11px] text-muted-foreground">tail -n 8</span>
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-32">{t('serverConsole.logTime')}</TableHead>
              <TableHead className="w-20">{t('serverConsole.logLevel')}</TableHead>
              <TableHead>{t('serverConsole.logMessage')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {logs.slice(0, 8).map((log) => (
              <TableRow key={log.id}>
                <TableCell className="font-mono text-xs text-muted-foreground">{new Date(log.time).toLocaleTimeString()}</TableCell>
                <TableCell className="font-mono text-xs uppercase">{log.level}</TableCell>
                <TableCell className="max-w-0 truncate">{log.message}</TableCell>
              </TableRow>
            ))}
            {logs.length === 0 && (
              <TableRow>
                <TableCell colSpan={3} className="text-center text-muted-foreground">{t('serverConsole.noLogs')}</TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </section>

      {/* 崩溃诊断（FR-313）：与失败横幅（FR-312）互补——横幅一句话，此卡看现场。 */}
      <CrashDiagnosticsCard instanceId={instanceId} />
    </div>
  )
}

function KpiCard({ icon: Icon, label, value, sub, progress, danger }: { icon: LucideIcon; label: string; value: string; sub?: string; progress: number; danger?: boolean }) {
  return (
    <div className="rounded-lg border bg-card p-2 shadow-soft">
      <div className="flex items-center justify-between gap-2">
        <div className="min-w-0">
          <p className="text-[11px] text-muted-foreground">{label}</p>
          <p className={cn('mt-0.5 font-mono text-lg font-semibold tabular-nums', danger && 'text-status-danger')}>{value}</p>
          {sub && <p className="truncate text-[10px] text-muted-foreground">{sub}</p>}
        </div>
        <Icon className={cn('size-4 shrink-0', danger ? 'text-status-danger' : 'text-primary')} />
      </div>
      <div className="mt-2 h-1.5 overflow-hidden rounded-sm bg-muted">
        <div className={cn('h-full rounded-sm', danger ? 'bg-status-danger' : progress > 80 ? 'bg-status-warning' : 'bg-primary')} style={{ width: `${Math.max(4, Math.min(100, progress))}%` }} />
      </div>
    </div>
  )
}

function PlaceholderPanel({ tab }: { tab: TabKey }) {
  const { t } = useTranslation()
  return (
    <div className="rounded-lg border bg-card p-6 shadow-soft">
      <div className="flex items-center gap-2 text-sm font-semibold">
        {tab === 'players' ? <Users className="size-4 text-primary" /> : <Boxes className="size-4 text-primary" />}
        {t(TAB_LABEL_KEY[tab])}
      </div>
      <p className="mt-2 text-sm text-muted-foreground">{t('serverConsole.placeholderTitle')}</p>
      <p className="mt-1 text-xs text-muted-foreground">{t('serverConsole.placeholderHint')}</p>
    </div>
  )
}

function formatNumber(value: number | undefined, digits: number) {
  if (value == null || Number.isNaN(value)) return '—'
  return value.toFixed(digits)
}

function buildSparkValues(tps: number, mspt: number) {
  return Array.from({ length: 24 }, (_, i) => {
    const wave = Math.sin(i / 2.2) * 12 + Math.cos(i / 3.7) * 8
    const base = Math.max(24, Math.min(92, tps * 4 + wave - Math.max(0, mspt - 45) / 2))
    return Math.round(base)
  })
}

function buildWatchItems({
  status,
  metrics,
  probeConnected,
}: {
  status?: string
  metrics?: { tps: number; msptMillis: number; cpuPercent: number; probeAvailable: boolean }
  probeConnected?: boolean
}) {
  const items: string[] = []
  if (status === 'CRASHED') items.push('服务器处于崩溃状态，请检查启动日志')
  if (status === 'STARTING' || status === 'STOPPING') items.push('服务器处于过渡态，操作按钮已收敛')
  if (metrics?.tps != null && metrics.tps < 18) items.push('TPS 低于 18，建议检查插件或实体数量')
  if (metrics?.msptMillis != null && metrics.msptMillis > 50) items.push('MSPT 超过 50ms，主线程可能卡顿')
  if (metrics?.cpuPercent != null && metrics.cpuPercent > 85) items.push('CPU 使用率偏高')
  if (!probeConnected) items.push('ServerProbe 未连接，部分运行态数据不可用')
  return items
}
