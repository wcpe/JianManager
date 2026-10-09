import { useConfigDiscover } from '@/api/configs'
import { FavoritesBarView } from '@/components/views/config-explorer/FavoritesBarView'

/**
 * 配置左栏：收藏（书签）+ 已发现配置面板（FR-071）。
 *
 * 收藏点选打开 / 取消收藏；已发现配置为 `GET /configs/discover` 递归结果（不限内置 schema），
 * 按目录分组展示，点选打开、星标收藏。
 *
 * 展示层已回迁应用侧（`FavoritesBarView`），此处只做取数与透传。
 */
interface FavoritesBarProps {
  instanceId: number
  /** 已收藏的相对路径列表。 */
  favorites: string[]
  /** 切换收藏。 */
  onToggleFavorite: (path: string) => void
  /** 打开某配置文件（交给资源管理器编辑器）。 */
  onOpen: (path: string) => void
}

export default function FavoritesBar({ instanceId, favorites, onToggleFavorite, onOpen }: FavoritesBarProps) {
  const discoverQ = useConfigDiscover(instanceId)

  return (
    <FavoritesBarView
      favorites={favorites}
      onToggleFavorite={onToggleFavorite}
      onOpen={onOpen}
      discover={discoverQ.data}
      discoverLoading={discoverQ.isLoading}
      discoverError={!!discoverQ.error}
    />
  )
}
