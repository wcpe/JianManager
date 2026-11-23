import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Activity, ChevronDown, ChevronRight, Cpu, MemoryStick, Users, Zap } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import type { InstanceInfo } from '@/lib/instances/instance-types'
import type { InstanceStatusCounts } from '@/lib/instances/instance-summary'
import { resolveCapabilities } from '@/lib/instances/capabilities'
import { fmtBytes } from '@/lib/metrics/metrics-format'
import type { InstanceMetricSnapshot } from './InstanceRowMetrics'

/** 状态使用服务端全范围聚合；指标只汇总当前页已取得的采样，不额外逐服轮询。 */
export interface InstanceRuntimeOverviewProps {
  counts: InstanceStatusCounts
  instances: InstanceInfo[]
  metrics: ReadonlyMap<string, InstanceMetricSnapshot>
}

export function InstanceRuntimeOverview({ counts, instances, metrics }: InstanceRuntimeOverviewProps) {
  const { t } = useTranslation()
  const [collapsed, setCollapsed] = useState(false)
  const current = useMemo(() => {
    const running = instances.filter((inst) => inst.status === 'RUNNING')
    const backends = running.filter((inst) => !resolveCapabilities(inst).capabilities.includes('bcTopology'))
    const values = (targets: InstanceInfo[], pick: (value: InstanceMetricSnapshot) => number | null) => targets.flatMap((inst) => {
      const snapshot = metrics.get(inst.uuid)
      const value = snapshot ? pick(snapshot) : null
      return value != null && Number.isFinite(value) && value >= 0 ? [value] : []
    })
    const cpu = values(running, (s) => s.cpuPct)
    const memory = values(running, (s) => s.memUsedBytes)
    // 代理与后端可能观察到同一玩家，人数合计只计算后端，避免重复。
    const players = values(backends, (s) => s.players)
    const tps = values(backends, (s) => s.tps)
    const sum = (items: number[]) => items.reduce((total, value) => total + value, 0)
    return {
      cpu: cpu.length ? sum(cpu) / cpu.length : null,
      memory: memory.length ? sum(memory) : null,
      players: players.length ? sum(players) : null,
      tps: tps.length ? sum(tps) / tps.length : null,
      coverage: [cpu.length, memory.length, players.length, tps.length],
      targets: [running.length, running.length, backends.length, backends.length],
    }
  }, [instances, metrics])

  const segments = [
    { key: 'active', label: t('instances.overview.active', '活动中'), count: counts.running, color: 'var(--status-success)' },
    { key: 'stopped', label: t('instances.stopped'), count: counts.stopped, color: 'var(--muted-foreground)' },
    { key: 'crashed', label: t('instances.crashed'), count: counts.crashed, color: 'var(--status-danger)' },
    { key: 'other', label: t('instances.overview.other', '其他'), count: Math.max(0, counts.total - counts.running - counts.stopped - counts.crashed), color: 'var(--status-warning)' },
  ]
  const circumference = 2 * Math.PI * 34
  let offset = 0
  const arcs = segments.map((segment) => {
    const length = counts.total > 0 ? segment.count / counts.total * circumference : 0
    const arc = { ...segment, length, offset }
    offset += length
    return arc
  })
  const readings = [
    { key: 'cpu', label: t('instances.overview.cpu', '平均 CPU'), icon: Cpu, value: current.cpu == null ? '--' : `${current.cpu.toFixed(1)}%` },
    { key: 'memory', label: t('instances.overview.memory', '堆内存合计'), icon: MemoryStick, value: current.memory == null ? '--' : fmtBytes(current.memory) },
    { key: 'players', label: t('instances.overview.players', '后端在线人数'), icon: Users, value: current.players == null ? '--' : String(Math.round(current.players)) },
    { key: 'tps', label: t('instances.overview.tps', '平均 TPS'), icon: Zap, value: current.tps == null ? '--' : current.tps.toFixed(1) },
  ]
  const distributionLabel = t('instances.overview.distributionLabel', {
    total: counts.total, active: counts.running, stopped: counts.stopped, crashed: counts.crashed,
    defaultValue: '服务器状态分布：共 {{total}} 台，活动 {{active}} 台，停止 {{stopped}} 台，异常 {{crashed}} 台',
  })

  return (
    <section aria-label={t('instances.overview.title', '服务器运行概览')} className="shrink-0 rounded-lg border bg-card" data-testid="instances-runtime-overview">
      <div className="flex min-w-0 items-center justify-between gap-2 px-3 py-1.5">
        <Button variant="invisible" size="auto" aria-expanded={!collapsed} aria-controls="instances-overview-body" onClick={() => setCollapsed((value) => !value)} className="gap-1.5 rounded px-1 py-0.5 text-xs font-semibold">
          {collapsed ? <ChevronRight className="size-3.5" aria-hidden /> : <ChevronDown className="size-3.5" aria-hidden />}
          <Activity className="size-3.5 text-muted-foreground" aria-hidden />
          {t('instances.overview.title', '服务器运行概览')}
        </Button>
        <span className="text-right text-[11px] text-muted-foreground" title={t('instances.overview.scopeHint', '状态统计按所选节点、群组、环境和标签收敛，不受搜索词或状态筛选影响；指标仅含本页已采样服务器。')}>{t('instances.overview.scope', '状态：当前范围 · 指标：本页最近采样')}</span>
      </div>
      {!collapsed && (
        <div id="instances-overview-body" className="grid min-w-0 items-center gap-3 border-t border-border/60 px-3 py-2 lg:grid-cols-[minmax(240px,1fr)_minmax(0,2fr)]">
          <div className="flex min-w-0 items-center justify-center gap-3 sm:justify-start">
            <svg viewBox="0 0 96 96" className="size-24 shrink-0" role="img" aria-label={distributionLabel}>
              <circle cx="48" cy="48" r="34" fill="none" stroke="var(--muted)" strokeWidth="10" />
              <g transform="rotate(-90 48 48)">
                {arcs.filter((arc) => arc.count > 0).map((arc) => (
                  <circle key={arc.key} cx="48" cy="48" r="34" fill="none" stroke={arc.color} strokeWidth="10" strokeDasharray={`${arc.length} ${circumference}`} strokeDashoffset={-arc.offset} />
                ))}
              </g>
              <text x="48" y="47" textAnchor="middle" dominantBaseline="middle" className="fill-foreground text-xl font-semibold tabular-nums">{counts.total}</text>
              <text x="48" y="64" textAnchor="middle" className="fill-muted-foreground text-[10px]">{t('instances.overview.servers', '服务器')}</text>
            </svg>
            <div className="grid min-w-0 flex-1 grid-cols-2 gap-x-4 gap-y-2 text-xs">
              {segments.map((segment) => (
                <div key={segment.key} className="flex min-w-0 items-center gap-1.5">
                  <span className="size-2 shrink-0 rounded-full" style={{ backgroundColor: segment.color }} aria-hidden />
                  <span className="truncate text-muted-foreground">{segment.label}</span>
                  <strong className="ml-auto tabular-nums">{segment.count}</strong>
                </div>
              ))}
            </div>
          </div>
          <div className="min-w-0">
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              {readings.map((reading, index) => (
                <div key={reading.key} className="min-w-0 rounded-md bg-muted/40 px-2.5 py-2" data-testid={`instances-overview-${reading.key}`}>
                  <div className="flex items-center gap-1 text-[11px] text-muted-foreground"><reading.icon className="size-3 shrink-0" aria-hidden />{reading.label}</div>
                  <div className="my-1 text-lg font-semibold leading-tight tabular-nums">{reading.value}</div>
                  <div className="text-[10px] text-muted-foreground">{t('instances.overview.coverage', { covered: current.coverage[index], total: current.targets[index], defaultValue: '已采样 {{covered}}/{{total}} 台' })}</div>
                </div>
              ))}
            </div>
            <p className="mt-1 text-[10px] text-muted-foreground">{t('instances.overview.sampleHint', '本页运行服务器近 1 小时的末次采样；缺测不按 0 计，人数不重复计算代理。')}</p>
          </div>
        </div>
      )}
    </section>
  )
}
