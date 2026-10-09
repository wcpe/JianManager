import { useMemo } from 'react'
import { useNavigate } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'
import type { InstanceInfo } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { recordRecentServer, removeServer, toggleFavoriteServer, useFavoriteServers, useRecentServers } from '@/lib/server-selection'
import SidebarServerList from '@/components/views/instances/SidebarServerList'

/** 状态合并查询的刷新间隔：侧栏不引入高频轮询（FR-293），仅低频合并刷新列表内实例。 */
const STATUS_REFRESH_MS = 60_000

/** 判定错误是否为确定性 404（实例已删）——用于对账剔除死链；网络错等不算，避免误删。 */
function isNotFound(reason: unknown): boolean {
  return (reason as { response?: { status?: number } })?.response?.status === 404
}

/**
 * 侧栏常驻服务器列的应用接线层（ADR-097 a 范式）。
 *
 * 视图本体是受控视图（见 components/views）；本层订阅收藏/最近存储、合并列表内实例的实时状态
 * （含 404 死链对账剔除）、解析节点名，并把打开与切换收藏接上 store 与路由。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function SidebarServerListContainer() {
  const navigate = useNavigate()
  const favorites = useFavoriteServers()
  const recent = useRecentServers()
  const favoriteIds = useMemo(() => new Set(favorites.map((item) => item.id)), [favorites])
  const recentRows = useMemo(() => recent.filter((item) => !favoriteIds.has(item.id)), [recent, favoriteIds])
  const ids = useMemo(() => [...favorites, ...recentRows].map((item) => item.id), [favorites, recentRows])
  const liveStatus = useListedInstanceStatus(ids)
  const { data: nodes = [] } = useNodes({ enabled: ids.length > 0 })
  const nodeNames = useMemo(() => new Map(nodes.map((node) => [node.id, node.name])), [nodes])

  return (
    <SidebarServerList
      favorites={favorites}
      recent={recent}
      liveStatus={liveStatus}
      nodeNames={nodeNames}
      onOpen={(item) => {
        recordRecentServer(item)
        navigate(`/instances/${item.id}`)
      }}
      onToggleFavorite={(item) => toggleFavoriteServer(item)}
    />
  )
}

/**
 * 列表内实例的低频合并状态查询（FR-293）：与 useInstance 同端点逐个拉取后合并为 id→status。
 * 用 allSettled 使单个失败（如实例已删）只回落该行的本地快照，不拖垮整列；
 * 不设短轮询，仅 STATUS_REFRESH_MS 低频合并刷新一次。
 */
function useListedInstanceStatus(ids: number[]): Map<number, string> {
  const sortedIds = useMemo(() => [...new Set(ids)].sort((a, b) => a - b), [ids])
  const { data } = useQuery({
    queryKey: ['instances', 'sidebar-status', sortedIds],
    enabled: sortedIds.length > 0,
    staleTime: 30_000,
    refetchInterval: STATUS_REFRESH_MS,
    queryFn: async () => {
      const results = await Promise.allSettled(
        sortedIds.map((id) => api.get<InstanceInfo>(`/instances/${id}`).then((res) => res.data)),
      )
      const statuses: [number, string][] = []
      results.forEach((result, index) => {
        if (result.status === 'fulfilled') {
          statuses.push([sortedIds[index]!, result.value.status])
        } else if (isNotFound(result.reason)) {
          // 该实例确已删除（404）：从最近/收藏剔除死链（BUG 修复对账兜底）——
          // 覆盖批量删/其它 tab/直接 API 删的场景。仅对确定性 404，网络错回落本地快照不误删。
          removeServer(sortedIds[index]!)
        }
      })
      return statuses
    },
  })
  return useMemo(() => new Map(data ?? []), [data])
}
