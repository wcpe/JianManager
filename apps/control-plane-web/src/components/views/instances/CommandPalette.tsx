import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Box, CornerDownLeft, Network, Search, Server, Terminal } from 'lucide-react'

import { cn } from '@jianmanager/ui'
import { instanceStatusLevel } from '@jianmanager/ui'
import { searchPalette, type PaletteEntry, type PaletteSources } from '@/lib/command-palette'

/** kind → 行首图标。 */
const KIND_ICON = {
  instance: Box,
  node: Server,
  page: Network,
  command: Terminal,
} as const

/**
 * 全局命令面板（FR-241，导航外壳 v2 Part A）：Ctrl/⌘+K 或点页眉搜索框打开，
 * 单输入框检索实例/节点/页面/操作并跳转或执行。键盘 ↑↓ 选择、Enter 执行、Esc 关。
 * 实例结果消费 FR-247 服务端搜索；纯节点/页面/操作匹配逻辑下沉 `lib/command-palette`。
 *
 * 受控视图（ADR-097 a 范式）：不取数、不挂全局监听——开合、四类数据源与「执行结果」
 * 都由外壳注入（全局 Ctrl+K 监听也在外壳）。输入态、键盘导航、滚动定位与预取时机留在视图内。
 */
export interface CommandPaletteProps {
  /** 面板开合（外壳持有，并负责全局 Ctrl/⌘+K 与 Esc 的切换）。 */
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 检索词（外壳持有：它同时是实例服务端搜索的入参，视图拿不到）。 */
  query: string
  onQueryChange: (q: string) => void
  /** 可检索的页面（外壳从导航配置取）。 */
  pages: { to: string; label: string }[]
  /** 可检索的操作（静态，执行副作用而非跳转）。 */
  commands: { id: string; label: string }[]
  /** 实例候选（外壳按服务端搜索注入）。 */
  instances: PaletteSources['instances']
  /** 节点候选。 */
  nodes: PaletteSources['nodes']
  /** 页眉节点作用域：只收敛实例结果，节点本身仍全局可搜。 */
  nodeScopeId?: number | null
  /** 实例搜索中（仅用于「无结果时的加载态」判定）。 */
  searching?: boolean
  /** 执行一条结果（外壳负责跳转与副作用）。 */
  onSelect: (entry: PaletteEntry) => void
  /** 预取路由 chunk（键盘选中页面时同样预取，见下方注释）。 */
  onPrefetchRoute?: (to: string) => void
}

export default function CommandPalette({
  open,
  onOpenChange,
  query,
  onQueryChange,
  pages,
  commands,
  instances,
  nodes,
  nodeScopeId,
  searching = false,
  onSelect,
  onPrefetchRoute,
}: CommandPaletteProps) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)

  const entries = useMemo(
    () => searchPalette(query, { instances, nodes, pages, commands, nodeScopeId }),
    [query, instances, nodes, pages, commands, nodeScopeId],
  )

  // selected 在渲染期 clamp，避免结果变化后越界（不在 effect 里 setState）。
  const activeIndex = entries.length === 0 ? -1 : Math.min(selected, entries.length - 1)

  // FR-496 阶段 6 补丁：键盘用户不会 hover，但面板里「当前选中项」是等价的导航意图
  // （Enter 即跳转），故选中项是页面时同样预取目标 chunk。仅面板打开时生效；
  // prefetchRoute 幂等，连续输入不会重复请求同一个路由。
  useEffect(() => {
    if (!open || activeIndex < 0) return
    const target = entryRouteTarget(entries[activeIndex])
    if (target) onPrefetchRoute?.(target)
  }, [open, entries, activeIndex, onPrefetchRoute])

  const close = () => {
    onOpenChange(false)
    onQueryChange('')
    setSelected(0)
  }

  const run = (entry: PaletteEntry) => {
    onSelect(entry)
    close()
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault()
      close()
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      setSelected((s) => Math.min(s + 1, entries.length - 1))
      scrollIntoView(listRef.current, activeIndex + 1)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setSelected((s) => Math.max(s - 1, 0))
      scrollIntoView(listRef.current, activeIndex - 1)
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (activeIndex >= 0) run(entries[activeIndex])
    }
  }

  if (!open) return null

  return (
    <div
      className="fixed inset-0 z-[60] flex items-start justify-center bg-black/50 p-4 pt-[14vh]"
      onClick={close}
      role="presentation"
    >
      <div
        className="flex max-h-[60vh] w-full max-w-xl flex-col overflow-hidden rounded-xl border bg-card text-card-foreground shadow-lift"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal
        aria-label={t('palette.title')}
      >
        <div className="flex items-center gap-2 border-b px-3">
          <Search className="size-4 shrink-0 text-muted-foreground" />
          <input
            ref={inputRef}
            autoFocus
            value={query}
            onChange={(e) => {
              onQueryChange(e.target.value)
              setSelected(0)
            }}
            onKeyDown={onKeyDown}
            placeholder={t('palette.placeholder')}
            aria-label={t('palette.placeholder')}
            className="h-11 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
          />
          <kbd className="hidden shrink-0 rounded border bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground sm:inline-block">
            Esc
          </kbd>
        </div>

        <div ref={listRef} className="min-h-0 flex-1 overflow-y-auto p-1.5">
          {searching && entries.length === 0 ? (
            <p className="px-3 py-6 text-center text-sm text-muted-foreground">{t('common.loading')}</p>
          ) : entries.length === 0 ? (
            <p className="px-3 py-6 text-center text-sm text-muted-foreground">{t('palette.empty')}</p>
          ) : (
            entries.map((entry, i) => {
              const Icon = KIND_ICON[entry.kind]
              const active = i === activeIndex
              // 页面类条目在悬停/聚焦时预取其 chunk（FR-496 阶段 6 补丁）：点下去时模块已在缓存。
              const target = entryRouteTarget(entry)
              const intentProps = target && onPrefetchRoute
                ? { onMouseEnter: () => onPrefetchRoute(target), onFocus: () => onPrefetchRoute(target) }
                : {}
              return (
                <button
                  key={entry.key}
                  type="button"
                  data-palette-row={i}
                  {...intentProps}
                  onMouseMove={() => setSelected(i)}
                  onClick={() => run(entry)}
                  className={cn(
                    'flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm transition-colors',
                    active ? 'bg-accent text-foreground' : 'text-foreground/80',
                  )}
                >
                  {entry.kind === 'instance' ? (
                    <span
                      className="size-2 shrink-0 rounded-full"
                      style={{ backgroundColor: `var(--status-${statusColor(entry.status)})` }}
                      aria-hidden
                    />
                  ) : (
                    <Icon className="size-4 shrink-0 text-muted-foreground" />
                  )}
                  <span className="min-w-0 flex-1 truncate">{entry.label}</span>
                  {entry.sublabel && (
                    <span className="shrink-0 truncate text-[11px] text-muted-foreground">{entry.sublabel}</span>
                  )}
                  <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                    {t(`palette.kind.${entry.kind}`)}
                  </span>
                  {active && <CornerDownLeft className="size-3.5 shrink-0 text-muted-foreground" />}
                </button>
              )
            })
          )}
        </div>
      </div>
    </div>
  )
}

/** 实例状态 → status 变量后缀（命中阈值色）。neutral 归 info 以有可见色点。 */
function statusColor(status?: string): string {
  const level = instanceStatusLevel(status ?? '')
  return level === 'neutral' ? 'info' : level
}

/**
 * 条目对应的路由目标（FR-496 阶段 6 补丁）：只有页面类条目才是路由，
 * 实例/节点/操作类的 key 是 id 或动作名，预取没有意义（它们各自的预取在别处）。
 */
function entryRouteTarget(entry: PaletteEntry | undefined): string | null {
  if (!entry || entry.kind !== 'page') return null
  return entry.key.slice(entry.kind.length + 1)
}

/** 把第 idx 行滚入可视区（键盘移动时）。越界/无元素则忽略。 */
function scrollIntoView(container: HTMLElement | null, idx: number): void {
  if (!container || idx < 0) return
  const row = container.querySelector<HTMLElement>(`[data-palette-row="${idx}"]`)
  row?.scrollIntoView({ block: 'nearest' })
}
