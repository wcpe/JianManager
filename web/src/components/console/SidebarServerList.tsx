import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { Star } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'

import api from '@/api/client'
import type { InstanceInfo } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { cn } from '@jianmanager/ui'
import InstanceStatusDot from './InstanceStatusDot'
import {
  RECENT_LIMIT,
  recordRecentServer,
  toggleFavoriteServer,
  useFavoriteServers,
  useRecentServers,
  type StoredInstance,
} from './server-selection'

/** 状态合并查询的刷新间隔：侧栏不引入高频轮询（FR-293），仅低频合并刷新列表内实例。 */
const STATUS_REFRESH_MS = 60_000

/**
 * 侧栏常驻服务器列（FR-293，增强 FR-240）：「选择服务器」按钮下方常驻两区 =
 * 收藏（置顶）+ 最近打开（LRU ≤8，已收藏条目去重不重复出现）。
 * 与选择器弹窗共用 localStorage 存储、经 server-selection 订阅互通；
 * 行 = 状态点 + 名称（title 含节点名），点击进入该服控制台并计入最近。
 * 折叠图标轨（compact 轨）不挂载本组件，天然不显示。
 */
export default function SidebarServerList() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const favorites = useFavoriteServers()
  const recent = useRecentServers()
  const favoriteIds = useMemo(() => new Set(favorites.map((item) => item.id)), [favorites])
  const recentRows = useMemo(
    () => recent.filter((item) => !favoriteIds.has(item.id)).slice(0, RECENT_LIMIT),
    [recent, favoriteIds],
  )
  const ids = useMemo(() => [...favorites, ...recentRows].map((item) => item.id), [favorites, recentRows])
  const liveStatus = useListedInstanceStatus(ids)
  const { data: nodes = [] } = useNodes({ enabled: ids.length > 0 })
  const nodeNames = useMemo(() => new Map(nodes.map((node) => [node.id, node.name])), [nodes])

  if (favorites.length === 0 && recentRows.length === 0) {
    return (
      <p
        data-testid="sidebar-server-list"
        className="mt-2 rounded-md border border-dashed px-2 py-2 text-[11px] leading-relaxed text-muted-foreground"
      >
        {t('sidebarServers.empty')}
      </p>
    )
  }

  const openInstance = (item: StoredInstance) => {
    recordRecentServer(item)
    navigate(`/instances/${item.id}`)
  }

  const renderRow = (item: StoredInstance) => (
    <ServerRow
      key={item.id}
      item={item}
      favorite={favoriteIds.has(item.id)}
      status={liveStatus.get(item.id) ?? item.status}
      nodeName={nodeNames.get(item.nodeId) ?? `#${item.nodeId}`}
      onOpen={openInstance}
      onToggleFavorite={toggleFavoriteServer}
    />
  )

  return (
    <div
      data-testid="sidebar-server-list"
      aria-label={t('sidebarServers.listLabel')}
      className="mt-2 max-h-64 space-y-2 overflow-y-auto scrollbar-none"
    >
      {favorites.length > 0 && (
        <section data-testid="sidebar-server-favorites" aria-label={t('sidebarServers.favorites')}>
          <h3 className="px-2 pb-0.5 text-[11px] font-medium uppercase tracking-wide text-muted-foreground/70">
            {t('sidebarServers.favorites')}
          </h3>
          <div className="space-y-0.5">{favorites.map(renderRow)}</div>
        </section>
      )}
      {recentRows.length > 0 && (
        <section data-testid="sidebar-server-recent" aria-label={t('sidebarServers.recent')}>
          <h3 className="px-2 pb-0.5 text-[11px] font-medium uppercase tracking-wide text-muted-foreground/70">
            {t('sidebarServers.recent')}
          </h3>
          <div className="space-y-0.5">{recentRows.map(renderRow)}</div>
        </section>
      )}
    </div>
  )
}

function ServerRow({
  item,
  favorite,
  status,
  nodeName,
  onOpen,
  onToggleFavorite,
}: {
  item: StoredInstance
  favorite: boolean
  /** 展示状态：查询缓存命中值优先，未命中回落本地快照。 */
  status: string
  nodeName: string
  onOpen: (item: StoredInstance) => void
  onToggleFavorite: (item: StoredInstance) => void
}) {
  const { t } = useTranslation()
  return (
    <div data-testid="sidebar-server-row" className="flex items-center gap-0.5 rounded-md transition-colors hover:bg-accent/60">
      <button
        type="button"
        onClick={() => onOpen(item)}
        title={`${item.name} · ${nodeName}`}
        className="flex min-w-0 flex-1 items-center gap-2 rounded-md px-2 py-1.5 text-left text-[13px] text-foreground/90"
      >
        <InstanceStatusDot status={status} />
        <span className="min-w-0 flex-1 truncate">{item.name}</span>
      </button>
      <button
        type="button"
        onClick={() => onToggleFavorite(item)}
        aria-label={t(favorite ? 'serverSelector.unfavoriteName' : 'serverSelector.favoriteName', { name: item.name })}
        className={cn(
          'rounded p-1.5 text-muted-foreground/70 transition-colors hover:bg-accent hover:text-foreground',
          favorite && 'text-status-warning',
        )}
      >
        <Star className={cn('size-3.5', favorite && 'fill-current')} />
      </button>
    </div>
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
        if (result.status === 'fulfilled') statuses.push([sortedIds[index]!, result.value.status])
      })
      return statuses
    },
  })
  return useMemo(() => new Map(data ?? []), [data])
}
