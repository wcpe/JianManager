import { usePlayerTrend } from '@/api/metrics'
import { PlayerTrendCard as PlayerTrendCardView } from '@/components/views/instances/PlayerTrendCard'
import type { MetricRange } from '@jianmanager/ui'

// 纯展示子组件原样再导出。
export { HourlyBars } from '@/components/views/instances/PlayerTrendCard'

/**
 * 玩家在线趋势卡的应用接线层（ADR-097 a 范式）。
 *
 * 卡片本体已迁入组件库并受控；本层读浏览器时区并接取数（时区是查询入参，只有这里能拿）。
 * 保留同路径的导出与同一套 props，调用点无需改动。
 */
export function PlayerTrendCard({ range }: { range: MetricRange }) {
  const tz = typeof Intl !== 'undefined' ? Intl.DateTimeFormat().resolvedOptions().timeZone : undefined
  const { data, isError, isLoading } = usePlayerTrend({ range, tz })

  return <PlayerTrendCardView range={range} data={data} loading={isLoading} error={isError} />
}
