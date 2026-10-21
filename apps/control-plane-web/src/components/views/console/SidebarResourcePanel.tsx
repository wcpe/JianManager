/**
 * @file SidebarResourcePanel：侧栏「资源」面板（重构件）——一行工具条 + 服务器列表。
 *
 * 结构（自上而下）：
 * 1. **一行工具条**：左=搜索（服务端 `q`，防抖在接线层）；右=筛选（状态下拉，多选）+ 分组方式（下拉）；
 * 2. **服务器列表**：按所选分组方式分段渲染（组头 + 行）；行悬停出「置顶/取消置顶」，
 *    悬停预览卡（HoverCard）展示缩略信息（状态/节点:端口/类型/角色/标签/创建时间）。
 *
 * 取数边界（性能判据）：列表是**有界窗口**（接线层 pageSize 上限 + 服务端 `q` 搜索），
 * 不是千级全量列举；截断时给出明确提示（护栏要求）。收藏/最近沿用既有 store（FR-293/240）。
 *
 * 受控边界（ADR-102）：数据（instances/nodes/query）由接线层注入；本地交互状态（分组方式/筛选）
 * 自持；偏好写入经 `lib/console/*`（localStorage 外部 store，无请求、无路由）。
 *
 * 【FR-509/510】本地自定义分组（原 `custom` 模式，存 localStorage）已删除：换机即失且与后端
 * 分组树（`instance_group_nodes`，ADR-033）语义重复。分组归属一律以后端分组树为准。
 */
 import { useMemo, useState } from 'react'
 import { useTranslation } from 'react-i18next'
 import { Check, Pin, Search, SlidersHorizontal } from 'lucide-react'

 import { cn } from '@jianmanager/ui'
import { focusRing } from '@jianmanager/ui/lib/focus-ring'
 import { instanceStatusLevel } from '@jianmanager/ui'
 import { Badge } from '@jianmanager/ui/components/badge'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
 import { HoverCard, HoverCardContent, HoverCardTrigger } from '@jianmanager/ui/components/hover-card'
 import { Input } from '@jianmanager/ui/components/input'
 import { StatusBadge } from '@jianmanager/ui/components/status-badge'
 import InstanceStatusDot from '@/components/views/instances/InstanceStatusDot'
 import { RoleBadge } from '@/components/views/instances/InstanceTableParts'
 import { statusDotKind } from '@/lib/instances/instance-tree'
import { envOf, freeTagsOf } from '@/lib/instances/instance-grouping'
import type { InstanceInfo } from '@/lib/instances/instance-types'
import type { InstanceMetricSnapshot } from '@/components/views/instances/InstanceRowMetrics'
import { useInstanceWindowMetrics } from '@/components/views/instances/useInstanceWindowMetrics'
import type { NodeInfo } from '@/lib/nodes/node-types'
import type { StorableInstance } from '@/lib/console/server-selection'
import { toggleFavoriteServer, useFavoriteServers } from '@/lib/console/server-selection'
 import {
  DEFAULT_GROUP_MODE,
  DEFAULT_SORT_MODE,
  GROUP_HEAD_HEIGHT,
  GROUP_MODES,
  SERVER_ROW_HEIGHT,
  SORT_MODES,
  buildGroupTreeLines,
  buildSections,
  flattenSidebarLines,
  rowId,
  rowName,
  rowNodeId,
  rowStatus,
  toStorable,
  type GroupMode,
  type PanelRow,
  type SortMode,
} from '@/lib/console/sidebar-sections'
import { useVirtualRows } from '@/lib/shared/virtual-list'
import { INSTANCE_KIND_LABEL_KEY, instanceKindOf } from '@/lib/instances/instance-kind'
import {
  formatCpuPct,
  formatMemGb,
  formatPlayers,
  formatPlayersRatio,
  formatTps,
} from '@/lib/console/metric-display'
import type { InstanceGroupNode } from '@/lib/instances/instance-group'

/** 状态筛选键（多选；空集=不筛）。 */
type StatusFilterKey = 'running' | 'abnormal' | 'stopped'

const STATUS_FILTERS: StatusFilterKey[] = ['running', 'abnormal', 'stopped']

const STATUS_FILTER_LABEL_KEY: Record<StatusFilterKey, string> = {
  running: 'sidebarResource.filterRunning',
  abnormal: 'sidebarResource.filterAbnormal',
  stopped: 'sidebarResource.filterStopped',
}

/** 实例状态 → 筛选键（与资源树的点色分类同源：`statusDotKind`）。 */
function statusFilterKeyOf(status: string): StatusFilterKey {
  const kind = statusDotKind(status)
  if (kind === 'running') return 'running'
  if (kind === 'crashed' || kind === 'transitioning') return 'abnormal'
  return 'stopped'
}

/** 分组维度下拉的文案。 */
const GROUP_MODE_LABEL_KEY: Record<GroupMode, string> = {
  node: 'sidebarResource.modeNode',
  network: 'sidebarResource.modeNetwork',
  groupTree: 'sidebarResource.modeGroupTree',
  tag: 'sidebarResource.modeTag',
}

/** 排序维度下拉的文案。 */
const SORT_MODE_LABEL_KEY: Record<SortMode, string> = {
  name: 'sidebarResource.sortName',
  created: 'sidebarResource.sortCreated',
  status: 'sidebarResource.sortStatus',
}

/** 分组偏好键（jm.* 前缀：新键统一口径）。 */
const GROUP_MODE_KEY = 'jm.sidebar.groupMode'
/** 排序偏好键：与分组正交，单独持久化。 */
const SORT_MODE_KEY = 'jm.sidebar.sortMode'

/**
 * 拖拽负载的 MIME 类型。
 *
 * 自定义类型（而非 `text/plain`）：置顶区据此判断「这次拖的是服务器行」，
 * 不会把外部拖进来的文本/文件误当成置顶操作。值只放实例 id。
 */
const SERVER_DRAG_MIME = 'application/x-jm-instance-id'

function readStoredMode(): GroupMode {
  try {
    const value = localStorage.getItem(GROUP_MODE_KEY)
    return GROUP_MODES.includes(value as GroupMode) ? (value as GroupMode) : DEFAULT_GROUP_MODE
  } catch {
    return DEFAULT_GROUP_MODE
  }
}

/** 读排序偏好（历史值 `name`/`created` 仍在 `SORT_MODES` 内，无需迁移）。 */
function readStoredSort(): SortMode {
  try {
    const value = localStorage.getItem(SORT_MODE_KEY)
    return SORT_MODES.includes(value as SortMode) ? (value as SortMode) : DEFAULT_SORT_MODE
  } catch {
    return DEFAULT_SORT_MODE
  }
}

/** 状态文案键（与实例列表行同源）。 */
const STATUS_LABEL_KEY: Record<string, string> = {
  STOPPED: 'instances.stopped',
  STARTING: 'instances.starting',
  RUNNING: 'instances.running',
  STOPPING: 'instances.stopping',
  CRASHED: 'instances.crashed',
}


export interface SidebarResourcePanelProps {
  /** 已加载的实例窗口（服务端搜索落定后）。 */
  instances: InstanceInfo[]
  /** 节点列表（分组头/悬停卡取节点名）。 */
  nodes: NodeInfo[]
  /** 服务端搜索落定值（受控；防抖在接线层）。 */
  query: string
  onQueryChange: (value: string) => void
  /** 已加载条数（截断提示用）。 */
  loadedCount: number
  /** 权威总数（截断提示用）。 */
  totalCount?: number
  /** 实例 id → 群组名（「按群组」分组方式用；一台实例可命中多个群组，取首个）。 */
  networkKeyOf: Map<number, string[]>
  /** 后端组织分组树（「按分组」分组方式用；`GET /instance-groups` 的扁平节点）。 */
  groupTree: InstanceGroupNode[]
  /** 打开服务器（接线层负责导航与「最近打开」记账）。 */
  onOpen: (inst: StorableInstance) => void
}

/**
 * 侧栏资源面板：**搜索 + 筛选 + 一个分组方式下拉 + 服务器列表**，另加常驻置顶段。
 *
 * 【为什么只剩这些】此前尝试过「固定三段（置顶 / 分组树 / 标签）」，结论是标签是次要维度，
 * 平铺或折叠都会挤占侧栏；分组树常驻则把侧栏变成第二棵树。侧栏的职责是**快速定位一台服务器**，
 * 故收敛为：置顶（少量）、分组方式（下拉选一个维度）、列表（按该维度分段）。
 */
export function SidebarResourcePanel({
  instances,
  nodes,
  query,
  onQueryChange,
  loadedCount,
  totalCount,
  networkKeyOf,
  groupTree,
  onOpen,
}: SidebarResourcePanelProps) {
  const { t } = useTranslation()
  const [statusFilter, setStatusFilter] = useState<Set<StatusFilterKey>>(() => new Set())
  const [mode, setMode] = useState<GroupMode>(readStoredMode)
  const [sort, setSort] = useState<SortMode>(readStoredSort)
  /** 拖拽中的服务器（原生 HTML5 拖拽：`dataTransfer` 只需带 id）。 */
  const [draggingId, setDraggingId] = useState<number | null>(null)
  /** 置顶区是否处于拖放高亮（`dragover` 期间为 true）。 */
  const [pinDropActive, setPinDropActive] = useState(false)

  const favorites = useFavoriteServers()

  const toggleStatusFilter = (key: StatusFilterKey) => {
    setStatusFilter((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  const changeMode = (next: GroupMode) => {
    setMode(next)
    try {
      localStorage.setItem(GROUP_MODE_KEY, next)
    } catch {
      // localStorage 不可用（隐私模式等）：仅本会话生效，不阻断交互。
    }
  }

  const changeSort = (next: SortMode) => {
    setSort(next)
    try {
      localStorage.setItem(SORT_MODE_KEY, next)
    } catch {
      // 同上：不可持久化不影响本次交互。
    }
  }

  const loadedById = useMemo(() => new Map(instances.map((inst) => [inst.id, inst])), [instances])
  const nodeLabelOf = useMemo(() => new Map(nodes.map((node) => [node.id, node.name])), [nodes])
  const nodeOrder = useMemo(() => new Map(nodes.map((node, index) => [node.id, index])), [nodes])

  const matchesFilter = (status: string) => statusFilter.size === 0 || statusFilter.has(statusFilterKeyOf(status))

  const instanceRows = useMemo<PanelRow[]>(
    () => instances.filter((inst) => matchesFilter(inst.status)).map((inst) => ({ kind: 'instance', inst })),
    // matchesFilter 只依赖 statusFilter：以它为依赖即可。
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [instances, statusFilter],
  )

  /**
   * 置顶区：收藏顺序排在最上方，**独立于分组**（用户要求：不再是「置顶分组方式」）。
   * 未加载进窗口的收藏回落快照行（名称/状态仍可展示，拖拽仍可用）。
   */
  const pinnedRows = useMemo<PanelRow[]>(
    () => favorites
      .map((snap): PanelRow => {
        const inst = loadedById.get(snap.id)
        return inst ? { kind: 'instance', inst } : { kind: 'snapshot', snap }
      })
      // 置顶区同样吃状态筛选：筛了「异常」时不该在置顶区看到运行中的收藏。
      .filter((row) => matchesFilter(rowStatus(row))),
    // matchesFilter 只依赖 statusFilter。
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [favorites, loadedById, statusFilter],
  )

  /** 置顶区里已出现的实例 id：从下面的分组列表中剔除，避免同一台出现两次。 */
  const pinnedIdSet = useMemo(() => new Set(pinnedRows.map(rowId)), [pinnedRows])

  /** 分组列表的数据源：窗口内实例，去掉已在置顶区的那些。 */
  const rows = useMemo<PanelRow[]>(
    () => instanceRows.filter((row) => !pinnedIdSet.has(rowId(row))),
    [instanceRows, pinnedIdSet],
  )

  const sections = useMemo(
    () =>
      buildSections({
        rows,
        mode,
        nodeLabelOf,
        nodeOrder,
        networkKeyOf,
        sort,
        labels: { ungrouped: t('sidebarResource.ungrouped') },
      }),
    [rows, mode, nodeLabelOf, nodeOrder, networkKeyOf, sort, t],
  )

  /** 「按分组」维度走文件树（带层级缩进与展开），其余维度走分段列表。 */
  const treeLines = useMemo(
    () => (mode === 'groupTree' ? buildGroupTreeLines({ rows, groupTree }) : []),
    [mode, rows, groupTree],
  )

  /**
   * 扁平行模型（虚拟化的输入）：组头/组内实例/分段行统一成一条序列。
   * 两种渲染形态共用同一份窗口计算——否则分组方式切换会各走一条渲染路径，容易出现两套漂移。
   */
  const lines = useMemo(
    () => flattenSidebarLines({ mode, sections, treeLines }),
    [mode, sections, treeLines],
  )

  /**
   * 每行高度（变高模式）：组头 26、服务器行 44。
   *
   * 【为什么不在行上留 margin】`space-y-*` / `mt-*` 不计入 `sizes`，千行时会累积到数千像素，
   * 滚到底部能看到空白；间距一律折进行盒高度（护栏要求）。行内 padding 不改变外层高度。
   */
  const rowSizes = useMemo(
    () => lines.map((line) => (line.kind === 'server' ? SERVER_ROW_HEIGHT : GROUP_HEAD_HEIGHT)),
    [lines],
  )

  const { containerRef, onScroll, range, totalSize } = useVirtualRows({
    total: lines.length,
    itemSize: SERVER_ROW_HEIGHT,
    // 侧栏可视区通常 400~900px，回退值取 900 是为了首帧（未测量前）不留空白。
    fallbackViewportSize: 900,
    fallbackCrossSize: 240,
    // 侧栏滚动很快（滚轮一拨就是半屏），overscan 取 8 行≈350px，够覆盖一次快速滚动。
    overscan: 8,
    sizes: rowSizes,
  })

  const visibleLines = lines.slice(range.start, range.end)
  const totalRows = lines.reduce((sum, line) => (line.kind === 'server' ? sum + 1 : sum), 0)
  const truncated = totalCount !== undefined && loadedCount < totalCount

  /**
   * 指标取数边界改到**真实可见行**（本轮性能修复的核心之一）。
   *
   * 【修的是什么】此前接线层把整个窗口（最多 100 台）交给 `useInstanceWindowMetrics`，
   * 而该 hook 单次只取前 50 个目标（batch 端点硬上限）——于是第 51 台以后的服务器**永远没有
   * 指标**，滚动到那里只看到空白；同时那 50 个目标里还有大量行根本不在视口内，纯属浪费请求。
   *
   * 【现在】面板就是这张虚拟表的拥有者（ADR-102 b：窗口归属虚拟表），按 `range` 切片后
   * 只把**看得见的那 20 来台**交给同一个 hook。目标数从「固定 50」降到「可视行数」，
   * 且滚到哪取到哪，滚动时由 hook 内已有的 300ms 防抖 + `keepPreviousData` 兜住。
   */
  const visibleInstances = useMemo(
    () => visibleLines.flatMap((line) => (line.kind === 'server' && line.row.kind === 'instance' ? [line.row.inst] : [])),
    [visibleLines],
  )
  // 轻量模式：侧栏只要末点（当前/最大人数、TPS、CPU、内存），不画心跳带。
  // 1h raw × 5 指标 × 一屏几十台实测 575KB/次；换 24h+5m 预聚合档后小两个数量级。
  const metrics = useInstanceWindowMetrics(visibleInstances, { light: true })

  return (
    <div data-testid="sidebar-resource-panel" className="flex min-h-0 flex-1 flex-col">
      {/* 一行工具条：搜索 + 状态筛选 + 分组方式（唯一的维度选择）。 */}
      <div data-slot="sidebar-resource-toolbar" className="mx-[9px] mb-[7px] mt-[10px] flex items-center gap-[4px]">
        <div className="relative min-w-0 flex-1">
          <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground/60" />
          <Input
            value={query}
            onChange={(event) => onQueryChange(event.target.value)}
            placeholder={t('sidebarResource.searchPlaceholder')}
            aria-label={t('sidebarResource.searchLabel')}
            className="h-7 pl-7 text-xs"
          />
        </div>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              type="button"
              aria-label={t('sidebarResource.filterAndGroup')}
              title={t('sidebarResource.filterAndGroup')}
              className={cn(
                'flex h-7 shrink-0 items-center gap-1 rounded-md border border-border px-1.5 text-[11px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground',
                /*
                  【必须显式关掉原生 outline】这是原生 `<button>`（不是库里的 Button），菜单关闭后
                  焦点回到触发按钮，浏览器便画出默认的 `outline: rgb(16,16,16) auto 1px`——
                  在浅色侧栏上就是用户看到的那圈「黑边」。键盘可达性改由库统一的细焦点环承担
                  （focusRing：2px 主色 + 40% 底），既符合 FR-176 的收敛宽度，也不再出现黑框。
                */
                'focus-visible:outline-none',
                focusRing,
                (statusFilter.size > 0 || mode !== DEFAULT_GROUP_MODE || sort !== DEFAULT_SORT_MODE) && 'border-primary/50 text-primary',
              )}
            >
              <SlidersHorizontal className="size-3.5 shrink-0" />
              {/* 当前分组方式与筛选状态都在这一个按钮上：侧栏横向空间紧张，不再单独占一个下拉。 */}
              <span className="max-w-[64px] truncate">{t(GROUP_MODE_LABEL_KEY[mode])}</span>
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-44">
            {/*
              三组：分组 / 排序 / 筛选。分组与排序**正交**——此前它们是同一个维度
              （选了「按名称」就等于放弃分组），拆开后「按节点分组 + 按名称排序」才表达得出来。
              「置顶」不在这里：它已提升为列表上方的独立区（可拖拽放入）。
            */}
            <DropdownMenuLabel className="text-[10px] font-normal text-muted-foreground/70">{t('sidebarResource.groupMode')}</DropdownMenuLabel>
            {GROUP_MODES.map((item) => (
              <DropdownMenuItem key={item} className="text-xs" onSelect={() => changeMode(item)}>
                {t(GROUP_MODE_LABEL_KEY[item])}
                {mode === item && <Check className="ml-auto size-3.5" />}
              </DropdownMenuItem>
            ))}
            <DropdownMenuSeparator />
            <DropdownMenuLabel className="text-[10px] font-normal text-muted-foreground/70">{t('sidebarResource.sortMode')}</DropdownMenuLabel>
            {SORT_MODES.map((item) => (
              <DropdownMenuItem key={item} className="text-xs" onSelect={() => changeSort(item)}>
                {t(SORT_MODE_LABEL_KEY[item])}
                {sort === item && <Check className="ml-auto size-3.5" />}
              </DropdownMenuItem>
            ))}
            <DropdownMenuSeparator />
            <DropdownMenuLabel className="text-[10px] font-normal text-muted-foreground/70">{t('sidebarResource.filter')}</DropdownMenuLabel>
            {STATUS_FILTERS.map((key) => (
              <DropdownMenuItem
                key={key}
                onSelect={() => {
                  /*
                    【选完即关】此前刻意 `preventDefault()` 保持菜单打开，理由是「筛选是就地多选、
                    便于连续勾选」。实测判断：侧栏这个菜单同时装着「分组方式」与「筛选」两组，
                    保持打开反而让用户不确定这次点击生效了没有——用户反馈的「选择框不消失」即指此。
                    多选成本并没有变大：重开菜单仍能看到已勾选状态。
                  */
                  toggleStatusFilter(key)
                }}
              >
                <Check className={cn('size-3.5', !statusFilter.has(key) && 'opacity-0')} />
                {t(STATUS_FILTER_LABEL_KEY[key])}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {/*
        置顶区（独立于分组，用户要求）：拖任意服务器行到这里即置顶。
        它**不参与**下面的虚拟化——收藏数量是人手工攒的（`FAVORITES_LIMIT=32` 上限），
        列表短、且是拖放落点（护栏要求：拖拽依赖元素持续存在，不能进滚动回收的集合）。
      */}
      <PinnedZone
        rows={pinnedRows}
        active={pinDropActive}
        draggingId={draggingId}
        nodeLabelOf={nodeLabelOf}
        metrics={metrics}
        onOpen={onOpen}
        onUnpin={(id) => {
          const hit = favorites.find((item) => item.id === id)
          if (hit) toggleFavoriteServer(hit)
        }}
        onDropRow={(id) => {
          // 从「最近/窗口」拖来的可能是未收藏项：按 id 找回实例或快照后收藏。
          const inst = loadedById.get(id)
          if (inst) toggleFavoriteServer(toStorable({ kind: 'instance', inst }))
        }}
        onDragStateChange={(id) => setDraggingId(id)}
        onDropActiveChange={setPinDropActive}
      />

      {/*
        虚拟化列表：千级实例时只挂载可视窗口内的行（约 20 行），DOM 量从 N 降到常数。
        `data-slot` 保留在滚动容器上，原有选择器（含测试）不受影响。
        段容器 `sidebar-resource-section` 在虚拟化下不再按段包裹（段会被窗口切断），
        其语义改由行上的 `data-section` 承担。
      */}
      <div
        ref={containerRef}
        onScroll={onScroll}
        data-slot="sidebar-resource-list"
        className="min-h-0 flex-1 overflow-y-auto scrollbar-none px-[9px] pb-[12px]"
      >
        {totalRows === 0 ? (
          <p className="px-[8px] py-[10px] text-[11px] text-muted-foreground">{t('sidebarResource.empty')}</p>
        ) : (
          // 上占位 + 窗口 + 下占位：占位撑出完整滚动高度，窗口内才是真实 DOM。
          <div style={{ height: totalSize }}>
            <div style={{ height: range.before }} />
            {visibleLines.map((line) => {
              if (line.kind === 'section-head') {
                return <SectionHeader key={line.key} label={line.label} count={line.count} />
              }
              if (line.kind === 'group-branch') {
                return <GroupTreeBranch key={line.key} label={line.label} depth={line.depth} count={line.count} />
              }
              return (
                <ServerRow
                  key={line.key}
                  row={line.row}
                  nodeLabel={nodeLabelOf.get(rowNodeId(line.row)) ?? `#${rowNodeId(line.row)}`}
                  pinned={favorites.some((item) => item.id === rowId(line.row))}
                  metrics={metrics}
                  indent={line.indent}
                  onOpen={() => onOpen(toStorable(line.row))}
                  onTogglePin={() => toggleFavoriteServer(toStorable(line.row))}
                  draggable
                  onDragStateChange={setDraggingId}
                />
              )
            })}
            <div style={{ height: range.after }} />
          </div>
        )}

        {truncated && (
          <p className="px-[8px] pt-[8px] text-[10px] leading-relaxed text-muted-foreground/70">
            {t('sidebarResource.truncated', { loaded: loadedCount, total: totalCount })}
          </p>
        )}
      </div>
    </div>
  )
}

/**
 * 行尾：在线人数「当前/最大」。
 *
 * 三种情形分别处理，都不写占位符（这一列极窄，`--` 比空白更干扰扫读）：
 * - 都有值 → `16/50`；
 * - 只有当前人数（上限缺测，如代理未提供 maxplayers）→ `16人`；
 * - 都没有 → 空。
 *
 * 取整与「上限缺测」的判定都在 `lib/console/metric-display`（与悬停卡共用同一份实现，
 * 避免两处各写一遍、只修对一处——那正是悬浮卡显示 `18.208000000000002` 的成因）。
 */
function rowPlayerText(snapshot: InstanceMetricSnapshot | undefined): string {
  if (!snapshot) return ''
  return formatPlayersRatio(snapshot.players, snapshot.playersMax)
}

/**
 * 文件树的一层组头：缩进 + 竖线引导 + 名称 + 子树计数。
 *
 * 只做视觉层级，不做折叠——侧栏空间有限，折叠状态又要额外记忆，反而增加认知负担；
 * 层级靠缩进与引导线已足够读清「集群 → 大区 → 服组」。
 */
function GroupTreeBranch({ label, depth, count }: { label: string; depth: number; count: number }) {
  return (
    <div
      data-slot="sidebar-resource-group-branch"
      className="flex h-[26px] items-center gap-1 rounded-[5px] text-[11px] font-medium text-foreground/80"
      style={{ paddingLeft: `${6 + depth * 14}px` }}
    >
      <span className="min-w-0 flex-1 truncate">{label}</span>
      <span className="shrink-0 pr-[6px] font-mono text-[10px] tabular-nums text-muted-foreground/60">{count}</span>
    </div>
  )
}

/** 分组头：名称 + 计数（纯展示）。 */
function SectionHeader({ label, count }: { label: string; count: number }) {
  return (
    <div
      data-slot="sidebar-resource-group-head"
      className="flex h-[26px] items-center gap-1 rounded-[5px] px-[6px] text-[10px] tracking-[.5px] text-muted-foreground/70"
    >
      <span className="min-w-0 flex-1 truncate">{label}</span>
      <span className="shrink-0 font-mono text-[10px] text-muted-foreground/60">{count}</span>
    </div>
  )
}

/**
 * 置顶区（用户要求：不再是一种「分组方式」，而是列表上方独立的一块）。
 *
 * 交互：
 * - **拖任意服务器行到这里即置顶**（`dragover` 高亮、`drop` 收藏）；
 * - 区内每行有「取消置顶」按钮；点击行照常进入实例；
 * - 一行都没有时**不渲染任何容器**——空的置顶区会白占一条高度并画出分隔线。
 *
 * 【为什么不虚拟化】收藏数量由人手攒（上限 `FAVORITES_LIMIT=32`），列表短；
 * 更关键的是它本身就是**拖放落点**，而护栏要求「依赖元素持续存在的交互不得进滚动回收集合」。
 */
function PinnedZone({
  rows,
  active,
  draggingId,
  nodeLabelOf,
  metrics,
  onOpen,
  onUnpin,
  onDropRow,
  onDragStateChange,
  onDropActiveChange,
}: {
  rows: PanelRow[]
  /** 当前是否有一个可放置的拖拽经过（控制高亮）。 */
  active: boolean
  draggingId: number | null
  nodeLabelOf: Map<number, string>
  metrics: Map<string, InstanceMetricSnapshot>
  onOpen: (inst: StorableInstance) => void
  onUnpin: (id: number) => void
  onDropRow: (id: number) => void
  onDragStateChange: (id: number | null) => void
  onDropActiveChange: (active: boolean) => void
}) {
  const { t } = useTranslation()
  /*
    【为什么拖拽在途时也要渲染】没有收藏时本区整块不存在，于是没有任何元素能接收 `dragover`
    ——用户第一次想把服务器拖上来时**根本拖不进去**（实测：行已可拖，落点却不存在）。
    拖动进行中（`draggingId !== null`）先占位成虚线落点，拖完若仍未收藏就自动消失。
  */
  if (rows.length === 0 && draggingId === null) return null
  const placeholder = rows.length === 0

  return (
    <div
      data-slot="sidebar-pinned-zone"
      data-testid="sidebar-pinned-zone"
      data-active={active || undefined}
      onDragOver={(event) => {
        // 只有本应用的行可以放（外部文件/文本一律忽略）。
        if (!event.dataTransfer.types.includes(SERVER_DRAG_MIME)) return
        // 必须 preventDefault 才会触发 drop，且要标记为 copy 而非 move。
        event.preventDefault()
        event.dataTransfer.dropEffect = 'copy'
        onDropActiveChange(true)
      }}
      onDragLeave={(event) => {
        // 子元素间移动也会触发 dragleave：只有真正离开整个区才取消高亮。
        if (event.currentTarget.contains(event.relatedTarget as Node | null)) return
        onDropActiveChange(false)
      }}
      onDrop={(event) => {
        event.preventDefault()
        onDropActiveChange(false)
        const raw = event.dataTransfer.getData(SERVER_DRAG_MIME)
        const id = Number(raw)
        if (Number.isFinite(id) && id > 0) {
          onDropRow(id)
          // 拖进置顶区即已收藏：行本身还在下方列表里（置顶区是镜像），所以不清空拖拽态。
          onDragStateChange(null)
        }
      }}
      className={cn(
        'mx-[9px] mb-[4px] shrink-0 rounded-md transition-colors',
        active ? 'bg-primary/10 ring-1 ring-primary/40 ring-inset' : 'bg-muted/30',
        placeholder && 'border border-dashed border-primary/40 bg-primary/5',
      )}
    >
      <div className="flex h-[22px] items-center gap-1 px-[6px] text-[10px] tracking-[.5px] text-muted-foreground/70">
        <Pin className="size-2.5 shrink-0" />
        <span className="min-w-0 flex-1 truncate">{t('sidebarResource.pinnedZone')}</span>
        {placeholder
          ? <span className="shrink-0 text-muted-foreground/60">{t('sidebarResource.pinnedDropHint')}</span>
          : <span className="shrink-0 font-mono tabular-nums text-muted-foreground/60">{rows.length}</span>}
      </div>
      {rows.map((row) => (
        <div
          key={`pin:${rowId(row)}`}
          data-testid="sidebar-pinned-row"
          className={cn(
            'group/pin flex h-[28px] items-center gap-1 rounded-[5px] pr-[3px] transition-colors hover:bg-muted',
            draggingId === rowId(row) && 'opacity-50',
          )}
        >
          <button
            type="button"
            onClick={() => onOpen(toStorable(row))}
            title={`${rowName(row)} · ${nodeLabelOf.get(rowNodeId(row)) ?? `#${rowNodeId(row)}`}`}
            className="flex min-w-0 flex-1 items-center gap-[6px] rounded-[5px] px-[6px] text-left focus-visible:outline-none"
          >
            <InstanceStatusDot status={rowStatus(row)} />
            <span className="min-w-0 flex-1 truncate text-[12px] font-[570] text-foreground/90">{rowName(row)}</span>
            <span className="shrink-0 text-right font-mono text-[10px] tabular-nums leading-[15px] text-muted-foreground/60">
              {row.kind === 'instance' ? rowPlayerText(metrics.get(row.inst.uuid)) : ''}
            </span>
          </button>
          <button
            type="button"
            onClick={() => onUnpin(rowId(row))}
            aria-label={`${t('sidebarResource.unpin')}：${rowName(row)}`}
            title={t('sidebarResource.unpin')}
            className="flex size-[20px] shrink-0 items-center justify-center rounded-[4px] text-primary opacity-0 transition-opacity hover:bg-muted-foreground/10 group-hover/pin:opacity-100 focus-visible:opacity-100"
          >
            <Pin className="size-3 fill-current" />
          </button>
        </div>
      ))}
    </div>
  )
}

/** 服务器行：状态点 + 名称；悬停出置顶按钮与缩略信息卡。 */
function ServerRow({
  row,
  nodeLabel,
  pinned,
  metrics,
  indent = 0,
  onOpen,
  onTogglePin,
  draggable = false,
  onDragStateChange,
}: {
  row: PanelRow
  nodeLabel: string
  pinned: boolean
  /** 行尾小字用的指标快照（uuid → 快照）。 */
  metrics: Map<string, InstanceMetricSnapshot>
  /** 文件树层级缩进（单位：层）。非树维度为 0。 */
  indent?: number
  onOpen: () => void
  onTogglePin: () => void
  /** 允许把这一行拖到置顶区（原生 HTML5 拖拽，`dataTransfer` 只带实例 id）。 */
  draggable?: boolean
  /** 拖拽开始/结束上报（供置顶区高亮，也供外部判断当前拖的是哪台）。 */
  onDragStateChange?: (id: number | null) => void
}) {
  const { t } = useTranslation()
  const name = rowName(row)
  const status = rowStatus(row)
  const inst = row.kind === 'instance' ? row.inst : undefined
  return (
    <div
      data-slot="sidebar-resource-row"
      data-testid="sidebar-resource-row"
      /*
        `draggable` 落在外层行盒而不是按钮上：按钮内部已 `onClick` 打开实例，
        再让按钮自身可拖会在「按下即拖」时吞掉点击。行盒可拖、按钮照常可点。
      */
      draggable={draggable || undefined}
      onDragStart={
        draggable
          ? (event) => {
              // 只带 id：跨区域拖拽不需要序列化整台实例，且避免把敏感字段写进 dataTransfer。
              event.dataTransfer.setData(SERVER_DRAG_MIME, String(rowId(row)))
              event.dataTransfer.effectAllowed = 'copy'
              onDragStateChange?.(rowId(row))
            }
          : undefined
      }
      onDragEnd={draggable ? () => onDragStateChange?.(null) : undefined}
      className="group/row flex h-[32px] items-center gap-1 rounded-[5px] pr-[3px] transition-colors hover:bg-muted"
    >
      <HoverCard openDelay={300} closeDelay={80}>
        <HoverCardTrigger asChild>
          <button
            type="button"
            onClick={onOpen}
            title={`${name} · ${nodeLabel}`}
            className="flex min-w-0 flex-1 items-center gap-[6px] rounded-[5px] px-[6px] py-[2px] text-left focus-visible:outline-none"
            style={indent > 0 ? { paddingLeft: `${6 + indent * 14}px` } : undefined}
          >
            <InstanceStatusDot status={status} />
            {/*
              两行：第一行名称，第二行备注。
              备注取实例的 `statusReason`（实例模型没有独立 remark 字段，这条自由文本就是它的
              唯一备注语义：崩溃原因、未绑定 JDK 等）。**不回落到节点名**——组头已经写了节点，
              行内再写一遍是冗余，测试也会因同一文案命中两处而误判。
              没有备注就留空：行高固定，空白不改变布局。
            */}
            <span className="min-w-0 flex-1">
              <span className="block truncate text-[12px] font-[570] leading-[14px] text-foreground/90">{name}</span>
              {/*
                第二行 = 类别徽标 +（有备注时）` · 备注`（FR-511）。
                为什么放这里：`statusReason` 只有崩溃/异常时才有值——**正常运行的服务器第二行本来是空的**，
                那块空间一直白占。把类别常驻在此，信息量最大且**完全不挤名称宽度**
                （方案对比实测：名称仍 171px，与不标记时持平）。
              */}
              <span className="flex h-[12px] items-center gap-1 overflow-hidden">
                {inst && (
                  <span className="shrink-0 rounded-[3px] border border-border/70 bg-muted/60 px-[3px] text-[9px] leading-[11px] text-muted-foreground">
                    {t(INSTANCE_KIND_LABEL_KEY[instanceKindOf(inst)])}
                  </span>
                )}
                {inst?.statusReason?.trim() && (
                  <>
                    <span className="shrink-0 text-[10px] leading-[12px] text-muted-foreground/40">·</span>
                    <span className="truncate text-[10px] leading-[12px] text-muted-foreground/60">
                      {inst.statusReason.trim()}
                    </span>
                  </>
                )}
              </span>
            </span>
            {/*
              行尾：在线人数「当前/最大」（FR-511，TPS 已删）。
              上限来自新增的 `inst_players_max` 时序（心跳携带 maxPlayers 由 CP 落库），
              与当前人数同批取回，不需要逐台拉详情（那会退化成 N+1）。
              上限缺测时只显示当前人数——不能用 0 兜底，那会被读成「上限 0」。
            */}
            <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground/60">
              {inst ? rowPlayerText(metrics.get(inst.uuid)) : ''}
            </span>
          </button>
        </HoverCardTrigger>
        <HoverCardContent>
          <ServerPeekCard row={row} nodeLabel={nodeLabel} metrics={metrics} />
        </HoverCardContent>
      </HoverCard>
      <button
        type="button"
        onClick={onTogglePin}
        aria-label={`${t(pinned ? 'sidebarResource.unpin' : 'sidebarResource.pin')}：${name}`}
        title={t(pinned ? 'sidebarResource.unpin' : 'sidebarResource.pin')}
        data-testid="sidebar-resource-pin"
        className={cn(
          'flex size-[20px] shrink-0 items-center justify-center rounded-[4px] text-muted-foreground/50 opacity-0 transition-opacity hover:bg-muted-foreground/10 hover:text-foreground group-hover/row:opacity-100 focus-visible:opacity-100',
          pinned && 'opacity-100 text-primary',
        )}
      >
        <Pin className={cn('size-3.5', pinned && 'fill-current')} />
      </button>
    </div>
  )
}

/**
 * 悬停预览卡：**侧栏行上不再显示的细节全在这里**——实时指标（CPU / 内存 / 玩家 / TPS / 心跳）
 * + 归属信息（节点:端口 / 类型·角色 / 标签 / 创建时间）+ 进入提示。
 *
 * 【为什么把指标挪到这里】用户要求行尾只保留名称 + 备注 + 在线玩家：侧栏一行约 230px，
 * 四段数字并排会把名称挤成省略号。移到悬停卡后信息不丢、扫描密度也回来，且悬停是**主动**行为，
 * 不会干扰扫读。
 *
 * 缺测一律显示 `--`：这里是宽卡片而非超窄行尾，占位符反而提供列对齐价值。
 */
function ServerPeekCard({
  row,
  nodeLabel,
  metrics,
}: {
  row: PanelRow
  nodeLabel: string
  metrics: Map<string, InstanceMetricSnapshot>
}) {
  const { t } = useTranslation()
  const name = rowName(row)
  const status = rowStatus(row)
  const statusLabel = t(STATUS_LABEL_KEY[status] ?? 'instances.stopped')
  const inst = row.kind === 'instance' ? row.inst : undefined
  const snapshot = inst ? metrics.get(inst.uuid) : undefined
  const tags = inst ? [envOf(inst), ...freeTagsOf(inst)].filter(Boolean).slice(0, 4) : []

  const beats = snapshot?.heartbeat ?? []
  const beatActive = beats.filter((b) => b === true).length
  const beatDown = beats.filter((b) => b === false).length
  const beatLabel = t('instances.metrics.heartbeatLabel', {
    active: beatActive,
    down: beatDown,
    unknown: beats.length - beatActive - beatDown,
  })

  const dash = <span className="text-muted-foreground/60">--</span>

  return (
    <div data-testid="sidebar-resource-peek" className="space-y-1.5">
      <div className="flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-sm font-medium">{name}</span>
        <StatusBadge level={instanceStatusLevel(status)} label={statusLabel} />
      </div>

      {/* 运行指标：2×2 网格，`tabular-nums` 防数值跳动。 */}
      {snapshot ? (
        <div data-slot="sidebar-peek-metrics" className="grid grid-cols-2 gap-x-3 gap-y-1 text-[11px]">
          <span className="flex items-baseline justify-between gap-2">
            <span className="text-muted-foreground/70">{t('sidebarResource.peekCpu')}</span>
            <span className="font-mono tabular-nums">
              {snapshot.cpuPct === null ? dash : formatCpuPct(snapshot.cpuPct)}
            </span>
          </span>
          <span className="flex items-baseline justify-between gap-2">
            <span className="text-muted-foreground/70">{t('sidebarResource.peekMem')}</span>
            <span className="font-mono tabular-nums">
              {snapshot.memUsedBytes === null ? dash : formatMemGb(snapshot.memUsedBytes)}
            </span>
          </span>
          <span className="flex items-baseline justify-between gap-2">
            <span className="text-muted-foreground/70">{t('sidebarResource.peekPlayers')}</span>
            {/*
              【必须取整】`players` 是序列末点的 avg（时序库里的浮点均值），直接输出会打印出
              `18.208000000000002` 这种双精度尾巴——「在线人数」在语义上只能是整数。
              与行尾 `rowPlayerText` 的口径保持一致（同一份数据，两处显示不该不同）。
            */}
            <span className="font-mono tabular-nums">
              {snapshot.players === null ? dash : formatPlayers(snapshot.players)}
            </span>
          </span>
          <span className="flex items-baseline justify-between gap-2">
            <span className="text-muted-foreground/70">{t('sidebarResource.peekTps')}</span>
            {/*
              TPS 保留一位小数，但 `toFixed(1)` 在部分取值上仍会露出浮点尾巴
              （例如 19.850000000000001 → "19.9" 是对的，但 18.208 → "18.2" 依赖舍入）。
              这里先按 1 位量化再输出字符串，避免出现 `18.200000000000003` 这类结果。
            */}
            <span className="font-mono tabular-nums">
              {snapshot.tps === null ? dash : formatTps(snapshot.tps)}
            </span>
          </span>
        </div>
      ) : (
        <div className="text-[11px] text-muted-foreground/70">{t('sidebarResource.peekNoMetrics')}</div>
      )}

      {/* 活动心跳：末段采样逐格（绿=活跃 / 红=停摆 / 灰=缺测），聚合文案供读屏与悬停。 */}
      {beats.length > 0 && (
        <div className="flex items-center gap-2 text-[11px]">
          <span className="shrink-0 text-muted-foreground/70">{t('sidebarResource.peekHeartbeat')}</span>
          <span
            role="img"
            aria-label={beatLabel}
            title={beatLabel}
            data-testid="sidebar-peek-heartbeat"
            className="flex h-3 flex-1 items-stretch gap-px"
          >
            {beats.map((b, i) => (
              <span
                key={i}
                className={cn(
                  'flex-1 rounded-[1px]',
                  b === true ? 'bg-status-success' : b === false ? 'bg-status-danger' : 'bg-muted-foreground/25',
                )}
              />
            ))}
          </span>
        </div>
      )}

      <div className="text-[11px] text-muted-foreground">
        {t('instances.nodePort')}：{nodeLabel}
        {inst && inst.serverPort > 0 && <span className="font-mono">:{inst.serverPort}</span>}
      </div>
      {inst && (
        <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          <span>{t('instances.type')}：{inst.type}</span>
          <RoleBadge role={inst.role} compact />
        </div>
      )}
      {tags.length > 0 && (
        <div className="flex flex-wrap items-center gap-1">
          {tags.map((tag) => (
            <Badge key={tag} variant="secondary" className="font-normal">
              {tag}
            </Badge>
          ))}
        </div>
      )}
      {inst && (
        <div className="text-[11px] text-muted-foreground">
          {t('sidebarResource.peekCreated')}：{new Date(inst.createdAt).toLocaleDateString()}
        </div>
      )}
      <div className="pt-0.5 text-[11px] text-primary">{t('sidebarResource.peekOpen')}</div>
    </div>
  )
}
