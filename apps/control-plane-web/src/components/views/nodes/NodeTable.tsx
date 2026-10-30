import { useTranslation } from 'react-i18next'
import { Box, ArrowUpDown, Network } from 'lucide-react'
import { cloneElement, Fragment, useEffect, useRef, useState, type ReactElement, type ReactNode } from 'react'

import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import { ColumnResizeHandle } from '@jianmanager/ui/components/column-resize-handle'
import { NativeSelect } from '@jianmanager/ui/components/native-select'
import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { nodeStatusLevel } from '@/lib/nodes/node-list'
import type { NodeInfo } from '@/lib/nodes/node-types'
import {
  NODE_COLUMN_WIDTH,
  NODE_DEFAULT_ORDER,
  NODE_PAGE_SIZES,
  NODE_PINNED_COLUMNS,
  type NodeColumnKey,
  type NodeColumnWidths,
} from '@/lib/nodes/node-columns'

/**
 * 节点列表（默认视图）。
 *
 * 【为什么需要它】节点页原来只有卡片网格（`CardsGrid` 固定 3 列）。实测 2 个节点时：
 * 卡片各 314×117px、页面占约 179px，**视口 900px 里约 560px（62%）是空白**；
 * 而卡片只用了 `NodeInfo` 二十多个字段里的 4 个（IP / 核数 / 内存 / 磁盘）。
 * 列表视图一是把密度提上来（一屏 10+ 行），二是把已有字段真正摆出来。
 *
 * 【为什么不复用实例表】两者语义不同（实例有生命周期与角色，节点是机器）。
 * 强行共用列模型会让「加一列」牵动另一个页面，反而更脆弱；共用的是**交互范式**
 * （排序走 URL、离线态表达一致），而不是数据结构。
 */
export type NodeSortKey =
  | 'name'
  | 'status'
  | 'cpu'
  | 'memory'
  | 'disk'
  | 'instances'
  /** 「异常优先」：离线 → 维护 → 高负载 → 其余。节点多时默认该看这个。 */
  | 'attention'
export type NodeSortOrder = 'asc' | 'desc'

/** 心跳超过这个时长视为「可能失联」（节点侧通常 15s 一报）。 */
const HEARTBEAT_STALE_MS = 90_000

/** 相对时间：只到「秒/分/时/天」，够判断新鲜度，不引入日期库。 */
function relativeTime(iso: string | null, now: number): string | null {
  if (!iso) return null
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return null
  const diff = Math.max(0, now - t)
  const sec = Math.round(diff / 1000)
  if (sec < 60) return `${sec}s`
  const min = Math.round(sec / 60)
  if (min < 60) return `${min}m`
  const hour = Math.round(min / 60)
  if (hour < 24) return `${hour}h`
  return `${Math.round(hour / 24)}d`
}

function SortableHead({
  label,
  sortValue,
  currentSort,
  currentOrder,
  onSort,
  align = 'left',
  children,
  ...rest
}: {
  label: string
  sortValue: NodeSortKey
  currentSort: NodeSortKey | null
  currentOrder: NodeSortOrder
  onSort: (key: NodeSortKey) => void
  align?: 'left' | 'right'
  /** 表头额外内容（列宽手柄由 DraggableNodeHead 注入）。 */
  children?: ReactNode
} & Omit<React.ComponentProps<typeof TableHead>, 'children'>) {
  const { t } = useTranslation()

  const active = currentSort === sortValue
  const orderLabel = currentOrder === 'desc' ? t('grouping.orderDesc') : t('grouping.orderAsc')
  return (
    /*
      `{...rest}` 必须透传：拖拽换位靠 `cloneElement` 往这个 th 上合并
      `data-column-key` / `draggable` / `onDragStart`。不透传的话可排序列会丢掉这些属性，
      表现为「只有不可排序的列能拖」（实例页踩过同一个坑）。
    */
    <TableHead aria-sort={active ? (currentOrder === 'desc' ? 'descending' : 'ascending') : 'none'} {...rest}>
      <Button
        type="button"
        variant="invisible"
        size="auto"
        onClick={() => onSort(sortValue)}
        aria-label={
          active
            ? t('grouping.sortHeaderActive', { label, order: orderLabel })
            : t('grouping.sortHeader', { label })
        }
        className={cn(
          'inline-flex items-center gap-1 rounded px-1 py-0.5 text-[11px] font-semibold transition-colors hover:bg-accent/60',
          align === 'right' && 'flex-row-reverse',
        )}
      >
        <span>{label}</span>
        <ArrowUpDown
          className={cn('size-3.5', active ? 'text-primary' : 'text-muted-foreground/60')}
          aria-hidden="true"
        />
      </Button>
      {children}
    </TableHead>
  )
}

/** 表头拖拽用的自定义 MIME：只接受本页发起的列拖拽，不误收外部拖入的文件/文本。 */
const COLUMN_DRAG_MIME = 'application/x-jm-node-column'

/**
 * 可拖拽的表头单元格。
 *
 * 关键点：`children` 已经是 `<TableHead>`（由 `renderHead` 产出），这里用 `cloneElement`
 * 把拖拽属性**合并进那唯一一个 th**，而不是再包一层 —— 包一层就会出现「一列两个 th」，
 * 表头列数翻倍、与 tbody 对不上（实例页踩过：14 个 th 对 8 列）。
 */
function DraggableNodeHead({
  columnKey,
  draggable,
  draggingKey,
  onDragStart,
  onDragEnd,
  onDropColumn,
  resizable,
  resizeLabel,
  resizeTitle,
  onResize,
  onResizeEnd,
  onResetWidth,
  children,
}: {
  columnKey: NodeColumnKey
  draggable: boolean
  draggingKey: NodeColumnKey | null
  onDragStart: (key: NodeColumnKey) => void
  onDragEnd: () => void
  onDropColumn: (key: NodeColumnKey) => void
  resizable?: boolean
  resizeLabel?: string
  resizeTitle?: string
  onResize?: (width: number) => void
  onResizeEnd?: () => void
  onResetWidth?: () => void
  children: ReactElement
}) {
  const [over, setOver] = useState(false)
  const headProps = children.props as { className?: string; children?: ReactNode }
  const headChildren = resizable && onResize ? (
    <>
      {headProps.children}
      <ColumnResizeHandle
        testId={`node-col-resize-${columnKey}`}
        label={resizeLabel ?? '调整列宽'}
        title={resizeTitle ?? '拖动调整列宽'}
        onResize={onResize}
        onResizeEnd={onResizeEnd}
        onReset={onResetWidth}
      />
    </>
  ) : (
    headProps.children
  )
  return cloneElement(children as ReactElement<Record<string, unknown>>, {
    'data-column-key': columnKey,
    'data-dragging': draggingKey === columnKey ? 'true' : undefined,
    children: headChildren,
    draggable: draggable || undefined,
    onDragStart: draggable
      ? (event: React.DragEvent) => {
          // 只带 key：列是可重排的语义单元，搬 DOM 片段没有意义。
          event.dataTransfer.setData(COLUMN_DRAG_MIME, columnKey)
          event.dataTransfer.effectAllowed = 'move'
          onDragStart(columnKey)
        }
      : undefined,
    onDragEnd: draggable
      ? () => {
          setOver(false)
          onDragEnd()
        }
      : undefined,
    onDragOver: draggable
      ? (event: React.DragEvent) => {
          // 必须 preventDefault 才会触发 drop。
          event.preventDefault()
          event.dataTransfer.dropEffect = 'move'
          setOver(true)
        }
      : undefined,
    onDragLeave: draggable ? () => setOver(false) : undefined,
    onDrop: draggable
      ? (event: React.DragEvent) => {
          event.preventDefault()
          setOver(false)
          onDropColumn(columnKey)
        }
      : undefined,
    className: cn(
      headProps.className,
      'select-none',
      resizable && 'relative',
      draggable && 'cursor-grab active:cursor-grabbing',
      draggingKey === columnKey && 'opacity-40',
      // 插入位提示：左侧一条实色竖线（不用背景色，避免与表头 muted 底混淆）
      over && draggingKey !== columnKey && 'shadow-[inset_2px_0_0_0_var(--primary)]',
    ),
  })
}

export function NodeTable({
  nodes,
  instanceCountByNode,
  sortKey,
  sortOrder,
  onSort,
  onOpen,
  /** 列顺序（表头拖拽换位结果）。缺省为默认顺序。 */
  columnOrder = NODE_DEFAULT_ORDER,
  /** 用户拖过的列宽；缺省列走 NODE_COLUMN_WIDTH。 */
  columnWidths,
  /** 列被拖到目标列时回调；省略则整表不可拖拽。 */
  onReorderColumn,
  /** 拖动列宽时实时回调；省略则不渲染列宽手柄。 */
  onColumnResize,
  /** 列宽拖动结束（落存储）。 */
  onColumnResizeEnd,
  /** 双击列宽手柄恢复该列默认宽度。 */
  onResetColumnWidth,
  now: injectedNow,
  selectedIds,
  onToggleRow,
  pagination,
  containerRef,
  emptyKind = 'noNodes',
}: {
  nodes: NodeInfo[]
  instanceCountByNode: Map<number, number>
  sortKey: NodeSortKey | null
  sortOrder: NodeSortOrder
  onSort: (key: NodeSortKey) => void
  onOpen: (id: number) => void
  columnOrder?: readonly NodeColumnKey[]
  columnWidths?: NodeColumnWidths
  onReorderColumn?: (from: NodeColumnKey, to: NodeColumnKey) => void
  onColumnResize?: (key: NodeColumnKey, width: number) => void
  onColumnResizeEnd?: () => void
  onResetColumnWidth?: (key: NodeColumnKey) => void
  /** 注入「当前时间」便于测试相对心跳；缺省取系统时间。 */
  now?: number
  /** 已勾选的节点 id（批量操作）。省略则不显示勾选框。 */
  selectedIds?: ReadonlySet<number>
  /** 勾选/取消一行。 */
  onToggleRow?: (id: number) => void
  /**
   * 表格滚动容器的 ref 回传（动态分页用）。
   * 页面据此测量「一屏能放几行」，再把页大小喂回来——测量与切片在同一处，不会打架。
   */
  containerRef?: React.RefObject<HTMLDivElement | null>
  /**
   * 空态原因：`noMatch` 表示有筛选/搜索条件但没匹配，`noNodes` 表示确实一台都没有。
   * 两者混用会让「搜错字」看起来像「集群里没有节点」。
   */
  emptyKind?: 'noNodes' | 'noMatch'
  /** 分页。省略时不分页、全量渲染。 */
  pagination?: {
    page: number
    pageSize: number
    total: number
    onPageChange: (page: number) => void
    /**
     * 动态分页：容器测得的「一屏能放几行」。
     * 传了它就用它当页大小（覆盖外部 pageSize 的渲染切片由页面负责），
     * 并把它回报给页面，让页面的切片与这里算的一致。
     */
    onDynamicPageSizeChange?: (size: number) => void
    /** 手动选择每页条数；null = 回到自动（按一屏行数）。 */
    onPageSizeChange?: (size: number | null) => void
    /** 当前是否处于自动模式（决定选择器显示「自动」还是具体条数）。 */
    autoSize?: boolean
  }
}) {
  const { t } = useTranslation()
   /* 选中状态由外部持有（批量操作条要跨卡片/表格两视图共用同一份选择）。 */
   const selectable = !!onToggleRow

  /*
    心跳相对时间需要一个「会走的现在」。
    原先写成参数默认值 `now = Date.now()`：那是在渲染期调用不纯函数（同一次渲染可能给出不同值），
    且值不会自己前进——页面停着就一直显示「3s 前」。改为 15s 一跳的 state；
    测试仍可用 `now` 属性注入固定时间。
  */
  const [tickNow, setTickNow] = useState<number | null>(null)
  useEffect(() => {
    const tick = () => setTickNow(Date.now())
    tick()
    const id = setInterval(tick, 15_000)
    return () => clearInterval(id)
  }, [])
  const now = injectedNow ?? tickNow ?? 0


  /*
    表头拖拽换位（用户要求「表头参考服务器列表可以拖拽」）。
    实现与实例页同款：原生 HTML5 DnD + cloneElement 把属性合并到**已有的那一个 th**，
    不另外包一层 TableHead（包了就会出现「一列两个 th」，表头列数翻倍）。
  */
  const [dragging, setDragging] = useState<NodeColumnKey | null>(null)
  /** 源列用 ref 兜底：state 更新异步，极快拖动或同步事件序列里闭包可能读到旧值。 */
  const dragSourceRef = useRef<NodeColumnKey | null>(null)

  const orderedKeys = columnOrder.filter((k) => (NODE_DEFAULT_ORDER as readonly string[]).includes(k))

  /** 列 key → 表头元素（与 colgroup、行单元格共用同一顺序）。 */
  const renderHead = (key: NodeColumnKey) => {
    switch (key) {
      case 'status':
        return <TableHead key={key}>{t('nodes.status')}</TableHead>
      case 'name':
        return (
          <SortableHead key={key} label={t('nodes.name')} sortValue="name" currentSort={sortKey} currentOrder={sortOrder} onSort={onSort} />
        )
      case 'address':
        return <TableHead key={key}>{t('nodes.ip')}</TableHead>
      case 'system':
        return <TableHead key={key}>{t('nodes.system')}</TableHead>
      case 'load':
        return (
          <SortableHead key={key} label={t('nodes.load')} sortValue="cpu" currentSort={sortKey} currentOrder={sortOrder} onSort={onSort} />
        )
      case 'instances':
        return (
          <SortableHead key={key} label={t('nodes.instances')} sortValue="instances" currentSort={sortKey} currentOrder={sortOrder} onSort={onSort} align="right" />
        )
      case 'tunnel':
        return <TableHead key={key}>{t('nodes.tunnel', { defaultValue: '隧道' })}</TableHead>
      case 'heartbeat':
        return <TableHead key={key}>{t('nodes.heartbeat', { defaultValue: '心跳' })}</TableHead>
    }
  }

  return (
    /*
      【高度为什么用视口算式而不是纯 flex-1】页面外壳链上有一层 `display:block` 的
      滚动容器（`jm-workspace-bg`），`flex-1` 到那里就失去参照，PageShell 只剩内容高度，
      表格于是「内容多高就多高」，下方留白。
      这里按视口算出「表格可用高度」：视口 − 顶栏/页头/KPI/图表/工具栏的占位 − 页面内边距。
      数值取实测：1280×900 下表格顶部在 447px（页头+KPI+图表+工具栏），底部还需 20px 呼吸，故 470px
      = 447 + 23。改这个值时**必须实测表格顶部**：算小了会溢出视口，算大了又会退化成下限 240px。
      卡片视图不需要这个约束（它是网格自流），所以只作用在表格容器上。
    */
    <div className="flex min-h-0 flex-1 flex-col gap-2">
    <div
      /*
        不要再加 `flex-1`：父级是 `overflow:hidden` 的 flex 容器，`flex:1 1 0%` 会以
        「父高 - 兄弟」重新分配高度，把显式 height 覆盖掉（实测写了 690px 却渲染 240px）。
        这里只让 height 生效，滚动交给容器自己。
      */
      /*
        【铺满策略】节点页已纳入「固定视口」骨架（Workspace 的 isFixedViewportRoute），
        父级 flex 容器有确定高度，因此这里可以放心用 `flex-1` 吃掉剩余空间：
        表格铺满、页面不滚动，行多时在表格**内部**滚动。
        （早前不能这么做：那时父级是 `display:block` 的滚动容器，`flex-1` 失去参照，
        显式高度又会被 `overflow:hidden` 的 flex 父级压回下限，两头都不可靠。）
      */
      ref={containerRef}
      className="min-h-0 flex-1 overflow-auto rounded-md border"
      data-testid="nodes-table"
    >
      <Table className="table-fixed">
        <colgroup>
          {orderedKeys.map((key) => {
            const width = columnWidths?.[key] ?? NODE_COLUMN_WIDTH[key]
            return <col key={key} style={width ? { width } : undefined} />
          })}
        </colgroup>
        <TableHeader className="border-t border-border/70 bg-muted/25">
          <TableRow>
            {orderedKeys.map((key) => (
              <DraggableNodeHead
                key={key}
                columnKey={key}
                draggable={Boolean(onReorderColumn) && !NODE_PINNED_COLUMNS.includes(key)}
                draggingKey={dragging}
                resizable={Boolean(onColumnResize)}
                resizeLabel={t('instances.resizeColumnHint', { defaultValue: '拖动调整列宽' })}
                resizeTitle={t('instances.resizeColumnTitle', { defaultValue: '拖动调整列宽（双击恢复默认）' })}
                onResize={(width) => onColumnResize?.(key, width)}
                onResizeEnd={onColumnResizeEnd}
                onResetWidth={() => onResetColumnWidth?.(key)}
                onDragStart={(k) => {
                  dragSourceRef.current = k
                  setDragging(k)
                }}
                onDragEnd={() => {
                  dragSourceRef.current = null
                  setDragging(null)
                }}
                onDropColumn={(target) => {
                  const from = dragSourceRef.current ?? dragging
                  if (from && from !== target) onReorderColumn?.(from, target)
                  dragSourceRef.current = null
                  setDragging(null)
                }}
              >
                {renderHead(key)}
              </DraggableNodeHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {nodes.length === 0 ? (
            <TableRow>
              {/* 8 列：空态横跨整行，否则右侧会露出空白列边界。 */}
              <TableCell colSpan={8} className="py-10 text-center text-sm text-muted-foreground">
                {emptyKind === 'noMatch' ? t('nodes.emptyNoMatch', '没有匹配的节点，试试清空搜索或筛选') : t('nodes.empty', '暂无节点')}
              </TableCell>
            </TableRow>
          ) : (
            nodes.map((node) => {
              const online = node.status === 1
              const level = nodeStatusLevel(node.status)
              const statusLabel = node.maintenance
                ? t('nodes.maintenance')
                : online
                  ? t('nodes.online')
                  : t('nodes.offline')
              const dotColor = `var(--status-${level === 'neutral' ? 'info' : level})`
              const hb = relativeTime(node.lastHeartbeat, now)
              const hbStale =
                node.lastHeartbeat != null &&
                now - new Date(node.lastHeartbeat).getTime() > HEARTBEAT_STALE_MS

              const cell_status = (
              <TableCell>
                <div className="flex items-center gap-1.5">
                  {/*
                    勾选框放在状态格里，**不新增一列**：这表的列由列设置/拖拽换位驱动，
                    三处（表头/行/colgroup）各算一遍列数曾经错位过，加列的风险远高于放这里。
                    点击时 stopPropagation，避免与「整行打开详情」冲突。
                  */}
                  {selectable && (
                    <input
                      type="checkbox"
                      className="size-3.5 shrink-0 cursor-pointer accent-[var(--primary)]"
                      checked={selectedIds?.has(node.id) ?? false}
                      onChange={() => onToggleRow?.(node.id)}
                      onClick={(e) => e.stopPropagation()}
                      aria-label={t('nodes.selectNode', { name: node.name })}
                      data-testid={`node-select-${node.id}`}
                    />
                  )}
                  <span
                    className={cn('size-2 shrink-0 rounded-full', online && 'animate-breathing')}
                    style={{ backgroundColor: dotColor, color: dotColor }}
                    aria-hidden
                  />
                  <StatusBadge level={level} label={statusLabel} className="bg-transparent px-0 py-0" />
                </div>
              </TableCell>
              )
              const cell_name = (
              <TableCell className="font-medium">
                <button
                  type="button"
                  onClick={(e) => {
                    // 打开详情是这一格的主行为；stopPropagation 避免与行点击重复触发。
                    e.stopPropagation()
                    onOpen(node.id)
                  }}
                  className="block max-w-full truncate text-left text-[13px] hover:text-primary"
                  title={node.name}
                >
                  {node.name}
                </button>
              </TableCell>
              )
              const cell_address = (
              <TableCell className="truncate text-xs text-muted-foreground">
                <span className="tabular-nums" title={`${node.host}:${node.grpcPort}`}>
                  {node.host}
                </span>
                <span className="text-muted-foreground/60 tabular-nums">:{node.grpcPort}</span>
              </TableCell>
              )
              const cell_system = (
              <TableCell className="truncate text-xs text-muted-foreground" title={`${node.os} / ${node.arch}`}>
                {node.os} / {node.arch}
              </TableCell>
              )
              const cell_load = (
              <TableCell>
                {/*
                  负载列：三条水位 + loadAvg1。
                  离线时不画空轨（与卡片同一判据）——空轨看着像「真的是 0%」，
                  是最没信息量的表达；改为明确的占位短横。
                */}
                <div className="flex items-center gap-2">
                  {online ? (
                    <>
                      <MiniBar value={(node.cpuUsage ?? 0) * 100} className="w-10" />
                      <MiniBar value={(node.memoryUsage ?? 0) * 100} className="w-10" />
                      <MiniBar value={(node.diskUsage ?? 0) * 100} className="w-10" />
                      <span
                        className="tabular-nums text-[11px] text-muted-foreground"
                        title={t('nodes.loadAvgHint', '1 分钟 load average')}
                      >
                        {node.loadAvg1 ? node.loadAvg1.toFixed(2) : '--'}
                      </span>
                    </>
                  ) : (
                    <span className="text-[11px] text-muted-foreground/50" title={t('nodes.noLiveData', '离线，无实时数据')}>
                      --
                    </span>
                  )}
                </div>
              </TableCell>
              )
              const cell_instances = (
              <TableCell className="text-right">
                <span className="inline-flex items-center gap-0.5 text-xs tabular-nums text-muted-foreground">
                  <Box className="size-3" aria-hidden />
                  {instanceCountByNode.get(node.id) ?? 0}
                </span>
              </TableCell>
              )
              const cell_tunnel = (
              <TableCell>
                {/* 反向隧道（ADR-066）：连上意味着 NAT/内网也能下发指令，是运维排障的关键信号。 */}
                {node.tunnelConnected ? (
                  <span className="inline-flex items-center gap-0.5 rounded bg-status-success/15 px-1.5 py-px text-[11px] text-status-success">
                    <Network className="size-3" aria-hidden />
                    {t('nodes.tunnelUp', '已连')}
                  </span>
                ) : (
                  <span className="text-[11px] text-muted-foreground/60">{t('nodes.tunnelDown', '直拨')}</span>
                )}
              </TableCell>
              )
              const cell_heartbeat = (
              <TableCell>
                {/*
                  心跳相对时间：超过 HEARTBEAT_STALE_MS 标红。
                  绝对时间对「还活着吗」这个判断不直观，相对时间才能一眼看出新鲜度。
                */}
                <span
                  className={cn(
                    'text-[11px] tabular-nums',
                    hbStale ? 'text-status-danger' : 'text-muted-foreground',
                  )}
                  title={node.lastHeartbeat ?? undefined}
                >
                  {hb ?? '--'}
                </span>
              </TableCell>
              )
              const CELLS: Record<string, ReactNode> = {
                status: cell_status,
                name: cell_name,
                address: cell_address,
                system: cell_system,
                load: cell_load,
                instances: cell_instances,
                tunnel: cell_tunnel,
                heartbeat: cell_heartbeat,
              }

              return (
                <TableRow
                  key={node.id}
                  data-testid="nodes-table-row"
                  data-node-id={node.id}
                  onClick={() => onOpen(node.id)}
                  className={cn('cursor-pointer border-b border-border/60', !online && 'opacity-70')}
                >
                  {orderedKeys.map((key) => (
                    <Fragment key={key}>{CELLS[key]}</Fragment>
                  ))}
                </TableRow>
              )
            })
          )}
        </TableBody>
      </Table>
    </div>
      {/* 分页条：与服务器列表同一套信息密度与交互（上一页 / 页码 / 下一页）。 */}
      {pagination && (
        <NodePaginationBar
          page={pagination.page}
          pageSize={pagination.pageSize}
          total={pagination.total}
          loaded={nodes.length}
          onPageChange={pagination.onPageChange}
          {...(pagination.onPageSizeChange ? { onPageSizeChange: pagination.onPageSizeChange } : {})}
          {...(pagination.autoSize !== undefined ? { autoSize: pagination.autoSize } : {})}
        />
      )}
    </div>
  )
}

/** 节点分页条（结构对齐服务器页的 InstancePaginationBar）。卡片视图也复用它。 */
export function NodePaginationBar({
  page,
  pageSize,
  total,
  loaded,
  onPageChange,
  onPageSizeChange,
  autoSize,
}: {
  page: number
  pageSize: number
  total: number
  loaded: number
  onPageChange: (page: number) => void
  /** 选择每页条数；传 null 表示回到「按可视行数自动」。省略则不显示选择器。 */
  onPageSizeChange?: (size: number | null) => void
  /** 当前是否处于「自动（按一屏行数）」模式。 */
  autoSize?: boolean
}) {
  const { t } = useTranslation()
  const totalPages = Math.max(1, Math.ceil(total / pageSize))
  // 页码窗口只渲染当前页附近 5 个：节点多时全渲染会把分页条撑成两行。
  const windowStart = Math.max(1, Math.min(page - 2, totalPages - 4))
  const pageNumbers = Array.from({ length: Math.min(5, totalPages) }, (_, i) => windowStart + i)

  return (
    <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
      <span className="tabular-nums" data-testid="nodes-loaded-count">
        {t('nodes.paginationSummary', {
          loaded,
          total,
          page,
          pages: totalPages,
          defaultValue: '已加载 {{loaded}} / 共 {{total}} 个节点，第 {{page}} / {{pages}} 页',
        })}
      </span>
      <div className="flex items-center gap-2">
        {/*
          每页条数：默认「自动」= 按当前可视行数分页（节点页原设计）。
          手动选择后写入 URL，页大小可分享；选回「自动」即清掉该参数。
        */}
        {onPageSizeChange && (
          <label className="flex items-center gap-1 text-[11px] text-muted-foreground">
            {t('nodes.pageSize', '每页')}
            <NativeSelect
              className="h-7 w-20 text-xs"
              value={autoSize ? 'auto' : String(pageSize)}
              onChange={(e) => onPageSizeChange(e.target.value === 'auto' ? null : Number(e.target.value))}
              aria-label={t('nodes.pageSize', '每页')}
              data-testid="nodes-page-size"
            >
              <option value="auto">{t('nodes.pageSizeAuto', '自动')}</option>
              {NODE_PAGE_SIZES.map((size) => (
                <option key={size} value={size}>
                  {size}
                </option>
              ))}
            </NativeSelect>
          </label>
        )}
        <Button type="button" variant="outline" size="sm" disabled={page <= 1} onClick={() => onPageChange(page - 1)}>
          {t('common.prevPage', { defaultValue: '上一页' })}
        </Button>
        {pageNumbers.map((p) => (
          <Button
            key={p}
            type="button"
            variant={p === page ? 'default' : 'outline'}
            size="sm"
            className="min-w-8 tabular-nums"
            onClick={() => onPageChange(p)}
            aria-current={p === page ? 'page' : undefined}
          >
            {p}
          </Button>
        ))}
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={page >= totalPages}
          onClick={() => onPageChange(page + 1)}
        >
          {t('common.nextPage', { defaultValue: '下一页' })}
        </Button>
      </div>
    </div>
  )
}
