import { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import { Search, Server, Star, X } from 'lucide-react'

import { useVirtualRows } from '@/lib/shared/virtual-list'
import { cn, useFocusTrap } from '@jianmanager/ui'
import { Card } from '@jianmanager/ui/components/card'
import { Button } from '@jianmanager/ui/components/button'
import type { HoverPrefetcher, StoredInstance } from '@/lib/console/server-selection'

/** 选择器行所需的实例最小信息（外壳可传结构兼容的更宽类型）。 */
export interface SelectorInstance {
  id: number
  nodeId: number
  name: string
  status: string
}

const ROW_HEIGHT = 36

type GroupBy = 'node' | 'status'
type SelectorRow =
  | { kind: 'group'; key: string; label: string; count: number }
  | { kind: 'instance'; key: string; instance: SelectorInstance }

/** 
 * 导航外壳服务器选择器：服务端搜索 + 聚合计数 + 虚拟渲染 + 最近/收藏。
 * 最近/收藏经外壳的共享 store 读写，与侧栏常驻列实时互通。
 *
 * 受控视图（ADR-103 a 范式）：开合与搜索词也提到外壳——取数依赖它们（未打开时不查），
 * 视图拿不到；最近/收藏、节点名映射与悬停预取器同样由外壳注入。
 * 分组计算、虚拟渲染、portal 逃逸包含块留在视图内。
 */
export interface ServerSelectorProps {
  /** 弹窗开合（外壳据此决定是否取数）。 */
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 搜索词。 */
  query: string
  onQueryChange: (q: string) => void
  /** 当前选中的节点筛选；null=全部。 */
  selectedNodeId: number | null
  /** 节点名映射（分组标签与行内展示用）。 */
  nodeNames: Map<number, string>
  /** 搜索命中。 */
  items: SelectorInstance[]
  /** 总数（聚合计数）。 */
  totalCount: number
  /** 搜索中。 */
  loading?: boolean
  /** 最近打开。 */
  recent: StoredInstance[]
  /** 收藏。 */
  favorites: StoredInstance[]
  /** 悬停预取器。 */
  prefetcher: HoverPrefetcher
  /** 打开实例（外壳负责记录最近与路由跳转）。 */
  onOpen: (instance: SelectorInstance) => void
  /** 切换收藏。 */
  onToggleFavorite: (instance: SelectorInstance) => void
}

export default function ServerSelector({
  open,
  onOpenChange,
  query,
  onQueryChange,
  nodeNames,
  items,
  totalCount,
  loading = false,
  recent,
  favorites,
  prefetcher,
  onOpen,
  onToggleFavorite,
}: ServerSelectorProps) {
  const { t } = useTranslation()
  const [groupBy, setGroupBy] = useState<GroupBy>('node')
  const trimmed = query.trim()

  const rows = useMemo(() => buildRows(items, groupBy, nodeNames, t), [items, groupBy, nodeNames, t])
  const favoriteIds = useMemo(() => new Set(favorites.map((item) => item.id)), [favorites])
  const showQuickLists = trimmed.length === 0

  const { containerRef, onScroll, range, totalSize } = useVirtualRows({
    total: rows.length,
    itemSize: ROW_HEIGHT,
    overscan: 8,
    fallbackViewportSize: 360,
  })
  const visibleRows = rows.slice(range.start, range.end)

  const searchInputRef = useRef<HTMLInputElement>(null)

  // 键盘可达性：弹层此前只有 `role="dialog" aria-modal="true"` 两个声明，
  // 却没有配套的键盘与焦点管理——Esc 关不掉（只能点遮罩或关闭按钮），Tab 能一路跑到
  // 遮罩背后的侧栏与页眉，且屏读器仍读得到背景内容（ARIA 承诺了模态却没兑现）。
  // 这里接上共享焦点陷阱：Esc 交给 onOpenChange，Tab 在弹层内首尾环绕，关闭时还原焦点。
  // 陷阱边界取面板本身（而非遮罩）：遮罩只是点击热区、不含可聚焦内容。
  const { containerRef: dialogRef } = useFocusTrap<HTMLDivElement>({
    isActive: open,
    onEscape: () => onOpenChange(false),
  })

  // 打开后把焦点交给搜索框（呼出即可打字，与命令面板一致；陷阱的 focusFirst() 会落在
  // 关闭按钮上，那不是本弹层的主入口）。
  // 必须声明在 useFocusTrap 之后：陷阱要先记下「打开前的焦点」（触发按钮）作为还原点，
  // 若抢在它之前把焦点移进弹层，还原点就变成随后会被卸载的搜索框，关闭时焦点会掉到 body。
  useEffect(() => {
    if (!open) return
    searchInputRef.current?.focus()
  }, [open])

  return (
    <>
      {/*
        触发按钮：原为手写卡片样式的 <button>，改用 Card 统一圆角/描边/底色/阴影。
        原底色 `bg-card/80` 属次级档 → surface="subtle"。
        语义：Card 渲染 div，故补 role/tabIndex 与 Enter/Space 键处理，
        保住键盘可达与 `getByRole('button')` 的可访问性契约（焦点陷阱关闭后要靠它还原焦点）。
      */}
      <Card
        surface="subtle"
        role="button"
        tabIndex={0}
        onClick={() => onOpenChange(true)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            onOpenChange(true)
          }
        }}
        aria-label={t('serverSelector.open')}
        className="w-full flex-row items-center gap-2 px-2.5 py-2 text-left text-body transition-colors hover:bg-accent/60"
      >
        <Server className="size-4 shrink-0 text-primary" />
        <span className="min-w-0 flex-1 truncate">{t('serverSelector.open')}</span>
      </Card>

      {/* Portal 到 body：侧栏祖先带 transform/contain（折叠动画）会给 fixed 建立包含块，
          内联渲染会把全屏模态压到侧栏宽度内。渲染到 body 逃出该包含块，覆盖整个视口居中。 */}
      {open && createPortal(
        <div className="fixed inset-0 z-[58] flex items-start justify-center bg-black/45 p-4 pt-[10vh]" role="presentation" onClick={() => onOpenChange(false)}>
          {/*
            弹层面板：只把「圆角/描边/底色/阴影」交给 Card，浮层定位、max-h、溢出裁切
            与分段结构（标题栏 border-b + 内容区 flex-1）全部保留。
            Card 默认带 py-4/gap-4，会把标题栏与内容区推开并撑破 max-h，故 py-0/gap-0 归零。
          */}
          <Card
            ref={dialogRef}
            role="dialog"
            aria-modal="true"
            aria-label={t('serverSelector.title')}
            className="max-h-[76vh] w-full max-w-3xl gap-0 overflow-hidden py-0 shadow-lift"
            onClick={(event) => event.stopPropagation()}
          >
            <div className="flex items-center gap-2 border-b px-3 py-2">
              <Server className="size-4 text-primary" />
              <div className="min-w-0 flex-1">
                <h2 className="truncate text-sm font-semibold">{t('serverSelector.title')}</h2>
                <p className="truncate text-xs text-muted-foreground">{t('serverSelector.subtitle', { count: totalCount })}</p>
              </div>
              {/* 纯图标关闭：需与标题两行同高，故保留 p-1.5 原样，用 invisible + auto */}
              <Button
                type="button"
                variant="invisible"
                size="auto"
                onClick={() => onOpenChange(false)}
                aria-label={t('common.close')}
                className="rounded p-1.5 text-muted-foreground hover:bg-accent/60 hover:text-foreground"
              >
                <X className="size-4" />
              </Button>
            </div>

            <div className="space-y-2 border-b p-3">
              <label className="flex h-9 items-center gap-2 rounded-md border bg-background px-2.5 text-sm shadow-soft">
                <Search className="size-4 shrink-0 text-muted-foreground" />
                <input
                  ref={searchInputRef}
                  type="search"
                  value={query}
                  onChange={(event) => onQueryChange(event.target.value)}
                  aria-label={t('serverSelector.search')}
                  placeholder={t('serverSelector.search')}
                  className="min-w-0 flex-1 bg-transparent outline-none placeholder:text-muted-foreground"
                />
              </label>
              <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label={t('grouping.groupBy')}>
                {(['node', 'status'] as const).map((dim) => (
                  // 分组维度切换：无边框的轻量开关，选中态靠底色的 font-medium 表达，取 invisible + auto
                  <Button
                    key={dim}
                    type="button"
                    variant="invisible"
                    size="auto"
                    onClick={() => setGroupBy(dim)}
                    className={cn(
                      'rounded-md px-2 py-1 text-xs transition-colors',
                      groupBy === dim ? 'bg-accent font-medium text-primary' : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground',
                    )}
                  >
                    {t(`serverSelector.group_${dim}`)}
                  </Button>
                ))}
                <span className="ml-auto text-xs text-muted-foreground">{t('serverSelector.loaded', { count: items.length })}</span>
              </div>
            </div>

            <div className="min-h-0 flex-1 overflow-hidden p-3">
              {showQuickLists && (recent.length > 0 || favorites.length > 0) && (
                <div className="mb-3 grid gap-2 md:grid-cols-2">
                  <QuickList title={t('serverSelector.recent')} items={recent} favoriteIds={favoriteIds} prefetcher={prefetcher} onOpen={onOpen} onToggleFavorite={onToggleFavorite} />
                  <QuickList title={t('serverSelector.favorites')} items={favorites} favoriteIds={favoriteIds} prefetcher={prefetcher} onOpen={onOpen} onToggleFavorite={onToggleFavorite} />
                </div>
              )}

              {loading && rows.length === 0 ? (
                <p className="rounded-lg border border-dashed px-3 py-8 text-center text-sm text-muted-foreground">{t('common.loading')}</p>
              ) : rows.length === 0 ? (
                <p className="rounded-lg border border-dashed px-3 py-8 text-center text-sm text-muted-foreground">{t('serverSelector.empty')}</p>
              ) : (
                <div
                  ref={containerRef}
                  onScroll={onScroll}
                  data-testid="server-selector-virtual"
                  data-total-count={totalCount}
                  className="max-h-[42vh] min-h-72 overflow-auto rounded-lg border bg-background/70"
                >
                  <div style={{ height: totalSize, position: 'relative' }}>
                    <div style={{ transform: `translateY(${range.before}px)` }}>
                      {visibleRows.map((row) =>
                        row.kind === 'group' ? (
                          <div key={row.key} data-testid="server-selector-group" className="flex h-9 items-center gap-2 bg-muted/60 px-3 text-xs font-medium text-muted-foreground">
                            <span className="min-w-0 flex-1 truncate">{row.label}</span>
                            <span className="tabular-nums">{row.count}</span>
                          </div>
                        ) : (
                          <InstanceRow
                            key={row.key}
                            instance={row.instance}
                            favorite={favoriteIds.has(row.instance.id)}
                            nodeName={nodeNames.get(row.instance.nodeId)}
                            prefetcher={prefetcher}
                            onOpen={onOpen}
                            onToggleFavorite={onToggleFavorite}
                          />
                        ),
                      )}
                    </div>
                  </div>
                </div>
              )}
            </div>
          </Card>
        </div>,
        document.body,
      )}
    </>
  )
}

function QuickList({
  title,
  items,
  favoriteIds,
  prefetcher,
  onOpen,
  onToggleFavorite,
}: {
  title: string
  items: StoredInstance[]
  favoriteIds: Set<number>
  prefetcher: HoverPrefetcher
  onOpen: (instance: SelectorInstance) => void
  onToggleFavorite: (instance: SelectorInstance) => void
}) {
  if (items.length === 0) return null
  return (
    <section className="rounded-lg border bg-background/70 p-2">
      <h3 className="mb-1 px-1 text-xs font-medium text-muted-foreground">{title}</h3>
      <div className="space-y-1">
        {items.slice(0, 4).map((item) => (
          <StoredInstanceRow
            key={item.id}
            instance={item}
            favorite={favoriteIds.has(item.id)}
            prefetcher={prefetcher}
            onOpen={onOpen}
            onToggleFavorite={onToggleFavorite}
          />
        ))}
      </div>
    </section>
  )
}

function StoredInstanceRow({
  instance,
  favorite,
  prefetcher,
  onOpen,
  onToggleFavorite,
}: {
  instance: StoredInstance
  favorite: boolean
  prefetcher: HoverPrefetcher
  onOpen: (instance: SelectorInstance) => void
  onToggleFavorite: (instance: SelectorInstance) => void
}) {
  const { t } = useTranslation()
  return (
    <div
      className="flex items-center gap-1 rounded-md hover:bg-accent/50"
      onMouseEnter={() => prefetcher.enter(instance.id)}
      onMouseLeave={() => prefetcher.leave()}
    >
      {/* 实例条目：整行左侧可点区域，两行文字自带排版，故 invisible + auto */}
      <Button
        type="button"
        variant="invisible"
        size="auto"
        onClick={() => onOpen(instance)}
        className="min-w-0 flex-1 px-2 py-1.5 text-left text-sm"
      >
        <span className="block truncate">{instance.name}</span>
        <span className="block truncate text-meta text-muted-foreground">{instance.status}</span>
      </Button>
      {/* 收藏星标：纯图标切换，需保留 p-1.5 触达面积，故 invisible + auto */}
      <Button
        type="button"
        variant="invisible"
        size="auto"
        onClick={() => onToggleFavorite(instance)}
        aria-label={t(favorite ? 'serverSelector.unfavoriteName' : 'serverSelector.favoriteName', { name: instance.name })}
        className={cn('rounded p-1.5 text-muted-foreground hover:bg-accent hover:text-foreground', favorite && 'text-status-warning')}
      >
        <Star className={cn('size-4', favorite && 'fill-current')} />
      </Button>
    </div>
  )
}

function InstanceRow({
  instance,
  favorite,
  nodeName,
  prefetcher,
  onOpen,
  onToggleFavorite,
}: {
  instance: SelectorInstance
  favorite: boolean
  nodeName: string | undefined
  prefetcher: HoverPrefetcher
  onOpen: (instance: SelectorInstance) => void
  onToggleFavorite: (instance: SelectorInstance) => void
}) {
  const { t } = useTranslation()
  return (
    <div
      data-testid="server-selector-row"
      className="flex h-9 items-center gap-2 border-t px-2 text-sm hover:bg-accent/50"
      onMouseEnter={() => prefetcher.enter(instance.id)}
      onMouseLeave={() => prefetcher.leave()}
    >
      {/* 表格化列表行：整行左侧可点，行高由父容器 h-9 定，故 invisible + auto */}
      <Button
        type="button"
        variant="invisible"
        size="auto"
        onClick={() => onOpen(instance)}
        className="min-w-0 flex flex-1 items-center gap-2 text-left"
      >
        <span className="size-2 shrink-0 rounded-full bg-primary" />
        <span className="min-w-0 flex-1 truncate">{instance.name}</span>
        <span className="hidden shrink-0 text-meta text-muted-foreground sm:inline">{nodeName ?? `#${instance.nodeId}`}</span>
        <span className="shrink-0 text-meta text-muted-foreground">{instance.status}</span>
      </Button>
      {/* 收藏星标：纯图标切换，紧凑尺寸由 p-1 决定，故 invisible + auto */}
      <Button
        type="button"
        variant="invisible"
        size="auto"
        onClick={() => onToggleFavorite(instance)}
        aria-label={t(favorite ? 'serverSelector.unfavoriteName' : 'serverSelector.favoriteName', { name: instance.name })}
        className={cn('rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground', favorite && 'text-status-warning')}
      >
        <Star className={cn('size-4', favorite && 'fill-current')} />
      </Button>
    </div>
  )
}

function buildRows(
  instances: SelectorInstance[],
  groupBy: GroupBy,
  nodeNames: Map<number, string>,
  t: (key: string, options?: Record<string, unknown>) => string,
): SelectorRow[] {
  const groups = new Map<string, { label: string; items: SelectorInstance[] }>()
  for (const instance of instances) {
    const key = groupBy === 'node' ? String(instance.nodeId) : instance.status
    const label = groupBy === 'node' ? nodeNames.get(instance.nodeId) ?? t('console.unknownNode', { id: instance.nodeId }) : instance.status
    const group = groups.get(key) ?? { label, items: [] }
    group.items.push(instance)
    groups.set(key, group)
  }

  const rows: SelectorRow[] = []
  for (const [key, group] of [...groups.entries()].sort((a, b) => a[1].label.localeCompare(b[1].label))) {
    rows.push({ kind: 'group', key: `group:${groupBy}:${key}`, label: group.label, count: group.items.length })
    for (const instance of group.items) rows.push({ kind: 'instance', key: `instance:${instance.id}`, instance })
  }
  return rows
}
