import { ChevronRightIcon, SearchIcon } from 'lucide-react'
import * as React from 'react'

import { cn } from '../../lib/utils'

/**
 * 资源状态三态（原型 `.dot` / `.dot.warn` / `.dot.bad`）。
 * `muted` 对应原型的 `.dot.gray`——已停止、状态未知这类「非异常但也不健康」的实例，
 * 若强行归入三态会显示成绿色，等于把停机实例谎报成健康。
 */
export type ResourceStatus = 'normal' | 'warn' | 'bad' | 'muted'

/** 资源树里的实例行数据。 */
export type ResourceInstance = {
  id: string
  name: string
  status: ResourceStatus
}

/** 资源树里的节点行数据（节点或群组）。 */
export type ResourceNode = {
  id: string
  name: string
  status: ResourceStatus
  /** 该节点的实例总数；缺省用 `instances.length`。它同时用于节点行徽章与「其余 N 个」的计数。 */
  instanceCount?: number
  /** `instances` 之外**未随本次数据下发**的实例数（默认 0）。已用 `instanceCount` 表达总数时不要重复传。 */
  hiddenCount?: number
  /** 已下发的实例；超出 `visibleLimit` 的部分由组件折叠。 */
  instances: ResourceInstance[]
}

const STATUS_DOT: Record<ResourceStatus, string> = {
  normal: 'bg-status-success',
  warn: 'bg-status-warning',
  bad: 'bg-status-danger',
  muted: 'bg-muted-foreground/50',
}

/**
 * 资源树（FR-496 阶段 4）：侧栏里的「节点 → 实例」两级导航。
 *
 * **它是原型解决「侧栏被路由占满」的核心手段之一**：侧栏不再罗列路由，而是罗列资源；
 * 同时每个节点展开后**默认只列前 7 个实例**，其余折叠成「查看其余 N 个实例」去往
 * 过滤后的实例列表页。理由很直接——一个节点上可能挂着上百个实例，全部铺开会把
 * 侧栏变成一条几百行的列表，既滚不到底，也让真正需要的定位入口被淹没。
 * 7 是原型的取值（`.slice(0,7)`），这里通过 `visibleLimit` 保留可调。
 *
 * 搜索过滤沿用原型的两条细节：
 *   1. 节点名命中时该节点的实例**全部**保留（不会出现「搜到了节点却看不到它下面的实例」）；
 *   2. 搜索状态下节点自动展开——用户已经在缩小范围了，不该再要求他手动点开。
 */
export function ResourceTree({
  nodes,
  visibleLimit = 7,
  query,
  onQueryChange,
  onSelectNode,
  onSelectInstance,
  onShowAll,
  searchPlaceholder = '定位节点或实例…',
  emptyText = '没有匹配资源',
  className,
  ...props
}: React.ComponentProps<'div'> & {
  nodes: ResourceNode[]
  /** 每个节点默认列出的实例条数；原型取 7。 */
  visibleLimit?: number
  /** 搜索词（受控）。传入但未传 `onQueryChange` 时只做过滤、不渲染输入框，便于由外部搜索框驱动。 */
  query?: string
  /** 传入时在树上方渲染搜索框。 */
  onQueryChange?: (query: string) => void
  onSelectNode?: (node: ResourceNode) => void
  onSelectInstance?: (instance: ResourceInstance, node: ResourceNode) => void
  /** 点击「查看其余 N 个实例」的回调（通常是跳到过滤后的实例列表页）。 */
  onShowAll?: (node: ResourceNode) => void
  searchPlaceholder?: string
  emptyText?: string
}) {
  const [openIds, setOpenIds] = React.useState<readonly string[]>([])

  const toggle = React.useCallback((id: string) => {
    setOpenIds((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]))
  }, [])

  const q = (query ?? '').trim().toLowerCase()

  const rows = nodes
    .map((node) => {
      const matched = q
        ? node.instances.filter(
            (instance) => instance.name.toLowerCase().includes(q) || node.name.toLowerCase().includes(q),
          )
        : node.instances
      const listed = matched.slice(0, visibleLimit)
      // 计数口径分两种：无搜索时用节点实例总数（可能大于已下发条数，链接要如实反映服务器上还有多少）；
      // 有搜索时只对当前匹配到的条数计数，否则会出现「搜出 3 条、却提示还有 97 条」的噪声。
      const total = q ? matched.length : (node.instanceCount ?? node.instances.length)
      const rest = Math.max(0, total - listed.length) + (q ? 0 : (node.hiddenCount ?? 0))
      return { node, listed, rest, open: q ? true : openIds.includes(node.id), visible: matched.length > 0 }
    })
    .filter((row) => row.visible)

  return (
    <div
      data-slot="resource-tree"
      data-query={q || undefined}
      className={cn('flex min-h-0 flex-1 flex-col', className)}
      {...props}
    >
      {onQueryChange && (
        <div
          data-slot="resource-tree-search"
          className="mx-[14px] mb-[9px] flex min-h-8 shrink-0 items-center gap-[7px] rounded-md border border-border bg-card px-[9px] text-muted-foreground"
        >
          <SearchIcon className="size-[13px] shrink-0" aria-hidden />
          <input
            value={query ?? ''}
            onChange={(event) => onQueryChange(event.target.value)}
            placeholder={searchPlaceholder}
            aria-label="搜索资源树"
            className="w-full border-0 bg-transparent py-1.5 text-[11px] text-foreground outline-none placeholder:text-muted-foreground"
          />
        </div>
      )}

      {/* 滚动只发生在这里：搜索框必须常驻（原型 .resource-search 位于 .resource-scroll 之外）。 */}
      <div data-slot="resource-tree-list" className="min-h-0 flex-1 overflow-auto px-[9px] pb-[14px]">
        {rows.length === 0 ? (
          <p data-slot="resource-tree-empty" className="px-[10px] py-[10px] text-[11px] text-muted-foreground">
            {emptyText}
          </p>
        ) : (
          rows.map(({ node, listed, rest, open }) => (
            <div key={node.id} data-slot="resource-tree-node" data-open={open || undefined} className="mb-[2px]">
              <div className="flex h-[33px] items-center gap-[6px] rounded-[5px] px-1 transition-colors duration-[var(--motion-duration-normal)] ease-ios hover:bg-muted">
                <button
                  type="button"
                  data-slot="resource-tree-toggle"
                  aria-expanded={open}
                  aria-label={open ? `收起 ${node.name}` : `展开 ${node.name}`}
                  onClick={() => toggle(node.id)}
                  className="flex h-[26px] w-[22px] shrink-0 items-center p-[3px] text-muted-foreground/70 transition-colors duration-[var(--motion-duration-normal)] ease-ios hover:text-foreground"
                >
                  <ChevronRightIcon
                    className={cn(
                      'size-3 transition-transform duration-[var(--motion-duration-normal)] ease-ios',
                      open && 'rotate-90',
                    )}
                  />
                </button>
                <ResourceStatusDot status={node.status} />
                <button
                  type="button"
                  data-slot="resource-tree-node-link"
                  onClick={() => onSelectNode?.(node)}
                  className="min-w-0 flex-1 truncate text-left text-[11px] font-[570]"
                >
                  {node.name}
                </button>
                <span
                  data-slot="resource-tree-node-count"
                  className="min-w-[24px] shrink-0 text-right font-mono text-[10px] text-muted-foreground/70"
                >
                  {node.instanceCount ?? node.instances.length}
                </span>
              </div>

              {open && (
                <div data-slot="resource-tree-children" className="ml-[17px] border-l border-border/60 pl-2">
                  {listed.map((instance) => (
                    <ResourceInstanceRow
                      key={instance.id}
                      instance={instance}
                      node={node}
                      onSelect={onSelectInstance}
                    />
                  ))}
                  {rest > 0 && (
                    <button
                      type="button"
                      data-slot="resource-tree-more"
                      onClick={() => onShowAll?.(node)}
                      className="block px-2 py-1.5 text-left text-[10px] text-status-info transition-colors duration-[var(--motion-duration-normal)] ease-ios hover:text-primary"
                    >
                      查看其余 {rest} 个实例
                    </button>
                  )}
                </div>
              )}
            </div>
          ))
        )}
      </div>
    </div>
  )
}

/** 状态点。三态由 `data-status` 暴露，颜色映射集中在此，避免各处自行拼状态色。 */
function ResourceStatusDot({ status }: { status: ResourceStatus }) {
  return (
    <span
      data-slot="resource-status-dot"
      data-status={status}
      aria-hidden
      className={cn('size-1.5 shrink-0 rounded-full', STATUS_DOT[status])}
    />
  )
}

/** 实例行。名称可截断（`truncate`），完整名称保留在 `title` 里。 */
function ResourceInstanceRow({
  instance,
  node,
  onSelect,
}: {
  instance: ResourceInstance
  node: ResourceNode
  onSelect?: (instance: ResourceInstance, node: ResourceNode) => void
}) {
  return (
    <button
      type="button"
      data-slot="resource-tree-instance"
      data-status={instance.status}
      title={`${instance.name} · ${node.name}`}
      onClick={() => onSelect?.(instance, node)}
      className={cn(
        'flex min-h-[30px] w-full items-center gap-[7px] rounded-[4px] px-[6px] py-1 text-left text-[11px] text-muted-foreground',
        'transition-colors duration-[var(--motion-duration-normal)] ease-ios hover:bg-muted hover:text-foreground',
      )}
    >
      <ResourceStatusDot status={instance.status} />
      <span className="truncate">{instance.name}</span>
    </button>
  )
}
