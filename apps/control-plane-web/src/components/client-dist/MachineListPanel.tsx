import { useState } from 'react'
import { MachineListPanelView } from '@/components/views/client-dist/MachineListPanelView'
import { MachineTimelineView } from '@/components/views/client-dist/MachineTimelineView'
import type { ClientMachineSummary, MachineSortField } from '@/lib/client-dist-machines-contracts'
import ClientDistExportButton from '@/components/client-dist/ClientDistExportButton'
import { useClientDistMachines, useClientMachineEvents } from '@/api/clientDistMachines'

/**
 * 机器级更新清单与钻取（FR-426）：「用户」= 机器（machineId）。
 * 窗口内每台机器的更新次数 / 最近更新 / 版本滞后；点行看该机器更新事件时间线。
 * 明细保留窗 14 天：exact=false 时 UI 明示「近似」（ADR-049）。
 *
 * 展示层已回迁应用侧（`MachineListPanelView` / `MachineTimelineView`），此处只保留取数、
 * 查询状态（sort/order/page 驱动查询，故留在容器）与 slot 组装。
 */
export function MachineListPanel({ channelId, from, to }: { channelId?: string; from: string; to: string }) {
  const [sort, setSort] = useState<MachineSortField>('updates')
  const [order, setOrder] = useState<'asc' | 'desc'>('desc')
  const [page, setPage] = useState(1)

  const query = useClientDistMachines({ channelId, from, to, sort, order, page, pageSize: 20 })

  const toggleSort = (field: MachineSortField) => {
    if (sort === field) {
      setOrder((o) => (o === 'desc' ? 'asc' : 'desc'))
    } else {
      setSort(field)
      setOrder('desc')
    }
    setPage(1)
  }

  return (
    <MachineListPanelView
      items={query.data?.items ?? []}
      total={query.data?.total ?? 0}
      exact={query.data?.exact}
      isLoading={query.isLoading}
      sort={sort}
      order={order}
      page={page}
      onToggleSort={toggleSort}
      onPageChange={setPage}
      exportSlot={<ClientDistExportButton kind="machine-updates" filters={{ channelId: channelId ?? '', from, to }} />}
      renderTimeline={(m: ClientMachineSummary) => (
        <MachineTimeline machineId={m.machineId} channelId={m.channelId} from={from} to={to} />
      )}
    />
  )
}

/** 机器事件时间线容器：取数后交给包内展示层。 */
function MachineTimeline({ machineId, channelId, from, to }: { machineId: string; channelId: string; from: string; to: string }) {
  const query = useClientMachineEvents({ machineId, channelId, from, to })
  return <MachineTimelineView items={query.data?.items ?? []} isLoading={query.isLoading} />
}

export default MachineListPanel
