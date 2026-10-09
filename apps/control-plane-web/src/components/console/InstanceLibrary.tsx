import { useMemo, useState } from 'react'
import { useInfiniteInstanceSearch, type InstanceInfo } from '@/api/instances'
import InstanceLibraryView from '@/components/views/instances/InstanceLibrary'

/**
 * 实例库面板的应用接线层（ADR-097 a 范式）。
 *
 * 面板本体是受控视图（见 components/views）；本层接服务端搜索分页（`/instances/search`，FR-235 范式）：
 * 视图给出已防抖的搜索词，本层据此取数并把累积结果、分页状态回灌。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function InstanceLibrary({
  collapsed,
  onToggleCollapsed,
}: {
  /** 折叠态（仅图标轨）。 */
  collapsed: boolean
  /** 切换折叠。 */
  onToggleCollapsed: () => void
}) {
  const [query, setQuery] = useState('')
  const searchParams = useMemo(
    () => ({ ...(query ? { q: query } : {}), pageSize: 50, sort: 'name' as const, order: 'asc' as const }),
    [query],
  )
  const {
    data: searchData,
    isLoading,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
  } = useInfiniteInstanceSearch(searchParams)
  const instances = useMemo<InstanceInfo[]>(
    () => searchData?.pages.flatMap((p) => p.items) ?? [],
    [searchData],
  )
  const totalCount = searchData?.pages[0]?.total ?? instances.length

  return (
    <InstanceLibraryView
      collapsed={collapsed}
      onToggleCollapsed={onToggleCollapsed}
      instances={instances}
      totalCount={totalCount}
      loading={isLoading}
      hasMore={hasNextPage}
      loadingMore={isFetchingNextPage}
      onLoadMore={() => {
        if (hasNextPage && !isFetchingNextPage) void fetchNextPage()
      }}
      onSearch={setQuery}
    />
  )
}
