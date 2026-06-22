import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Star, ChevronDown, ChevronRight, FileText, Search } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useConfigDiscover } from '@/api/configs'
import { groupDiscovered, baseNameOf } from './discover'

/**
 * 配置左栏：收藏（书签）+ 已发现配置面板（FR-071）。
 *
 * 收藏点选打开 / 取消收藏；已发现配置为 `GET /configs/discover` 递归结果（不限内置 schema），
 * 按目录分组展示，点选打开、星标收藏。
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
  const { t } = useTranslation()
  const [favOpen, setFavOpen] = useState(true)
  const [discOpen, setDiscOpen] = useState(true)
  const [filter, setFilter] = useState('')

  const discoverQ = useConfigDiscover(instanceId)
  const favSet = useMemo(() => new Set(favorites), [favorites])

  const groups = useMemo(() => {
    const all = discoverQ.data?.files ?? []
    const f = filter.trim().toLowerCase()
    const filtered = f ? all.filter((x) => x.path.toLowerCase().includes(f)) : all
    return groupDiscovered(filtered)
  }, [discoverQ.data, filter])

  return (
    <div className="flex max-h-[55%] shrink-0 flex-col border-b text-xs">
      {/* 收藏 */}
      <button
        type="button"
        className="flex items-center gap-1 px-2 py-1.5 font-medium hover:bg-accent/40"
        onClick={() => setFavOpen((o) => !o)}
      >
        {favOpen ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
        <Star className="size-3.5 text-amber-500" />
        {t('configExplorer.favorites')} ({favorites.length})
      </button>
      {favOpen && (
        <div className="max-h-40 overflow-auto pb-1">
          {favorites.length === 0 ? (
            <p className="px-3 py-1 text-muted-foreground">{t('configExplorer.noFavorites')}</p>
          ) : (
            favorites.map((p) => (
              <div
                key={p}
                className="group flex items-center gap-1 px-2 py-0.5 hover:bg-accent/40"
                title={p}
              >
                <button
                  type="button"
                  className="flex min-w-0 flex-1 items-center gap-1 text-left"
                  onClick={() => onOpen(p)}
                >
                  <FileText className="size-3.5 shrink-0 text-muted-foreground" />
                  <span className="truncate">{baseNameOf(p)}</span>
                </button>
                <button
                  type="button"
                  className="shrink-0 text-amber-500 hover:text-amber-600"
                  title={t('configExplorer.unfavorite')}
                  onClick={() => onToggleFavorite(p)}
                >
                  <Star className="size-3.5 fill-current" />
                </button>
              </div>
            ))
          )}
        </div>
      )}

      {/* 已发现配置 */}
      <button
        type="button"
        className="flex items-center gap-1 border-t px-2 py-1.5 font-medium hover:bg-accent/40"
        onClick={() => setDiscOpen((o) => !o)}
      >
        {discOpen ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
        {t('configExplorer.discovered')}
        {discoverQ.data ? ` (${discoverQ.data.files.length})` : ''}
      </button>
      {discOpen && (
        <div className="flex min-h-0 flex-1 flex-col">
          <div className="flex items-center gap-1 px-2 pb-1">
            <Search className="size-3 text-muted-foreground" />
            <input
              className="w-full rounded bg-muted px-1.5 py-0.5 text-[11px]"
              placeholder={t('configExplorer.filterPlaceholder')}
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
            />
          </div>
          <div className="min-h-0 flex-1 overflow-auto pb-1">
            {discoverQ.isLoading ? (
              <p className="px-3 py-1 text-muted-foreground">{t('common.loading')}</p>
            ) : discoverQ.error ? (
              <p className="px-3 py-1 text-destructive">{t('configExplorer.discoverFailed')}</p>
            ) : groups.length === 0 ? (
              <p className="px-3 py-1 text-muted-foreground">{t('configExplorer.noDiscovered')}</p>
            ) : (
              groups.map((g) => (
                <div key={g.dir || '__root__'}>
                  {g.dir && <div className="px-2 pt-1 text-[10px] font-medium text-muted-foreground">{g.dir}/</div>}
                  {g.files.map((file) => {
                    const fav = favSet.has(file.path)
                    return (
                      <div
                        key={file.path}
                        className="group flex items-center gap-1 px-2 py-0.5 hover:bg-accent/40"
                        title={file.path}
                      >
                        <button
                          type="button"
                          className="flex min-w-0 flex-1 items-center gap-1 text-left"
                          onClick={() => onOpen(file.path)}
                        >
                          <FileText
                            className={cn(
                              'size-3.5 shrink-0',
                              file.supported ? 'text-emerald-500' : 'text-muted-foreground',
                            )}
                          />
                          <span className="truncate">{baseNameOf(file.path)}</span>
                        </button>
                        <button
                          type="button"
                          className={cn(
                            'shrink-0 hover:text-amber-600',
                            fav ? 'text-amber-500' : 'text-muted-foreground/40 opacity-0 group-hover:opacity-100',
                          )}
                          title={fav ? t('configExplorer.unfavorite') : t('configExplorer.favorite')}
                          onClick={() => onToggleFavorite(file.path)}
                        >
                          <Star className={cn('size-3.5', fav && 'fill-current')} />
                        </button>
                      </div>
                    )
                  })}
                </div>
              ))
            )}
            {discoverQ.data?.truncated && (
              <p className="px-3 py-1 text-[10px] text-amber-600">{t('configExplorer.truncated')}</p>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
