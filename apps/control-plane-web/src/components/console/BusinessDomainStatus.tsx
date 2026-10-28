import { useQuery } from '@tanstack/react-query'
import { useBusinessStatus } from '@/api/businessStatus'
import { fetchBusinessManifest } from '@/api/business'
import BusinessDomainStatusView from '@/components/views/instances/BusinessDomainStatus'

/**
 * 业务域状态面板的应用接线层（ADR-103 a 范式）。
 *
 * 视图本体是受控视图（见 components/views）；本层只做两件事：
 * 取域状态（`GET /instances/:id/business/status`，30s TTL，见 {@link useBusinessStatus}），
 * 以及取同一实例的业务能力清单供「查看 manifest」抽屉展示原文。
 *
 * 为什么 manifest 也在这里取：它的取数与动作台 {@link BusinessSegment} 用的是同一个
 * query key（`['business-manifest', instanceId]`），TanStack Query 会命中同一份缓存——
 * 两个面板同屏共存时只发一次请求，不会因为多了这块面板就让业务页签多拉一次。
 */
export default function BusinessDomainStatus({
  instanceId,
  className,
}: {
  instanceId: number
  /** 追加到面板根节点的尺寸约束（如业务页签里的高度上限）。 */
  className?: string
}) {
  const statusQuery = useBusinessStatus(instanceId)

  // 复用动作台的 query key：同一实例的 manifest 全页签只取一次。
  const manifestQuery = useQuery({
    queryKey: ['business-manifest', instanceId],
    queryFn: () => fetchBusinessManifest(instanceId),
    enabled: !!instanceId,
  })

  const manifest = manifestQuery.data?.available ? manifestQuery.data.output ?? undefined : undefined

  return (
    <BusinessDomainStatusView
      status={statusQuery.data}
      manifest={manifest}
      loading={statusQuery.isLoading}
      refreshing={statusQuery.isFetching}
      error={statusQuery.isError}
      onRefresh={() => void statusQuery.refetch()}
      className={className}
    />
  )
}
