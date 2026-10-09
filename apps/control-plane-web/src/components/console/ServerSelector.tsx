import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router'
import { useInstanceAggregate, useSearchInstances } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useConsoleStore } from '@/stores/console'
import { useInstanceHoverPrefetch } from '@/lib/instances/instance-prefetch'
import { recordRecentServer, toggleFavoriteServer, useFavoriteServers, useRecentServers } from '@/lib/console/server-selection'
import ServerSelectorView from '@/components/views/instances/ServerSelector'

const PAGE_SIZE = 200

/**
 * 服务器选择器的应用接线层（ADR-097 a 范式）。
 *
 * 视图本体是受控视图（见 components/views）；本层持有开合与搜索词（取数依赖它们）、接服务端搜索与聚合计数、
 * 注入最近/收藏 store 与悬停预取器，并把「打开实例」接上记录最近 + 路由跳转。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function ServerSelector() {
  const navigate = useNavigate()
  const selectedNodeId = useConsoleStore((s) => s.selectedNodeId)
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const recent = useRecentServers()
  const favorites = useFavoriteServers()
  const trimmed = query.trim()

  const params = useMemo(
    () => ({
      q: trimmed || undefined,
      nodeId: selectedNodeId ?? undefined,
      page: 1,
      pageSize: PAGE_SIZE,
      sort: 'name' as const,
      order: 'asc' as const,
    }),
    [trimmed, selectedNodeId],
  )
  const aggregateParams = useMemo(
    () => ({ q: trimmed || undefined, nodeId: selectedNodeId ?? undefined }),
    [trimmed, selectedNodeId],
  )

  const { data: searchResult, isLoading, isFetching } = useSearchInstances(params, open)
  const { data: aggregate } = useInstanceAggregate(aggregateParams, open)
  const { data: nodes = [] } = useNodes()
  // 行悬停预取实例详情（FR-297）：稳定悬停 150ms 才预取，点击进入即命中缓存。
  const prefetcher = useInstanceHoverPrefetch()
  const nodeNames = useMemo(() => new Map(nodes.map((node) => [node.id, node.name])), [nodes])
  const items = searchResult?.items ?? []
  const totalCount = aggregate?.total ?? searchResult?.total ?? 0

  return (
    <ServerSelectorView
      open={open}
      onOpenChange={setOpen}
      query={query}
      onQueryChange={setQuery}
      selectedNodeId={selectedNodeId}
      nodeNames={nodeNames}
      items={items}
      totalCount={totalCount}
      loading={isLoading || (isFetching && items.length === 0)}
      recent={recent}
      favorites={favorites}
      prefetcher={prefetcher}
      onOpen={(instance) => {
        // 视图只给最小结构；「最近」store 需要完整快照，故按 id 从各来源找回原对象。
        const full = items.find((i) => i.id === instance.id) ?? recent.find((i) => i.id === instance.id)
        if (full) recordRecentServer(full)
        setOpen(false)
        navigate(`/instances/${instance.id}`)
      }}
      onToggleFavorite={(instance) => {
        const full =
          favorites.find((i) => i.id === instance.id) ?? items.find((i) => i.id === instance.id)
        if (full) toggleFavoriteServer(full)
      }}
    />
  )
}
