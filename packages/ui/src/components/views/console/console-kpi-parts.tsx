import type { LucideIcon } from 'lucide-react'
import { cn } from '@jianmanager/ui'

/**
 * 服务器控制台总览的 KPI 卡与派生纯函数（FR-269）。
 * 从应用侧 `InstanceConsolePage` 拆出：KpiCard 为纯展示，其余为无副作用纯函数。
 */

/** 总览 KPI 卡：图标 + 标签 + 等宽值 + 进度条（危险态着色）。 */
export function KpiCard({ icon: Icon, label, value, sub, progress, danger }: { icon: LucideIcon; label: string; value: string; sub?: string; progress: number; danger?: boolean }) {
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

/** 数值格式化：无值/非数显 —，否则固定小数位。 */
export function formatNumber(value: number | undefined, digits: number) {
  if (value == null || Number.isNaN(value)) return '—'
  return value.toFixed(digits)
}

/** 运行时长（秒）人性化：Xd Yh / Xh Ym / Xm / Xs；无值显 —。 */
export function formatUptime(sec: number | undefined): string {
  if (!sec || sec <= 0) return '—'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m`
  return `${Math.floor(sec)}s`
}

/** 关注事项文案的翻译签名（够用即可，不引 i18next 全量类型）。 */
export type Translate = (key: string, opts?: Record<string, unknown>) => string

/** 由实例状态与指标派生「关注事项」清单（文案走 i18n）。 */
export function buildWatchItems({
  status,
  metrics,
  probeConnected,
  mcSemantics,
  t,
}: {
  status?: string
  metrics?: { tps: number; msptMillis: number; cpuPercent: number; probeAvailable: boolean }
  probeConnected?: boolean
  /** 是否 MC 世界语义（FR-448）：TPS/MSPT 与探针相关告警只对世界语义实例成立。 */
  mcSemantics: boolean
  t: Translate
}) {
  const items: string[] = []
  // 走 i18n（修硬编码中文）：这些文案会出现在英文界面的「动态与告警」时间线里。
  if (status === 'CRASHED') items.push(t('serverConsole.watch.crashed'))
  if (status === 'STARTING' || status === 'STOPPING') items.push(t('serverConsole.watch.transition'))
  // TPS/MSPT 与探针在线是世界语义专属（FR-448）：proxy/二进制/beacon 不该出现这些 MC 告警。
  if (mcSemantics && metrics?.probeAvailable && metrics.tps < 18) items.push(t('serverConsole.watch.tpsLow'))
  if (mcSemantics && metrics?.probeAvailable && metrics.msptMillis > 50) items.push(t('serverConsole.watch.msptHigh'))
  if (metrics?.cpuPercent != null && metrics.cpuPercent > 85) items.push(t('serverConsole.watch.cpuHigh'))
  if (mcSemantics && !probeConnected) items.push(t('serverConsole.watch.probeOffline'))
  return items
}
