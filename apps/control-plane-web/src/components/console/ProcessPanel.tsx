import { useTranslation } from 'react-i18next'
import { Cpu, Gauge, MemoryStick, Timer, Waypoints } from 'lucide-react'

import { Panel } from '@jianmanager/ui/components/panel'
import { cn } from '@jianmanager/ui'
import { useInstanceMetrics } from '@/api/metrics'

/**
 * 通用进程指标面板（FR-450 §2.3.1）——不写死产品名，供二进制/beacon 与 BC 复用。
 *
 * 数据来自节点侧进程采集通道（`/instances/:id/metrics` 的 CPU/内存/线程/运行时长档）：
 * - 有 JVM 堆语义（heapMaxMb>0）展示「已用/上限」；无 JVM 堆（beacon 等原生二进制）展示 RSS。
 * - 探针缺失时**不伪造 -1 占位**，缺测项显「—」，与 FR-447 三态语义一致。
 */
export function ProcessPanel({ instanceId }: { instanceId: number }) {
  const { t } = useTranslation()
  const { data: metrics } = useInstanceMetrics(instanceId, true)

  const cpu = metrics?.cpuPercent
  const mem = metrics?.memoryMb
  const heapMax = metrics?.heapMaxMb ?? 0
  const hasHeap = heapMax > 0
  const threads = metrics?.threads
  const uptime = metrics?.uptimeSeconds

  return (
    <Panel title={t('process.title')} data-testid="process-panel">
      <div className="grid grid-cols-2 gap-2 p-2 text-xs sm:grid-cols-4">
        <ProcessStat
          icon={Cpu}
          label={t('process.cpu')}
          value={cpu == null ? '—' : `${Math.round(cpu)}%`}
          tone={cpu != null && cpu > 85 ? 'warn' : undefined}
        />
        <ProcessStat
          icon={MemoryStick}
          label={t('process.memory')}
          value={mem == null ? '—' : hasHeap ? `${formatMb(mem)} / ${formatMb(heapMax)}` : `${formatMb(mem)} RSS`}
          tone={hasHeap && mem != null && mem / heapMax > 0.9 ? 'warn' : undefined}
        />
        <ProcessStat icon={Waypoints} label={t('process.threads')} value={threads == null ? '—' : String(threads)} />
        <ProcessStat icon={Timer} label={t('process.uptime')} value={formatUptime(uptime)} />
      </div>
    </Panel>
  )
}

function ProcessStat({
  icon: Icon,
  label,
  value,
  tone,
}: {
  icon: typeof Gauge
  label: string
  value: string
  tone?: 'warn'
}) {
  return (
    <div className="rounded-md border bg-card p-2">
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <Icon className="size-3.5 shrink-0" />
        <span>{label}</span>
      </div>
      <p className={cn('mt-1 font-mono text-sm font-semibold tabular-nums', tone === 'warn' && 'text-status-warning')}>{value}</p>
    </div>
  )
}

function formatMb(v: number): string {
  if (!Number.isFinite(v)) return '—'
  if (v >= 1024) return `${(v / 1024).toFixed(1)}G`
  return `${Math.round(v)}M`
}

/** 运行时长（秒）人性化：Xd Yh / Xh Ym / Xm / Xs；无值显 —。 */
function formatUptime(sec?: number): string {
  if (!sec || sec <= 0) return '—'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m`
  return `${Math.floor(sec)}s`
}
