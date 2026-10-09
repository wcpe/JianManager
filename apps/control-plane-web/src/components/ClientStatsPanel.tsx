import { useState } from 'react'
import { useSearchParams } from 'react-router'
import { useClientStats } from '@/api/clientStats'
import { useClientDistObservability } from '@/api/clientDistObservability'
import type { ObservabilityRange, ObservabilityWindow } from '@/api/clientDistObservability'
import { ClientStatsPanelView } from '@/components/views/client-dist/ClientStatsPanelView'
import type { ObsWindow } from '@/lib/obs-window'
import { readClientDistQuery } from '@/lib/client-dist-query'
import MachineListPanel from '@/components/client-dist/MachineListPanel'

/** 天数窗口 → 观测端点 range 枚举（两看板共用一个时间选择器）。 */
const DAYS_TO_RANGE: Record<number, ObservabilityRange> = { 7: '7d', 30: '30d', 90: '90d' }

/** 预设档 → FR-095 日看板天数。 */
const DAYS_TO_RANGE_DAYS: Record<string, number> = { '24h': 1, '7d': 7, '30d': 30, '90d': 90, '180d': 180 }

/** 自定义跨度 → 最近预设天数（FR-095 日看板暂不支持任意窗，取最近档近似）。 */
function spanToDays(fromIso: string, toIso: string): number {
  const f = new Date(fromIso)
  const t = new Date(toIso)
  if (Number.isNaN(f.getTime()) || Number.isNaN(t.getTime()) || t <= f) return 30
  const days = Math.ceil((t.getTime() - f.getTime()) / 86_400_000)
  if (days <= 1) return 1
  if (days <= 7) return 7
  if (days <= 30) return 30
  return 90
}

/**
 * 客户端分发统计看板（FR-095 + FR-217/FR-219 + FR-356，见 ADR-023/ADR-049）的接线层（ADR-097）。
 *
 * 展示层已回迁应用侧（`ClientStatsPanelView`），此处只保留应用侧职责：
 * - 统一时间窗（FR-425）的持有与 URL 深链解析：初始窗读当前查询串（`from`/`to` 优先，否则默认 30d），
 *   之后由本层状态驱动；
 * - 窗口 → 查询参数映射：预设档映射回天数供 `/client-dist/stats`（FR-095 日看板），
 *   并按天数映射观测 range 或直接透传 from/to 供 `/client-dist/observability`；
 * - 两个端点的取数与加载态注入；
 * - 机器更新排行（FR-426）容器注入为插槽（`MachineListPanel` 按频道 + 观测窗口自行取数）。
 */
export default function ClientStatsPanel({ channelId }: { channelId: string }) {
  // FR-425：统一时间筛选（预设 + 任意起止），窗口与 URL 深链双向同步；预设档映射回天数供 FR-095 日看板查询。
  const [searchParams] = useSearchParams()
  const urlQuery = readClientDistQuery(searchParams)
  const [window, setWindow] = useState<ObsWindow>(
    urlQuery.from && urlQuery.to ? { from: urlQuery.from, to: urlQuery.to } : { range: '30d' },
  )
  const days = 'range' in window ? DAYS_TO_RANGE_DAYS[window.range] ?? 30 : spanToDays(window.from, window.to)
  const { data, isLoading } = useClientStats(channelId, days)
  const obsWindow: ObservabilityWindow = 'range' in window
    ? { range: DAYS_TO_RANGE[days] ?? '30d' }
    : { from: window.from, to: window.to }
  const { data: obs, isLoading: obsLoading } = useClientDistObservability(channelId, obsWindow)

  return (
    <ClientStatsPanelView
      stats={data}
      observability={obs}
      statsLoading={isLoading}
      obsLoading={obsLoading}
      window={window}
      onWindowChange={setWindow}
      machineRankingSlot={<MachineListPanel channelId={channelId} from={obs?.from ?? ''} to={obs?.to ?? ''} />}
    />
  )
}
