import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Star } from 'lucide-react'

import { cn } from '@jianmanager/ui'
import InstanceStatusDot from '@/components/views/instances/InstanceStatusDot'
import type { StoredInstance } from '@/lib/console/server-selection'

/** 最近打开在侧栏的展示上限（与存储层的 LRU 上限独立）。 */
export const SIDEBAR_RECENT_LIMIT = 8

/**
 * 侧栏常驻服务器列（FR-293，增强 FR-240）：「选择服务器」按钮下方常驻两区 =
 * 收藏（置顶）+ 最近打开（LRU ≤8，已收藏条目去重不重复出现）。
 * 与选择器弹窗共用同一份存储、经外壳的订阅互通；
 * 行 = 状态点 + 名称（title 含节点名），点击进入该服控制台并计入最近。
 * 折叠图标轨（compact 轨）不挂载本组件，天然不显示。
 *
 * 受控视图（ADR-097 a 范式）：不取数、不发请求——收藏/最近、实时状态与节点名都由外壳注入，
 * 打开与切换收藏经回调上报。两区去重、空态与行渲染留在视图内。
 */
export interface SidebarServerListProps {
  /** 收藏（置顶）。 */
  favorites: StoredInstance[]
  /** 最近打开（已按 LRU 排序）。 */
  recent: StoredInstance[]
  /** 实时状态覆盖（id → status）；未命中回落条目自带的快照。 */
  liveStatus?: Map<number, string>
  /** 节点名映射。 */
  nodeNames?: Map<number, string>
  /** 打开实例。 */
  onOpen: (item: StoredInstance) => void
  /** 切换收藏。 */
  onToggleFavorite: (item: StoredInstance) => void
  /** 最近列表展示上限。 */
  recentLimit?: number
}

export default function SidebarServerList({
  favorites,
  recent,
  liveStatus,
  nodeNames,
  onOpen,
  onToggleFavorite,
  recentLimit = SIDEBAR_RECENT_LIMIT,
}: SidebarServerListProps) {
  const { t } = useTranslation()
  const favoriteIds = useMemo(() => new Set(favorites.map((item) => item.id)), [favorites])
  // 已收藏的条目不再在「最近」区重复出现。
  const recentRows = useMemo(
    () => recent.filter((item) => !favoriteIds.has(item.id)).slice(0, recentLimit),
    [recent, favoriteIds, recentLimit],
  )

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

  const renderRow = (item: StoredInstance) => (
    <ServerRow
      key={item.id}
      item={item}
      favorite={favoriteIds.has(item.id)}
      status={liveStatus?.get(item.id) ?? item.status}
      nodeName={nodeNames?.get(item.nodeId) ?? `#${item.nodeId}`}
      onOpen={onOpen}
      onToggleFavorite={onToggleFavorite}
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
