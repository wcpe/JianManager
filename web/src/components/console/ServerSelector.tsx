import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { Search, Server, Star, X } from 'lucide-react'

import { useInstanceAggregate, useSearchInstances, type InstanceInfo } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useConsoleStore } from '@/stores/console'
import { useVirtualRows } from '@/lib/virtual-list'
import { cn } from '@jianmanager/ui'

const RECENT_KEY = 'server-selector.recent'
const FAVORITES_KEY = 'server-selector.favorites'
const PAGE_SIZE = 200
const ROW_HEIGHT = 36

interface StoredInstance {
  id: number
  uuid: string
  nodeId: number
  name: string
  status: string
}

type GroupBy = 'node' | 'status'
type SelectorRow =
  | { kind: 'group'; key: string; label: string; count: number }
  | { kind: 'instance'; key: string; instance: InstanceInfo }

/** 导航外壳服务器选择器（FR-240）：服务端搜索 + 聚合计数 + 虚拟渲染 + 最近/收藏。 */
export default function ServerSelector() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const selectedNodeId = useConsoleStore((s) => s.selectedNodeId)
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [groupBy, setGroupBy] = useState<GroupBy>('node')
  const [recent, setRecent] = useState<StoredInstance[]>(() => loadStored(RECENT_KEY))
  const [favorites, setFavorites] = useState<StoredInstance[]>(() => loadStored(FAVORITES_KEY))
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
  const nodeNames = useMemo(() => new Map(nodes.map((node) => [node.id, node.name])), [nodes])
  const rows = useMemo(() => buildRows(searchResult?.items ?? [], groupBy, nodeNames, t), [searchResult, groupBy, nodeNames, t])
  const favoriteIds = useMemo(() => new Set(favorites.map((item) => item.id)), [favorites])
  const totalCount = aggregate?.total ?? searchResult?.total ?? 0
  const showQuickLists = trimmed.length === 0

  const { containerRef, onScroll, range, totalSize } = useVirtualRows({
    total: rows.length,
    itemSize: ROW_HEIGHT,
    overscan: 8,
    fallbackViewportSize: 360,
  })
  const visibleRows = rows.slice(range.start, range.end)

  function openInstance(instance: InstanceInfo | StoredInstance) {
    const stored = toStored(instance)
    const next = upsertStored(recent, stored, 8)
    setRecent(next)
    saveStored(RECENT_KEY, next)
    setOpen(false)
    navigate(`/instances/${instance.id}`)
  }

  function toggleFavorite(instance: InstanceInfo | StoredInstance) {
    const exists = favoriteIds.has(instance.id)
    const next = exists
      ? favorites.filter((item) => item.id !== instance.id)
      : upsertStored(favorites, toStored(instance), 32)
    setFavorites(next)
    saveStored(FAVORITES_KEY, next)
  }

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        aria-label={t('serverSelector.open')}
        className="flex w-full items-center gap-2 rounded-md border bg-card/80 px-2.5 py-2 text-left text-[13px] text-foreground shadow-soft transition-colors hover:bg-accent/60"
      >
        <Server className="size-4 shrink-0 text-primary" />
        <span className="min-w-0 flex-1 truncate">{t('serverSelector.open')}</span>
      </button>

      {open && (
        <div className="fixed inset-0 z-[58] flex items-start justify-center bg-black/45 p-4 pt-[10vh]" role="presentation" onClick={() => setOpen(false)}>
          <div
            role="dialog"
            aria-modal="true"
            aria-label={t('serverSelector.title')}
            className="flex max-h-[76vh] w-full max-w-3xl flex-col overflow-hidden rounded-xl border bg-card text-card-foreground shadow-lift"
            onClick={(event) => event.stopPropagation()}
          >
            <div className="flex items-center gap-2 border-b px-3 py-2">
              <Server className="size-4 text-primary" />
              <div className="min-w-0 flex-1">
                <h2 className="truncate text-sm font-semibold">{t('serverSelector.title')}</h2>
                <p className="truncate text-xs text-muted-foreground">{t('serverSelector.subtitle', { count: totalCount })}</p>
              </div>
              <button type="button" onClick={() => setOpen(false)} aria-label={t('common.close')} className="rounded p-1.5 text-muted-foreground hover:bg-accent/60 hover:text-foreground">
                <X className="size-4" />
              </button>
            </div>

            <div className="space-y-2 border-b p-3">
              <label className="flex h-9 items-center gap-2 rounded-md border bg-background px-2.5 text-sm shadow-soft">
                <Search className="size-4 shrink-0 text-muted-foreground" />
                <input
                  type="search"
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                  aria-label={t('serverSelector.search')}
                  placeholder={t('serverSelector.search')}
                  className="min-w-0 flex-1 bg-transparent outline-none placeholder:text-muted-foreground"
                />
              </label>
              <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label={t('grouping.groupBy')}>
                {(['node', 'status'] as const).map((dim) => (
                  <button
                    key={dim}
                    type="button"
                    onClick={() => setGroupBy(dim)}
                    className={cn(
                      'rounded-md px-2 py-1 text-xs transition-colors',
                      groupBy === dim ? 'bg-accent font-medium text-primary' : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground',
                    )}
                  >
                    {t(`serverSelector.group_${dim}`)}
                  </button>
                ))}
                <span className="ml-auto text-xs text-muted-foreground">{t('serverSelector.loaded', { count: searchResult?.items.length ?? 0 })}</span>
              </div>
            </div>

            <div className="min-h-0 flex-1 overflow-hidden p-3">
              {showQuickLists && (recent.length > 0 || favorites.length > 0) && (
                <div className="mb-3 grid gap-2 md:grid-cols-2">
                  <QuickList title={t('serverSelector.recent')} items={recent} favoriteIds={favoriteIds} onOpen={openInstance} onToggleFavorite={toggleFavorite} />
                  <QuickList title={t('serverSelector.favorites')} items={favorites} favoriteIds={favoriteIds} onOpen={openInstance} onToggleFavorite={toggleFavorite} />
                </div>
              )}

              {isLoading || (isFetching && rows.length === 0) ? (
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
                            onOpen={openInstance}
                            onToggleFavorite={toggleFavorite}
                          />
                        ),
                      )}
                    </div>
                  </div>
                </div>
              )}
            </div>
          </div>
        </div>
      )}
    </>
  )
}

function QuickList({
  title,
  items,
  favoriteIds,
  onOpen,
  onToggleFavorite,
}: {
  title: string
  items: StoredInstance[]
  favoriteIds: Set<number>
  onOpen: (instance: StoredInstance) => void
  onToggleFavorite: (instance: StoredInstance) => void
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
  onOpen,
  onToggleFavorite,
}: {
  instance: StoredInstance
  favorite: boolean
  onOpen: (instance: StoredInstance) => void
  onToggleFavorite: (instance: StoredInstance) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="flex items-center gap-1 rounded-md hover:bg-accent/50">
      <button type="button" onClick={() => onOpen(instance)} className="min-w-0 flex-1 px-2 py-1.5 text-left text-sm">
        <span className="block truncate">{instance.name}</span>
        <span className="block truncate text-[11px] text-muted-foreground">{instance.status}</span>
      </button>
      <button
        type="button"
        onClick={() => onToggleFavorite(instance)}
        aria-label={t(favorite ? 'serverSelector.unfavoriteName' : 'serverSelector.favoriteName', { name: instance.name })}
        className={cn('rounded p-1.5 text-muted-foreground hover:bg-accent hover:text-foreground', favorite && 'text-status-warning')}
      >
        <Star className={cn('size-4', favorite && 'fill-current')} />
      </button>
    </div>
  )
}

function InstanceRow({
  instance,
  favorite,
  nodeName,
  onOpen,
  onToggleFavorite,
}: {
  instance: InstanceInfo
  favorite: boolean
  nodeName: string | undefined
  onOpen: (instance: InstanceInfo) => void
  onToggleFavorite: (instance: InstanceInfo) => void
}) {
  const { t } = useTranslation()
  return (
    <div data-testid="server-selector-row" className="flex h-9 items-center gap-2 border-t px-2 text-sm hover:bg-accent/50">
      <button type="button" onClick={() => onOpen(instance)} className="min-w-0 flex flex-1 items-center gap-2 text-left">
        <span className="size-2 shrink-0 rounded-full bg-primary" />
        <span className="min-w-0 flex-1 truncate">{instance.name}</span>
        <span className="hidden shrink-0 text-[11px] text-muted-foreground sm:inline">{nodeName ?? `#${instance.nodeId}`}</span>
        <span className="shrink-0 text-[11px] text-muted-foreground">{instance.status}</span>
      </button>
      <button
        type="button"
        onClick={() => onToggleFavorite(instance)}
        aria-label={t(favorite ? 'serverSelector.unfavoriteName' : 'serverSelector.favoriteName', { name: instance.name })}
        className={cn('rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground', favorite && 'text-status-warning')}
      >
        <Star className={cn('size-4', favorite && 'fill-current')} />
      </button>
    </div>
  )
}

function buildRows(
  instances: InstanceInfo[],
  groupBy: GroupBy,
  nodeNames: Map<number, string>,
  t: (key: string, options?: Record<string, unknown>) => string,
): SelectorRow[] {
  const groups = new Map<string, { label: string; items: InstanceInfo[] }>()
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

function toStored(instance: InstanceInfo | StoredInstance): StoredInstance {
  return { id: instance.id, uuid: instance.uuid, nodeId: instance.nodeId, name: instance.name, status: instance.status }
}

function upsertStored(list: StoredInstance[], item: StoredInstance, limit: number): StoredInstance[] {
  return [item, ...list.filter((x) => x.id !== item.id)].slice(0, limit)
}

function loadStored(key: string): StoredInstance[] {
  if (typeof localStorage === 'undefined') return []
  try {
    const value: unknown = JSON.parse(localStorage.getItem(key) ?? '[]')
    if (!Array.isArray(value)) return []
    return value.filter(isStoredInstance)
  } catch {
    return []
  }
}

function saveStored(key: string, value: StoredInstance[]): void {
  if (typeof localStorage === 'undefined') return
  localStorage.setItem(key, JSON.stringify(value))
}

function isStoredInstance(value: unknown): value is StoredInstance {
  if (!value || typeof value !== 'object') return false
  const item = value as Partial<StoredInstance>
  return typeof item.id === 'number' && typeof item.name === 'string' && typeof item.uuid === 'string' && typeof item.nodeId === 'number' && typeof item.status === 'string'
}
