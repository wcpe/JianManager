import { useQuery } from '@tanstack/react-query'
import { dispatchBusiness, fetchBusinessManifest } from '@/api/business'
import BusinessSegmentView from '@/components/views/instances/BusinessSegment'

/**
 * 业务掌控台的应用接线层（ADR-097 a 范式）。
 *
 * 视图本体已迁入组件库并受控；本层取能力清单、把 manifest 解析成「域 → 动作」并接下发动作。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function BusinessSegment({ instanceId }: { instanceId: number }) {
  const manifestQuery = useQuery({
    queryKey: ['business-manifest', instanceId],
    queryFn: () => fetchBusinessManifest(instanceId),
    enabled: !!instanceId,
  })

  const manifest = manifestQuery.data
  const domains = manifest?.available ? (manifest.output?.domains ?? {}) : {}

  return (
    <BusinessSegmentView
      manifestLoading={manifestQuery.isLoading}
      manifestRefreshing={manifestQuery.isFetching}
      available={!!manifest?.available}
      domains={domains}
      manifestError={manifest?.error}
      onRefreshManifest={() => void manifestQuery.refetch()}
      onDispatch={async (payload) => {
        const res = await dispatchBusiness(
          instanceId,
          payload.domain,
          payload.action,
          payload.payload,
          payload.write
            ? { write: true, operationId: payload.operationId, reason: payload.reason }
            : undefined,
        )
        // 失败原因由视图按 `available` 渲染，这里只把文案兜底补上（外壳持有 t）。
        return res
      }}
    />
  )
}
