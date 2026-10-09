import { useClientDistObservability } from '@/api/clientStats'
import { ObsOverviewView } from '@/components/views/client-dist/ObsOverviewView'
import MachineListPanel from './MachineListPanel'
import type { ObsWindow } from '@/lib/metrics/obs-window'

/**
 * 分发观测总览区块（FR-426/427/428 mock 集成）：
 * 洞察卡（同环比+口径）→ 拉取/字节趋势 → 更新热力图 → 机器更新排行/钻取。
 * 频道工作台统计 Tab 与分发监控页客户端 Tab 共用；channelId 省略=跨频道总。
 *
 * 展示层已回迁应用侧（`ObsOverviewView`），此处只保留取数与 slot 组装：
 * 机器排行面板（`MachineListPanel`）同为取数件，作为 slot 注入。
 */
export function ObsOverviewSection({ channelId, window }: { channelId?: string; window: ObsWindow }) {
  const obs = useClientDistObservability({ channelId: channelId ?? undefined, window })

  return (
    <ObsOverviewView
      data={obs.data}
      isLoading={obs.isLoading}
      isError={obs.isError}
      machinePanel={obs.data ? <MachineListPanel channelId={channelId} from={obs.data.from} to={obs.data.to} /> : null}
    />
  )
}

export default ObsOverviewSection
