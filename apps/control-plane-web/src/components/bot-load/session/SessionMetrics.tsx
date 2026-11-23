import { useState } from 'react'
import { SessionMetrics as SessionMetricsView, rangeResolution } from '@/components/views/bot-load/session/SessionMetrics'
import type { SessionMetricsRange } from '@/components/views/bot-load/session/SessionMetrics'
import { useBotLoadMetrics } from '@/api/bot-load'
import { useSessionEvents } from './SessionEventProvider'

/**
 * 会话指标接线层：时间窗决定服务端聚合粒度，故状态留容器；展示层已回迁应用侧。
 */
export function SessionMetrics({ runId }: { runId: number | string }) {
  const { live } = useSessionEvents()
  const [range, setRange] = useState<SessionMetricsRange>('15m')
  const { data, isLoading, isError } = useBotLoadMetrics(runId, {
    resolution: rangeResolution[range],
  })

  return (
    <SessionMetricsView
      range={range}
      onRangeChange={setRange}
      data={data}
      isLoading={isLoading}
      isError={isError}
      liveMetrics={live.liveMetrics}
    />
  )
}
