import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Star, ChevronDown, FileText, Search } from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@jianmanager/ui/components/collapsible'
import type { ConfigDiscoverResult } from '@/lib/config-explorer/config-contracts'
import { groupDiscovered, baseNameOf } from '@/lib/config-explorer/config-discover'

/**
 * 配置左栏：收藏（书签）+ 已发现配置面板（FR-071）。
 *
 * 收藏点选打开 / 取消收藏；已发现配置为 `GET /configs/discover` 递归结果（不限内置 schema），
 * 按目录分组展示，点选打开、星标收藏。
 */
export interface FavoritesBarViewProps {
  /** 已收藏的相对路径列表。 */
  favorites: string[]
  /** 切换收藏。 */
  onToggleFavorite: (path: string) => void
  /** 打开某配置文件（交给资源管理器编辑器）。 */
  onOpen: (path: string) => void
  /** 已发现配置（`GET /configs/discover` 结果），由应用侧取数容器传入。 */
  discover?: ConfigDiscoverResult
  discoverLoading: boolean
  discoverError: boolean
}

export function FavoritesBarView({
  favorites,
  onToggleFavorite,
  onOpen,
  discover,
  discoverLoading,
  discoverError,
}: FavoritesBarViewProps) {
  const { t } = useTranslation()
  const [filter, setFilter] = useState('')

  const favSet = useMemo(() => new Set(favorites), [favorites])

  const groups = useMemo(() => {
    const all = discover?.files ?? []
    const f = filter.trim().toLowerCase()
    const filtered = f ? all.filter((x) => x.path.toLowerCase().includes(f)) : all
    return groupDiscovered(filtered)
  }, [discover, filter])

  return (
    <div className="flex max-h-[55%] shrink-0 flex-col border-b text-xs">
      {/*
        两段各自独立开合（收藏 / 已发现配置），互不影响 → Collapsible 而非 Accordion：
        Accordion 表达的是「同一组里只开一个（或受控多开）」，这里的两个折叠段语义无关，
        用 Collapsible 才不会把「展开收藏」错误地当成「切换已发现配置的选中项」。
        换成原语的实质收益：aria-expanded / aria-controls 由原语维护（此前靠条件渲染，
        折叠头没有 aria-expanded，读屏拿不到开合态），data-state 也让旋转箭头不必再手写三元。
        defaultOpen：折叠头承载「点开才看得见」的信息，首屏默认展开与原行为一致。
      */}
      <Collapsible defaultOpen>
        <CollapsibleTrigger className="group flex w-full items-center gap-1 px-2 py-1.5 font-medium hover:bg-accent/40">
          <ChevronDown className="size-3.5 transition-transform group-data-[state=closed]:-rotate-90" />
          <Star className="size-3.5 text-amber-500" />
          {t('configExplorer.favorites')} ({favorites.length})
        </CollapsibleTrigger>
        <CollapsibleContent>
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
                  <Button
                    type="button"
                    variant="invisible"
                    size="auto"
                    // 文件名整行可点：无按钮外壳
                    className="flex min-w-0 flex-1 items-center justify-start gap-1 text-left"
                    onClick={() => onOpen(p)}
                  >
                    <FileText className="size-3.5 shrink-0 text-muted-foreground" />
                    <span className="truncate">{baseNameOf(p)}</span>
                  </Button>
                  <Button
                    type="button"
                    variant="invisible"
                    size="auto"
                    // 收藏星标：纯图标控件，颜色由 className 决定
                    className="shrink-0 text-amber-500 hover:text-amber-600"
                    title={t('configExplorer.unfavorite')}
                    onClick={() => onToggleFavorite(p)}
                  >
                    <Star className="size-3.5 fill-current" />
                  </Button>
                </div>
              ))
            )}
          </div>
        </CollapsibleContent>
      </Collapsible>

      {/* 已发现配置 */}
      <Collapsible defaultOpen className="flex min-h-0 flex-1 flex-col border-t">
        <CollapsibleTrigger className="group flex w-full items-center gap-1 px-2 py-1.5 font-medium hover:bg-accent/40">
          <ChevronDown className="size-3.5 transition-transform group-data-[state=closed]:-rotate-90" />
          {t('configExplorer.discovered')}
          {discover ? ` (${discover.files.length})` : ''}
        </CollapsibleTrigger>
        <CollapsibleContent className="min-h-0 flex-1">
          <div className="flex h-full min-h-0 flex-col">
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
              {discoverLoading ? (
                <p className="px-3 py-1 text-muted-foreground">{t('common.loading')}</p>
              ) : discoverError ? (
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
                          <Button
                            type="button"
                            variant="invisible"
                            size="auto"
                            // 文件名整行可点：无按钮外壳
                            className="flex min-w-0 flex-1 items-center justify-start gap-1 text-left"
                            onClick={() => onOpen(file.path)}
                          >
                            <FileText
                              className={cn(
                                'size-3.5 shrink-0',
                                file.supported ? 'text-emerald-500' : 'text-muted-foreground',
                              )}
                            />
                            <span className="truncate">{baseNameOf(file.path)}</span>
                          </Button>
                          <Button
                            type="button"
                            variant="invisible"
                            size="auto"
                            // 收藏星标：纯图标控件；未收藏态默认透明、行悬停才浮现
                            className={cn(
                              'shrink-0 hover:text-amber-600',
                              fav ? 'text-amber-500' : 'text-muted-foreground/40 opacity-0 group-hover:opacity-100',
                            )}
                            title={fav ? t('configExplorer.unfavorite') : t('configExplorer.favorite')}
                            onClick={() => onToggleFavorite(file.path)}
                          >
                            <Star className={cn('size-3.5', fav && 'fill-current')} />
                          </Button>
                        </div>
                      )
                    })}
                  </div>
                ))
              )}
              {discover?.truncated && (
                <p className="px-3 py-1 text-[10px] text-amber-600">{t('configExplorer.truncated')}</p>
              )}
            </div>
          </div>
        </CollapsibleContent>
      </Collapsible>
    </div>
  )
}