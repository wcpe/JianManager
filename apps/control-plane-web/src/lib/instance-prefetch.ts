import { useEffect, useMemo } from 'react'
import { useQueryClient } from '@tanstack/react-query'

// 纯逻辑（延迟常量、HoverPrefetcher 类型、createHoverPrefetcher）已迁至 `@jianmanager/ui`；
// 此处转出，调用点零改动。
export * from '@jianmanager/ui/lib/instance-prefetch'

import { createHoverPrefetcher, INSTANCE_PREFETCH_DELAY_MS, type HoverPrefetcher } from '@jianmanager/ui/lib/instance-prefetch'
import { instanceQueryOptions } from '@/api/instances'

/**
 * 悬停预取实例详情 hook（FR-297）：服务器选择器与侧栏常驻列（FR-293）复用。
 * 仅预取详情（与 useInstance 同 queryKey，命中即免等待），不预取指标。
 *
 * hook 留应用侧——它要读 queryClient 与应用侧查询选项。
 */
export function useInstanceHoverPrefetch(delayMs = INSTANCE_PREFETCH_DELAY_MS): HoverPrefetcher {
  const qc = useQueryClient()
  const prefetcher = useMemo(
    () =>
      createHoverPrefetcher((id) => {
        void qc.prefetchQuery(instanceQueryOptions(id))
      }, delayMs),
    [qc, delayMs],
  )
  useEffect(() => () => prefetcher.cancel(), [prefetcher])
  return prefetcher
}
