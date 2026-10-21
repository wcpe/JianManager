/**
 * @file InstanceRowMetrics：实例列表行内的「实时指标」与「活动心跳」两个单元格（平铺表与分组树表共用）。
 *       纯展示件（ADR-102 b 范式）：不取数、不发请求——数据由容器（InstancesPage）按**可见窗口**
 *       批量取数后逐行注入；缺测一律以 `--` 呈现，不隐藏列也不造数。
 * @input InstanceMetricSnapshot（本文件导出，取数侧 useInstanceWindowMetrics 派生）、
 *        TableCell 原语、fmtBytes（lib/metrics/metrics-format，图表紧凑口径）、翻译上下文
 * @output InstanceMetricsCell、InstanceHeartbeatCell、InstanceMetricSnapshot、HEARTBEAT_SLOTS
 * @sync apps/control-plane-web/src/components/views/instances/useInstanceWindowMetrics.ts（快照派生同源）、
 *        InstanceRowView.tsx（逐行调用）、InstanceTableParts.tsx（表头两列）
 */
import { useTranslation } from 'react-i18next'
import { cn } from '@jianmanager/ui'
import { TableCell } from '@jianmanager/ui/components/table'
import { fmtBytes } from '@/lib/metrics/metrics-format'

/** 行内指标快照：全部由可见窗口的批量序列派生（useInstanceWindowMetrics）；缺测为 null。 */
export interface InstanceMetricSnapshot {
  /** 当前 TPS（inst_tps 末点；停服/缺测 null）。 */
  tps: number | null
  /** CPU 占用百分比（inst_cpu_pct 末点）。 */
  cpuPct: number | null
  /** 堆内存占用字节（inst_heap_used 末点）。 */
  memUsedBytes: number | null
  /** 在线玩家数（inst_players_online 末点）。 */
  players: number | null
  /**
   * 在线人数上限（inst_players_max 末点，FR-511）。
   *
   * 与 `players` 成对展示为「当前/最大」；上限缺测（代理未提供 maxplayers 等）为 null，
   * 此时只显示当前人数——不要用 0 兜底，那会读成「上限 0」。
   */
  playersMax: number | null
  /** 活动心跳：inst_tps 末段采样逐点三态（true=活跃 / false=停摆 / null=缺测）。 */
  heartbeat: (boolean | null)[]
}

/**
 * 心跳带格数：raw 档 30s 一采样 → 40 格 = 近 20 分钟。
 * 与取数侧切片同源（useInstanceWindowMetrics 按本常量截取末段）。
 */
export const HEARTBEAT_SLOTS = 40

/** 指标段：标签弱化 + 数值前景色；段间距由父容器 gap 控制。 */
function MetricValue({ label, value }: { label: string; value: string }) {
  return (
    <span className="inline-flex items-baseline gap-0.5">
      <span className="text-muted-foreground/70">{label}</span>
      <span className="text-foreground">{value}</span>
    </span>
  )
}

/** 缺测占位（与标签列 `--` 同款口径）。 */
function Dash() {
  return <span className="text-muted-foreground/60">--</span>
}

/**
 * 实时指标单元格：TPS / CPU / 内存 / 玩家 四段紧凑并排（`tabular-nums` 防数值跳动）。
 * 全部缺测（如已停止且无历史）时收敛为单个 `--`，不铺四个空段。
 */
export function InstanceMetricsCell({ snapshot }: { snapshot?: InstanceMetricSnapshot }) {
  const { t } = useTranslation()
  const empty =
    !snapshot ||
    (snapshot.tps == null && snapshot.cpuPct == null && snapshot.memUsedBytes == null && snapshot.players == null)
  return (
    <TableCell className="text-xs whitespace-nowrap tabular-nums">
      {empty ? (
        <Dash />
      ) : (
        <div className="flex items-baseline gap-2" data-testid="instance-metrics">
          <MetricValue label={t('instances.metrics.tps')} value={snapshot.tps == null ? '--' : snapshot.tps.toFixed(1)} />
          <MetricValue
            label={t('instances.metrics.cpu')}
            value={snapshot.cpuPct == null ? '--' : `${Math.round(snapshot.cpuPct)}%`}
          />
          <MetricValue
            label={t('instances.metrics.mem')}
            value={snapshot.memUsedBytes == null ? '--' : fmtBytes(snapshot.memUsedBytes)}
          />
          <MetricValue
            label={t('instances.metrics.players')}
            value={snapshot.players == null ? '--' : String(Math.round(snapshot.players))}
          />
        </div>
      )}
    </TableCell>
  )
}

/**
 * 活动心跳单元格：末段 inst_tps 采样逐格渲染（绿=活跃 / 红=停摆 / 灰=缺测）。
 * 色板沿用分组头健康色带的 status token（`bg-status-success` 等），不引入新色。
 * 读屏与悬停均以聚合文案（活跃/停摆/缺测 格数）表达，不逐格播报。
 */
export function InstanceHeartbeatCell({ snapshot }: { snapshot?: InstanceMetricSnapshot }) {
  const { t } = useTranslation()
  const beats = snapshot?.heartbeat ?? []
  if (beats.length === 0) {
    return (
      <TableCell className="text-xs">
        <Dash />
      </TableCell>
    )
  }
  const active = beats.filter((b) => b === true).length
  const down = beats.filter((b) => b === false).length
  const label = t('instances.metrics.heartbeatLabel', { active, down, unknown: beats.length - active - down })
  return (
    <TableCell>
      <span
        role="img"
        aria-label={label}
        title={label}
        data-testid="instance-heartbeat"
        className="flex h-3.5 items-stretch gap-px"
      >
        {beats.map((b, i) => (
          <span
            key={i}
            className={cn(
              'w-[2px] rounded-[1px]',
              b === true ? 'bg-status-success' : b === false ? 'bg-status-danger' : 'bg-muted-foreground/25',
            )}
          />
        ))}
      </span>
    </TableCell>
  )
}
