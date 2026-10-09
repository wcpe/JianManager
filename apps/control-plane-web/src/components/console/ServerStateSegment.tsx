import { useState } from 'react'
import { useServerState } from '@/api/serverState'
import ServerStateSegmentView from '@/components/views/instances/ServerStateSegment'

/**
 * 「服务器状态」段的应用接线层（ADR-097 a 范式）。
 *
 * 视图本体已迁入组件库并受控；本层持有自动刷新间隔（它是 `useServerState` 的入参，
 * 必须在 hook 这一层）并接取数与刷新。保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function ServerStateSegment({ instanceId }: { instanceId: number }) {
  // 自动刷新默认关（保持 FR-077 非侵入按需语义）；开启后按间隔自动重拉。
  const [autoMs, setAutoMs] = useState<number | false>(false)
  const { data, isFetching, isError, refetch } = useServerState(instanceId, true, autoMs)

  return (
    <ServerStateSegmentView
      data={data}
      fetching={isFetching}
      error={isError}
      autoRefreshMs={autoMs}
      onAutoRefreshChange={setAutoMs}
      onRefresh={() => void refetch()}
    />
  )
}
