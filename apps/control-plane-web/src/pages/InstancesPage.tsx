// 列表行展示层已回迁应用侧（迁包已撤销，见 ADR-103）：平铺表与分组树表共用的行渲染件在包内；
// 本层保留取数（无限搜索 / 聚合 / 节点 / 群组 / 分组树）、URL 状态、批量选择与各弹窗目标的装配。
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate, useSearchParams } from 'react-router'
import { usePageContext } from '@/lib/assistant/page-context'
import { useTranslation } from 'react-i18next'
import { Zap, Globe, Plus, FolderTree, HardDriveDownload, Search, SlidersHorizontal, Settings2, Bookmark, Trash2, ServerCog, CircleCheck, CircleSlash, TriangleAlert, ChevronDown } from 'lucide-react'
import { useSearchInstances, useInstanceAggregate, useStartInstance, useStopInstance, useRestartInstance, useDeleteInstance, useKillInstance, useAdoptInstanceRuntime, isProvisioningInstance, type InstanceInfo } from '@/api/instances'
import type { InstanceListParams, InstanceSearchParams } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useNetworks } from '@/api/networks'
import { useTopology } from '@/api/topology'
import { useInstanceGroups } from '@/api/instanceGroups'
import { useRegistrations } from '@/api/registrations'
import { useConsoleStore } from '@/stores/console'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import InstanceBatchBar from '@/components/instances/InstanceBatchBar'
import ProvisionServerDialog from '@/components/provision/ProvisionServerDialog'
import ProvisionProxyDialog from '@/components/provision/ProvisionProxyDialog'
import ImportServerWizard from '@/components/provision/ImportServerWizard'
import ProxyRegistrationsDialog from '@/components/instances/ProxyRegistrationsDialog'
import CloneInstanceDialog from '@/components/instances/CloneInstanceDialog'
import InstanceTagsDialog from '@/components/instances/InstanceTagsDialog'
import EditInstanceLimitsDialog from '@/components/instances/EditInstanceLimitsDialog'
import EditInstanceConfigDialog from '@/components/instances/EditInstanceConfigDialog'
import { useDebounced } from '@/lib/hooks/use-debounced'
import { resolveCapabilities } from '@/lib/instances/capabilities'
import { InstanceWorktableCard } from '@/components/console/InstanceWorktableCard'
import { InstanceGroupManager } from '@/components/console/InstanceGroupManager'
import { buildGroupTreeSource, buildKeyMap, collectEnvs, collectTags, groupInstances, groupInstancesByGroupTree, parseTags, GROUP_DIMENSIONS } from '@/lib/instances/instance-grouping'
import type { GroupDimension } from '@/lib/instances/instance-grouping'
import { runtimeDriftOf } from '@/lib/runtime-assets/runtime-drift'
import { summarizeInstances, summaryFilterStatus } from '@/lib/instances/instance-summary'
import type { SummaryFilterKey } from '@/lib/instances/instance-summary'
import {
  INSTANCE_OPTIONAL_COLUMNS,
  readStoredColumns,
  readStoredViews,
  writeStoredColumns,
  writeStoredViews,
  type InstanceOptionalColumn,
  type SavedInstanceView,
} from '@/lib/instances/instance-view-prefs'
import { DataPanelSkeleton, PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { ViewToggle } from '@jianmanager/ui/components/view-toggle'
import type { ViewMode } from '@jianmanager/ui/components/view-toggle'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { Button } from '@jianmanager/ui/components/button'
import { TableCell, TableRow } from '@jianmanager/ui/components/table'
import { Input } from '@jianmanager/ui/components/input'
import { SCROLL_KEY_PREFIX, buildInstanceTreeRows } from '@/components/views/instances/VirtualizedInstanceTables'
import { FilterSelect, InstanceRowMenu, InstanceTableHeader } from '@/components/views/instances/InstanceTableParts'
import { useInstanceWindowMetrics } from '@/components/views/instances/useInstanceWindowMetrics'
import { useBusinessStatusBatch, type BusinessStatus } from '@/api/businessStatus'
import type { InstanceSortKey, InstanceSortOrder } from '@/components/views/instances/InstanceTableParts'
import { InstanceRowView } from '@/components/views/instances/InstanceRowView'
import { BackendsInline, VirtualizedGroupedCardView } from '@/components/views/instances/InstanceCardViews'
import { PaginatedInstanceTable } from '@/components/views/instances/PaginatedInstanceTable'
import { InstancePaginationBar } from '@/components/views/instances/InstancePaginationBar'
import { InstanceHealthHoverLayer } from '@/components/views/instances/InstanceRowSpark'
import { InstanceContextMenu } from '@/components/views/instances/InstanceContextMenu'
import { buildContextMenuActions } from '@/components/views/instances/instanceContextMenuActions'
import {
  applySelectionClick,
  invertSelection,
  moveColumn,
  readStoredColumnOrder,
  writeStoredColumnOrder,
  type InstanceColumnKey,
} from '@/lib/instances/instance-view-prefs'
import { InstanceGroupHeaderContent } from '@/components/views/instances/VirtualizedInstanceTables'

// 供针对性测试（rowMenu 门控）沿用既有导入路径。
export { InstanceRowMenu }

/** Radix Select 不允许空字符串 value，用哨兵值表示「全部 / 不过滤」。 */
/** 代理行内联后端摘要的取数接线层（ADR-103）：视图已入包，此处只做取数与跳转注入。 */
function BackendsInlineContainer({ proxyId }: { proxyId: number }) {
  const { data, isLoading } = useRegistrations(proxyId)
  const navigate = useNavigate()
  return (
    <BackendsInline
      registrations={data}
      isLoading={isLoading}
      onOpenInstance={(id) => navigate(`/instances/${id}`)}
    />
  )
}

const ALL = '__all__'

type InstanceUrlState = Partial<{
  q: string
  status: string
  page: string
  view: ViewMode
  groupBy: GroupDimension
  sort: InstanceSortKey
  order: InstanceSortOrder
  pageSize: string
  /** 折叠的分组键，逗号分隔、逐键 encodeURIComponent。 */
  collapsed: string
  networkId: string
  env: string
  tag: string
  nodeId: string
}>

const INSTANCE_SORT_KEYS: InstanceSortKey[] = ['name', 'status', 'createdAt', 'nodeId']
const INSTANCE_PAGE_SIZES = [50, 100, 200] as const

/** 
 * 实例搜索防抖（毫秒）：停止输入后才把 `q` 下发 `/instances/search`。
 *
 * 搜索词直接进 queryKey，不防抖就是「每个键击一次服务端搜索」——1000+ 实例下这既是
 * 无谓的请求风暴，也会让列表在输入过程中反复闪回加载态。取值与 `InstancePicker` /
 * `InstanceLibrary` / `GroupMembersDialog` 同一量级（250~300ms）。
 */
const SEARCH_DEBOUNCE_MS = 300

/** 
 * 视图模式：起 `list`（分组树表）为 `/instances` 默认——64 台跨区可一屏折叠管理。
 * 卡片视图保留为紧凑模式（`?view=card`）。
 */
function readViewMode(searchParams: URLSearchParams): ViewMode {
  return searchParams.get('view') === 'card' ? 'card' : 'list'
}

/** 列设置项的文案键（复用既有列名键，不另造同义键）。 */
const COLUMN_LABEL_KEY: Record<InstanceOptionalColumn, string> = {
  type: 'instances.type',
  role: 'instances.role',
  tags: 'grouping.tagsColumn',
  metrics: 'instances.metrics.column',
  active: 'instances.metrics.activeColumn',
  heartbeat: 'instances.metrics.heartbeatColumn',
  business: 'instances.business.column',
}

/** 默认分组维度：region/zone 两级。 */
const DEFAULT_GROUP_DIMENSION: GroupDimension = 'region'

function readGroupDimension(searchParams: URLSearchParams): GroupDimension {
  const value = searchParams.get('groupBy')
  if (value && (GROUP_DIMENSIONS as string[]).includes(value)) return value as GroupDimension
  return DEFAULT_GROUP_DIMENSION
}

/** 
 * 无折叠项时复用的常量空集合（**共享只读实例，任何调用方都不得就地修改**；
 * 本页所有变更折叠态的路径都先 `new Set(...)` 再改副本）。
 *
 * 为什么必须是常量：同步 effect 会在每次 URL 变化时用 `readCollapsed` 的结果 setState。
 * 若空值也新建 `Set`，则「URL 变了但折叠态没变」的场景（改排序、改分页、改视图…）
 * 每次都会拿到一个新身份 → 一次额外重渲染，且依赖它的 `treeRows` /
 * `allCollapsibleGroupKeys` 两个 useMemo 全部失效重算。
 */
const EMPTY_COLLAPSED: Set<string> = new Set()

/** 读取折叠分组键集合：URL `?collapsed=` 逗号分隔、逐键 decode。 */
function readCollapsed(searchParams: URLSearchParams): Set<string> {
  const raw = searchParams.get('collapsed')
  if (!raw) return EMPTY_COLLAPSED
  return new Set(
    raw
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)
      .map((s) => {
        try {
          return decodeURIComponent(s)
        } catch {
          return s
        }
      }),
  )
}

/** 序列化折叠键集合（稳定序，便于断言与缓存）。 */
function writeCollapsed(collapsed: Set<string>): string {
  return [...collapsed].sort().map((k) => encodeURIComponent(k)).join(',')
}

function readSortKey(searchParams: URLSearchParams): InstanceSortKey {
  const value = searchParams.get('sort')
  return INSTANCE_SORT_KEYS.includes(value as InstanceSortKey) ? (value as InstanceSortKey) : 'createdAt'
}

function readSortOrder(searchParams: URLSearchParams): InstanceSortOrder {
  return searchParams.get('order') === 'desc' ? 'desc' : 'asc'
}

function readPageSize(searchParams: URLSearchParams): number {
  const value = Number(searchParams.get('pageSize'))
  return INSTANCE_PAGE_SIZES.includes(value as (typeof INSTANCE_PAGE_SIZES)[number]) ? value : 50
}

function readPage(searchParams: URLSearchParams): number {
  const value = Number(searchParams.get('page'))
  return Number.isInteger(value) && value > 0 ? value : 1
}

function readParamOrAll(searchParams: URLSearchParams, key: string): string {
  return searchParams.get(key) ?? ALL
}

function writeParam(searchParams: URLSearchParams, key: string, value: string, defaultValue = ALL) {
  const next = value.trim()
  if (next === '' || next === defaultValue) searchParams.delete(key)
  else searchParams.set(key, next)
}

function writeInstanceUrlState(searchParams: URLSearchParams, updates: InstanceUrlState) {
  if (updates.q !== undefined) writeParam(searchParams, 'q', updates.q, '')
  if (updates.status !== undefined) writeParam(searchParams, 'status', updates.status)
  if (updates.page !== undefined) writeParam(searchParams, 'page', updates.page, '1')
  if (updates.view !== undefined) writeParam(searchParams, 'view', updates.view, 'list')
  if (updates.groupBy !== undefined) writeParam(searchParams, 'groupBy', updates.groupBy, DEFAULT_GROUP_DIMENSION)
  if (updates.sort !== undefined) writeParam(searchParams, 'sort', updates.sort, 'createdAt')
  if (updates.order !== undefined) writeParam(searchParams, 'order', updates.order, 'asc')
  if (updates.pageSize !== undefined) writeParam(searchParams, 'pageSize', updates.pageSize, '200')
  if (updates.collapsed !== undefined) writeParam(searchParams, 'collapsed', updates.collapsed, '')
  if (updates.networkId !== undefined) writeParam(searchParams, 'networkId', updates.networkId)
  if (updates.env !== undefined) writeParam(searchParams, 'env', updates.env)
  if (updates.tag !== undefined) writeParam(searchParams, 'tag', updates.tag)
  if (updates.nodeId !== undefined) writeParam(searchParams, 'nodeId', updates.nodeId)
}

/** 
 * proxy 行判定（能力画像 `bcTopology`）。
 *
 * 抽成模块级函数是为了让**行渲染与虚拟高度模型共用同一判据**：展开行会在同一虚拟「项」下
 * 多渲染一个 `<tr>`，模型必须按展开行高计入，两处判据一旦分叉就会重新引入行位漂移。
 */
function isProxyInstance(inst: InstanceInfo): boolean {
  return resolveCapabilities(inst).capabilities.includes('bcTopology')
}

export default function InstancesPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  // 实例入口直接写入 URL，避免列表页点击依赖临时 store 再由 Workspace 二次同步。
  const openInstance = useCallback((id: number) => navigate(`/instances/${id}`), [navigate])
  const location = useLocation()
  const [searchParams, setSearchParams] = useSearchParams()
  const hadUrlNodeRef = useRef(searchParams.has('nodeId'))
  /** 
   * 写回 URL 状态。`replace: true` 用 `history.replaceState` 原地改写当前条目，
   * 不往历史栈追加——高频来源（如搜索框逐键）必须走这条路，否则每敲一个字符
   * 就多一条历史记录，用户输 10 个字符后要按 11 次「返回」才离开本页。
   */
  const updateUrl = useCallback((updates: InstanceUrlState, options?: { replace?: boolean }) => {
    const next = new URLSearchParams(searchParams)
    writeInstanceUrlState(next, updates)
    setSearchParams(next, { replace: options?.replace })
  }, [searchParams, setSearchParams])
  const [showProvision, setShowProvision] = useState(false)
  const [showProvisionProxy, setShowProvisionProxy] = useState(false)
  const [showImport, setShowImport] = useState(false)
  const [manageProxy, setManageProxy] = useState<{ id: number; name: string } | null>(null)
  const [cloneTarget, setCloneTarget] = useState<{ id: number; name: string } | null>(null)
  const [editConfigTarget, setEditConfigTarget] = useState<InstanceInfo | null>(null)
  const [tagsTarget, setTagsTarget] = useState<{ id: number; name: string; tags: string[] } | null>(null)
  // 资源限额编辑目标：携带启动方式与当前限额回填。
  const [limitsTarget, setLimitsTarget] = useState<{
    id: number
    name: string
    processType: string
    cpuLimit: number
    memLimitMb: number
    diskLimitMb: number
  } | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<{ id: number; name: string; inPlace?: boolean; running?: boolean } | null>(null)
  const [killTarget, setKillTarget] = useState<{ id: number; name: string } | null>(null)
  /* 
    启停二次确认（用户提出）：启动/停止/重启都会打断正在游戏的玩家，尤其「停止」会把在线玩家直接踢下线，
    误点代价不可忽略。此前行内图标按钮与「⋯」菜单都是**一发即执行**，没有任何确认。
    这里统一收敛为「先置入待确认目标，DangerConfirm 确认后才发请求」——
    与删除/强杀/接管漂移完全同一套纪律（那三处早已如此，只有启停是漏的）。
  */
  /** 右键菜单目标与光标坐标：**全表一份**，由行上报（见 InstanceContextMenu 的注释）。 */
  const [contextMenu, setContextMenu] = useState<{ inst: InstanceInfo; x: number; y: number } | null>(null)
  const [lifecycleTarget, setLifecycleTarget] = useState<{ id: number; name: string; op: 'start' | 'stop' | 'restart' } | null>(null)
  // 接管运行态漂移：先把目标置入待确认，DangerConfirm 确认后才发请求（与强杀同款纪律）。
  const [adoptTarget, setAdoptTarget] = useState<{ id: number; name: string; pid: number } | null>(null)
  // 批量操作选中的实例 ID 集合。
  const [selectedIds, setSelectedIds] = useState<number[]>([])
  // 工作台卡 ⇄ 列表视图（§4.5）；运行实体默认卡片。
  const [view, setView] = useState<ViewMode>(() => readViewMode(searchParams))
  // 分组树表：折叠态入 URL（`?collapsed=`），切换维度即时重排（客户端聚合）。
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(() => readCollapsed(searchParams))
  // 「管理分组」面板开关（的组织分组能力在 groupTree 维度内可达，不再单开 orgView 视图）。
  const [groupManagerOpen, setGroupManagerOpen] = useState(false)
  // proxy 行 inline 展开已注册 backend 的代理 id 集合。
  const [expandedProxies, setExpandedProxies] = useState<Set<number>>(new Set())

  // 多维筛选状态：群组 / 环境 / 标签 / 节点 / 状态任意组合，下发后端过滤。
  const [searchQuery, setSearchQuery] = useState(() => searchParams.get('q') ?? '')
  // 防抖后的搜索词才是「查询真源」：输入框保持即时回显，服务端搜索等落定。
  const debouncedSearchQuery = useDebounced(searchQuery, SEARCH_DEBOUNCE_MS)
  const [networkId, setNetworkId] = useState<string>(() => readParamOrAll(searchParams, 'networkId'))
  const [env, setEnv] = useState<string>(() => readParamOrAll(searchParams, 'env'))
  const [tag, setTag] = useState<string>(() => readParamOrAll(searchParams, 'tag'))
  const selectedNodeId = useConsoleStore((s) => s.selectedNodeId)
  const setSelectedNodeId = useConsoleStore((s) => s.setSelectedNodeId)
  const nodeId = selectedNodeId == null ? ALL : String(selectedNodeId)
  const setNodeFilter = (value: string) => {
    setSelectedNodeId(value === ALL ? null : Number(value))
    updateUrl({ nodeId: value, page: '1' })
  }
  const [statusFilter, setStatusFilter] = useState<string>(() => readParamOrAll(searchParams, 'status'))

  // 分组视图维度。
  const [groupBy, setGroupBy] = useState<GroupDimension>(() => readGroupDimension(searchParams))
  const [sortKey, setSortKey] = useState<InstanceSortKey>(() => readSortKey(searchParams))
  const [sortOrder, setSortOrder] = useState<InstanceSortOrder>(() => readSortOrder(searchParams))

  // 页面上报「对外可说什么」（§5.1）：只发结构，不发正文。
  // 上报值全部来自 URL 派生状态（筛选/分组/排序），不读内部 state 对象、不遍历 DOM。
  // 敏感键由 serializePageContext 统一丢弃，这里不重复判定。
  // 空值不塞进 filters：一个 `status=all` 对模型没有信息量，反而占长度。
  const pageContextFilters = useMemo(() => {
    const f: Record<string, string> = {}
    if (statusFilter && statusFilter !== ALL) f.status = statusFilter
    if (groupBy) f.groupBy = groupBy
    return f
  }, [statusFilter, groupBy])
  usePageContext(
    useMemo(
      () => ({
        pageKey: 'instances',
        ...(Object.keys(pageContextFilters).length ? { filters: pageContextFilters } : {}),
      }),
      [pageContextFilters],
    ),
  )
  const [page, setPage] = useState(() => readPage(searchParams))
  const [pageSize, setPageSize] = useState(() => readPageSize(searchParams))
  const [filtersCollapsed, setFiltersCollapsed] = useState(false)
  // 列设置（页面重构件）：可见可选列持久化于 jm.instances.columns；缺省全开（旧行为不变）。
  const [visibleColumns, setVisibleColumns] = useState<Set<InstanceOptionalColumn>>(readStoredColumns)
  /* 
    列顺序（用户提出的表头拖拽换位）。与可见列分开存储：
    一个管「显示哪些列」、一个管「按什么顺序排」，两者正交——隐藏再显示某列时，
    它在顺序里的位置仍然保留，用户不用重排一遍。
  */
  const [columnOrder, setColumnOrder] = useState<InstanceColumnKey[]>(readStoredColumnOrder)
  // 保存视图：整串 search 快照 + 列集合（jm.instances.views）。恢复=导航回快照，
  // 由 `location.search → 状态` 的同步 effect 落全部字段（过滤/分组/排序/页大小天然保真）。
  const [savedViews, setSavedViews] = useState<SavedInstanceView[]>(readStoredViews)
  const [saveViewOpen, setSaveViewOpen] = useState(false)
  const [viewNameDraft, setViewNameDraft] = useState('')
  const scrollStorageKey = useMemo(
    () => `${SCROLL_KEY_PREFIX}${location.pathname}${location.search}`,
    [location.pathname, location.search],
  )

  useEffect(() => {
    const nextParams = new URLSearchParams(location.search)
    let cancelled = false
    queueMicrotask(() => {
      if (cancelled) return
      setSearchQuery(nextParams.get('q') ?? '')
      setNetworkId(readParamOrAll(nextParams, 'networkId'))
      setEnv(readParamOrAll(nextParams, 'env'))
      setTag(readParamOrAll(nextParams, 'tag'))
      setStatusFilter(readParamOrAll(nextParams, 'status'))
      setView(readViewMode(nextParams))
      setGroupBy(readGroupDimension(nextParams))
      setSortKey(readSortKey(nextParams))
      setSortOrder(readSortOrder(nextParams))
      setPage(readPage(nextParams))
      setPageSize(readPageSize(nextParams))
      setCollapsedGroups(readCollapsed(nextParams))

      const hasNode = nextParams.has('nodeId')
      if (hasNode) {
        setSelectedNodeId(Number(nextParams.get('nodeId')))
      } else if (hadUrlNodeRef.current) {
        setSelectedNodeId(null)
      }
      hadUrlNodeRef.current = hasNode
    })
    return () => { cancelled = true }
  }, [location.search, setSelectedNodeId])

  const params: InstanceListParams = useMemo(() => {
    const p: InstanceListParams = {}
    if (networkId !== ALL) p.networkId = Number(networkId)
    if (env !== ALL) p.env = env
    if (tag !== ALL) p.tag = tag
    if (nodeId !== ALL) p.nodeId = Number(nodeId)
    if (statusFilter !== ALL) p.status = statusFilter
    return p
  }, [networkId, env, tag, nodeId, statusFilter])

  const instanceSearchParams: Omit<InstanceSearchParams, 'page'> = useMemo(() => ({
    ...params,
    ...(debouncedSearchQuery.trim() ? { q: debouncedSearchQuery.trim() } : {}),
    pageSize,
    sort: sortKey,
    order: sortOrder,
  }), [debouncedSearchQuery, pageSize, params, sortKey, sortOrder])
  /* 
    列表改为**服务端分页**（之后的服务器页重构）：
    原先是无限滚动 + 虚拟窗口，一次滞千条；改成标准分页表格后，每页 pageSize 条，
    页码进 URL（已有 `page` 状态），刷新与深链都能回到同一页。
  */
  const {
    data: searchData,
    isLoading,
    isFetching,
  } = useSearchInstances({ ...instanceSearchParams, page }, true)
  const instances = useMemo(() => searchData?.items ?? [], [searchData])
  const totalCount = searchData?.total ?? instances.length
  const aggregateParams = useMemo(() => {
    const next: InstanceListParams = { ...params }
    delete next.status
    return next
  }, [params])
  const { data: aggregate } = useInstanceAggregate(aggregateParams)
  const { data: nodes } = useNodes()
  const { data: networks } = useNetworks()
  // `network` / `groupTree` 维度取数：均为一次请求、无 N+1；按维度分别按需预取——
  // network 维度只需 `/topology`（群组成员映射），groupTree 维度只需 `/instance-groups`（分组树）；
  // 其余维度两者皆不请求（避免「任一为真即同时预取」造成多余请求）。
  const { data: topology } = useTopology({ enabled: groupBy === 'network' })
  const { data: groupTree } = useInstanceGroups({ enabled: groupBy === 'groupTree' })

  const start = useStartInstance()
  const stop = useStopInstance()
  const restart = useRestartInstance()
  const kill = useKillInstance()
  const adopt = useAdoptInstanceRuntime()
  const del = useDeleteInstance()

  // 批量选择：select-all 作用于当前筛选后的可见集合。
  //
  // 已选集合改 `Set`：行渲染按 `selectedIds.includes(id)` 判定，`includes` 是 O(已选数)——
  // 全选 1200 台后「可见行 × 1200」的线性扫描会成为渲染热点（对照 NetworksPageView 同款处理）。
  const selectedIdSet = useMemo(() => new Set(selectedIds), [selectedIds])
  /* 
    Shift 连选要按「用户看到的顺序」取区间，而不是 id 顺序。
    分组维度下展示顺序来自 treeRows（组头 + 成员），平铺视图来自 instances；
    两者都只取实例 id、跳过组头行。
  */
  const selectionOrderIds = useMemo<number[]>(() => instances.map((i) => i.id), [instances])
  /** 
   * 单次点击选择（含 Shift 连选 / Ctrl 点选）。
   *
   * 语义统一由 `applySelectionClick` 决定（纯函数，单测穷举各修饰键组合），
   * 列表与卡片两个视图共用——避免「列表能 Shift 连选、卡片不能」这类不一致。
   * 连选区间按**当前展示顺序**（`selectionOrderIds`）取，而非 id 大小。
   */
  const toggleOneWithModifiers = (
    id: number,
    modifiers: { shiftKey?: boolean; ctrlKey?: boolean; metaKey?: boolean } = {},
  ) => {
    setSelectedIds((prev) => [...applySelectionClick({
      orderedIds: selectionOrderIds,
      selected: new Set(prev),
      targetId: id,
      shiftKey: modifiers.shiftKey,
      ctrlKey: modifiers.ctrlKey,
      metaKey: modifiers.metaKey,
    })])
  }
  /** 兼容旧调用：无修饰键的单项切换（复选框点选即此类）。 */
  const toggleOne = (id: number) =>
    setSelectedIds((prev) =>
      prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id],
    )
  const allIds = useMemo(() => instances?.map((i) => i.id) ?? [], [instances])
  const allSelected = allIds.length > 0 && allIds.every((id) => selectedIdSet.has(id))
  const toggleAll = () => setSelectedIds(allSelected ? [] : allIds)
  /** 
   * 反选（用户提出）：在当前可见集合内翻转。
   * 只翻转当前页/筛选结果里的项——否则会静默丢掉用户在其他页选好的内容。
   */
  const invertAll = () => setSelectedIds((prev) => [...invertSelection(allIds, new Set(prev))])
  const clearSelection = () => setSelectedIds([])
  // 选中实例的 {id,name,status}，供批量栏做状态感知禁用与部分失败明细。
  const selectedInstances = useMemo(
    () =>
      instances
        .filter((i) => selectedIdSet.has(i.id))
        .map((i) => ({ id: i.id, name: i.name, status: i.status })),
    [instances, selectedIdSet],
  )

  const scopedAllInstances = useMemo(
    () => (selectedNodeId == null ? instances : instances.filter((i) => i.nodeId === selectedNodeId)),
    [instances, selectedNodeId],
  )
  // 环境 / 标签候选由「已加载分页」推导（见下方 FilterSelect 处的局限说明）。
  const envOptions = useMemo(() => collectEnvs(scopedAllInstances), [scopedAllInstances])
  const tagOptions = useMemo(() => collectTags(scopedAllInstances), [scopedAllInstances])
  // 节点 id → 名称：行渲染逐行调用 `nodeName`，若每次线性 `find` 节点表，
  // 「可见行 × 节点数」会随两侧规模一起放大，故先建索引再查。
  const nodeNameById = useMemo(() => {
    const map = new Map<number, string>()
    for (const n of nodes ?? []) map.set(n.id, n.name)
    return map
  }, [nodes])
  const nodeName = useCallback(
    (id: number) => nodeNameById.get(id) ?? t('console.unknownNode', { id }),
    [nodeNameById, t],
  )

  // 「实例→分组键」映射：仅 network 维度需要外部映射（键=群组名）；
  // groupTree 维度走独立的层级聚合（键=组 id + 按 parentId 重建的层级，见下）。
  // 维度数据源不可用（预取未启用/未加载）时返回空映射 → 该维度整体落「未分组」，绝不借错数据源。
  const networkKeyMap = useMemo(() => {
    if (groupBy !== 'network') return undefined
    return buildKeyMap((topology?.networks ?? []).map((n) => ({ key: n.name, instanceIds: n.memberInstanceIds })))
  }, [groupBy, topology])

  // groupTree 维度组键 → 展示元数据（组名 / 祖先路径），供组头命名；键为组 id（同名不同组不合并）。
  const groupTreeLabels = useMemo(
    () => (groupBy === 'groupTree' ? buildGroupTreeSource(groupTree ?? []).labels : undefined),
    [groupBy, groupTree],
  )

  // 分组聚合：groupTree 走多级层级聚合（父组头 → 子组头 → 成员）；其余维度单级/两级。
  const groups = useMemo(() => {
    if (groupBy === 'groupTree') return groupInstancesByGroupTree(instances, groupTree ?? [])
    return groupInstances(instances, groupBy, networkKeyMap)
  }, [instances, groupBy, networkKeyMap, groupTree])

  // 汇总头计数随页眉节点作用域收敛，但不受状态/标签等本页细筛选影响。
  const counts = useMemo(() => {
    if (!aggregate) return summarizeInstances(scopedAllInstances)
    const byStatus = aggregate.byStatus
    return {
      total: aggregate.total,
      running: (byStatus.RUNNING ?? 0) + (byStatus.STARTING ?? 0) + (byStatus.STOPPING ?? 0),
      stopped: byStatus.STOPPED ?? 0,
      crashed: byStatus.CRASHED ?? 0,
    }
  }, [aggregate, scopedAllInstances])

  /** 
   * 表头全选框的可访问名。
   *
   * 列表分页加载，「全选」实际只覆盖**已加载页**（`allIds = instances.map(...)`），
   * 而页面上同时显示「已加载 N / 共 M」——两处口径不一致时，用户会把「全选」
   * 当成「全库全选」，批量删除的作用域与理解不符。这里把作用域直接写进可访问名，
   * 复用既有文案（`instanceBatch.selectAll` + `instances.loadedOfTotal`），不新增 i18n 键。
   */
  const selectAllLabel = `${t('instanceBatch.selectAll')} · ${t('instances.loadedOfTotal', {
    loaded: instances.length,
    total: totalCount,
  })}`

  /** 
   * 搜索框：输入即时回显，但服务端 `q` 走防抖（见 `SEARCH_DEBOUNCE_MS`）。
   * URL 同步改为 replace——逐键 push 会把历史栈塞满（输 10 个字符要按 11 次返回）。
   */
  const setSearchQueryFilter = (value: string) => {
    setSearchQuery(value)
    updateUrl({ q: value, page: '1' }, { replace: true })
  }
  const setNetworkFilter = (value: string) => {
    setNetworkId(value)
    updateUrl({ networkId: value, page: '1' })
  }
  const setEnvFilter = (value: string) => {
    setEnv(value)
    updateUrl({ env: value, page: '1' })
  }
  const setTagFilter = (value: string) => {
    setTag(value)
    updateUrl({ tag: value, page: '1' })
  }
  const setStatusFilterParam = (value: string) => {
    setStatusFilter(value)
    updateUrl({ status: value, page: '1' })
  }
  const setViewParam = (value: ViewMode) => {
    setView(value)
    updateUrl({ view: value })
  }
  const setGroupByParam = (value: GroupDimension) => {
    setGroupBy(value)
    updateUrl({ groupBy: value, page: '1' })
  }
  const setSortKeyParam = (value: InstanceSortKey) => {
    setSortKey(value)
    updateUrl({ sort: value, page: '1' })
  }
  const setSortOrderParam = (value: InstanceSortOrder) => {
    setSortOrder(value)
    updateUrl({ order: value, page: '1' })
  }
  const setTableSortParam = (value: InstanceSortKey) => {
    const nextOrder = sortKey === value && sortOrder === 'asc' ? 'desc' : 'asc'
    setSortKey(value)
    setSortOrder(nextOrder)
    updateUrl({ sort: value, order: nextOrder, page: '1' })
  }
  const setPageSizeParam = (value: string) => {
    const parsed = Number(value)
    const next = INSTANCE_PAGE_SIZES.includes(parsed as (typeof INSTANCE_PAGE_SIZES)[number]) ? parsed : 50
    setPageSize(next)
    updateUrl({ pageSize: String(next), page: '1' })
  }
  /** 切换某分组折叠态并写回 URL（`?collapsed=`）。 */
  const toggleGroupCollapsed = (key: string) => {
    // 纯计算放在 updater 之外：setState updater 须为纯函数（URL 写入是副作用，不能在里面）。
    const next = new Set(collapsedGroups)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    setCollapsedGroups(next)
    updateUrl({ collapsed: writeCollapsed(next) })
  }
  const setAllCollapsed = (collapse: boolean, keys: string[]) => {
    const next = collapse ? new Set(keys) : new Set<string>()
    setCollapsedGroups(next)
    updateUrl({ collapsed: writeCollapsed(next) })
  }

  const hasActiveFilter =
    searchQuery.trim() !== '' || networkId !== ALL || env !== ALL || tag !== ALL || nodeId !== ALL || statusFilter !== ALL
  /** 
   * 生效中的筛选条件数：给「筛选」按钮的徽标用。
   * 只数面板里的五个维度，不含搜索词——搜索框自己就可见，重复计入会让人误以为
   * 还有一个没找到的筛选条件。
   */
  const activeFilterCount = [networkId, env, tag, nodeId, statusFilter].filter((v) => v !== ALL).length
  const resetFilters = () => {
    setSearchQuery('')
    setNetworkId(ALL)
    setEnv(ALL)
    setTag(ALL)
    setSelectedNodeId(null)
    setStatusFilter(ALL)
    updateUrl({ q: '', networkId: ALL, env: ALL, tag: ALL, nodeId: ALL, status: ALL, page: '1' })
  }

  // 汇总 chip 点击 → 设状态筛选（再点同项=清空），可发现「不正常的」（§6.3 #4）。
  const applySummaryFilter = (key: SummaryFilterKey) => {
    const target = summaryFilterStatus(key)
    const next = statusFilter === (target ?? ALL) ? ALL : (target ?? ALL)
    setStatusFilterParam(next)
  }

  /* 
    KPI 卡：把 summaryChips 同一批数据换成「图标 + 大数字」形态。
    刻意**不加**「总在线玩家」这张卡——它的口径是跨全部实例求和，
    与「按状态筛选」不是同一维度，点它筛什么说不清；先不引入这个歧义。
  */
  const summaryStatCards = [
    {
      key: 'all',
      label: t('grouping.all'),
      value: counts.total.toLocaleString('zh-CN'),
      icon: ServerCog,
      tone: 'neutral' as const,
      active: statusFilter === ALL,
      onClick: () => resetFilters(),
    },
    {
      key: 'running',
      label: t('instances.running'),
      value: counts.running.toLocaleString('zh-CN'),
      icon: CircleCheck,
      tone: 'success' as const,
      active: statusFilter === 'RUNNING',
      onClick: () => applySummaryFilter('running'),
    },
    {
      key: 'stopped',
      label: t('instances.stopped'),
      value: counts.stopped.toLocaleString('zh-CN'),
      icon: CircleSlash,
      tone: 'neutral' as const,
      active: statusFilter === 'STOPPED',
      onClick: () => applySummaryFilter('stopped'),
    },
    {
      key: 'crashed',
      label: t('instances.crashed'),
      value: counts.crashed.toLocaleString('zh-CN'),
      icon: TriangleAlert,
      tone: 'danger' as const,
      active: statusFilter === 'CRASHED',
      onClick: () => applySummaryFilter('crashed'),
    },
  ]

  const statusConfig: Record<string, { text: string; variant: 'default' | 'secondary' | 'destructive' | 'outline' }> = {
    STOPPED: { text: t('instances.stopped'), variant: 'secondary' },
    STARTING: { text: t('instances.starting'), variant: 'outline' },
    RUNNING: { text: t('instances.running'), variant: 'default' },
    STOPPING: { text: t('instances.stopping'), variant: 'outline' },
    CRASHED: { text: t('instances.crashed'), variant: 'destructive' },
  }

  /** 
   * 按分组维度给出分组标题（各维度统一落此处）；空 key 显示该维度的「未分…」文案。
   * 多级维度由 `level` 区分层级（region：外层大区 / 内层小区；groupTree：按 `groupTreeLabels` 的组名）。
   */
  const groupLabel = (key: string, level: number = 0): string => {
    switch (groupBy) {
      case 'node':
        return nodeName(Number(key))
      case 'env':
        return key === '' ? t('grouping.envNone') : t(`grouping.env_${key}`, { defaultValue: key })
      case 'status':
        return statusConfig[key]?.text ?? key
      case 'region':
        if (level === 1) return key === '' ? t('grouping.zoneNone') : key
        return key === '' ? t('grouping.regionNone') : key
      case 'zone':
        return key === '' ? t('grouping.zoneNone') : key
      case 'role':
        return key === '' ? t('grouping.ungrouped') : t(`networks.role_${key}`, { defaultValue: key })
      case 'type':
        // 类型列与列表行一致显示原始枚举值（minecraft_java / generic），不额外造 i18n 键。
        return key === '' ? t('grouping.ungrouped') : key
      case 'groupTree':
        // 键为组 id（`g:<id>`），组名从分组树元数据取（同 id 唯一，同名不同组各自成组）。
        return key === '' ? t('grouping.ungrouped') : (groupTreeLabels?.get(key)?.name ?? key)
      case 'network':
        return key === '' ? t('grouping.ungrouped') : key
      default:
        return ''
    }
  }

  // 树表行模型：分组头 + 成员行统一行模型，供单个虚拟表渲染。
  const treeRows = useMemo(
    () => buildInstanceTreeRows(groups, groupBy, groupLabel, collapsedGroups),
    // groupLabel 依赖 nodes/t/groupBy/groupTreeLabels，随其变化重算；折叠态变化即时反映。
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [groups, groupBy, collapsedGroups, nodes, t, groupTreeLabels],
  )
  /** 当前可折叠的全部分组键（「全部折叠」用），不含已折叠分支不可见的子键统计差异。 */
  const allCollapsibleGroupKeys = useMemo(
    () => treeRows.filter((r) => r.kind === 'group').map((r) => (r as { collapseKey: string }).collapseKey),
    [treeRows],
  )

  const buildMenu = (inst: InstanceInfo) => {
    // 运行态漂移：无漂移为 undefined，据此决定是否给出「接管」菜单项。
    const drift = runtimeDriftOf(inst)
    return (
      <InstanceRowMenu
        inst={inst}
        // 生命周期动作同时出现在行内主按钮与「⋯」菜单：列表行内只留当前状态最常用的那一个，
        // 卡片视图没有行内按钮，全靠菜单——两处共用同一批 mutation，不走第二条路径。
        onStart={() => setLifecycleTarget({ id: inst.id, name: inst.name, op: 'start' })}
        onStop={() => setLifecycleTarget({ id: inst.id, name: inst.name, op: 'stop' })}
        onRestart={() => setLifecycleTarget({ id: inst.id, name: inst.name, op: 'restart' })}
        onTags={() => setTagsTarget({ id: inst.id, name: inst.name, tags: parseTags(inst.tags) })}
        onEditConfig={() => setEditConfigTarget(inst)}
        onLimits={() => setLimitsTarget({
          id: inst.id,
          name: inst.name,
          processType: inst.processType,
          cpuLimit: inst.cpuLimit ?? 0,
          memLimitMb: inst.memLimitMb ?? 0,
          diskLimitMb: inst.diskLimitMb ?? 0,
        })}
        onProxy={() => setManageProxy({ id: inst.id, name: inst.name })}
        onClone={() => setCloneTarget({ id: inst.id, name: inst.name })}
        // 接管运行态漂移：仅当目录下确有未纳管活进程时给出入口。
        onAdoptRuntime={
          drift ? () => setAdoptTarget({ id: inst.id, name: inst.name, pid: drift.pid }) : undefined
        }
        onDelete={() => setDeleteTarget({
          id: inst.id,
          name: inst.name,
          inPlace: !!inst.workDirInPlace,
          // 非停止态删除由后端编排「先停止（超时强杀）再删除」，确认框据此提示。
          running: inst.status !== 'STOPPED' && inst.status !== 'CRASHED',
        })}
      />
    )
  }

  const toggleProxy = (id: number) =>
    setExpandedProxies((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  /** 
   * 可见窗口上报（虚拟表 → 页面）：表格把 `range` 变化回报，页面据此切片实例列表，
   * 走 useInstanceWindowMetrics 批量取行内指标（≤50/次、300ms 防抖、30s 轮询）后逐行注入。
   * 下标语义随视图：平铺表按 `instances`、分组树表按 `treeRows`——两者由各自表格的 range 决定。
   */
  /* 
    行内指标只取**当前页**的实例（原先是虚拟窗口的可见区间）。
    改成分页后「可见」= 当前页，语义更简单；每页 50 条也远小于原先一屏能滚到的规模，
    批量取指标的压力反而更小。
  */
  const visibleInstances = useMemo(() => instances, [instances])

  const windowMetrics = useInstanceWindowMetrics(visibleInstances)

  /* 
    业务域状态：**只在「业务插件」列开启时才取数**。

    理由：这条链路会实打实打到探针（批量端点每次最多 50 台）。
    列关着还去取，等于让每个打开实例页的用户都白白打一轮探针。

    批量端点上限 50 台，故按当前页切片——分页大小可能被用户调到 100+，
    超出部分由后续页各自承担，不在这里一次性全发。
  */
  const businessColumnOn = visibleColumns.has('business')
  const businessIds = useMemo(
    () => (businessColumnOn ? visibleInstances.slice(0, 50).map((i) => i.id) : []),
    [businessColumnOn, visibleInstances],
  )
  const { data: businessRows } = useBusinessStatusBatch(businessIds, businessColumnOn)
  const businessById = useMemo(() => {
    const map = new Map<number, BusinessStatus>()
    for (const row of businessRows ?? []) {
      if (row.status) map.set(row.instanceId, row.status)
    }
    return map
  }, [businessRows])

  /** 
   * 当前表格实际渲染的列数：分组头行/空态占位的 colSpan 必须与之一致。
   *
   * 【为什么不再写死 11】列模型已从 11 列合并为 8 列（三列指标合并成健康度、身份列合并为一格），
   * 此前仍按 11 减，colSpan 比实际列数大 3——浏览器会截断，视觉无害但语义错。
   * 这里按「固定 6 列 + 两个可合并组是否可见」现算，与表头/行/colgroup 同一判据。
   */
  const hasIdentity = (['type', 'role', 'tags'] as const).some((c) => visibleColumns.has(c))
  const hasHealth = (['metrics', 'active', 'heartbeat'] as const).some((c) => visibleColumns.has(c))
  const columnCount = 6 + (hasIdentity ? 1 : 0) + (hasHealth ? 1 : 0)

  /** 
   * 表头拖拽换位：把 from 列移动到 to 列的位置，并持久化。
   * `moveColumn` 是纯函数（钉住的列不参与），这里只负责落状态与存储。
   */
  const reorderColumn = (from: InstanceColumnKey, to: InstanceColumnKey) => {
    setColumnOrder((prev) => {
      const next = moveColumn(prev, from, to)
      writeStoredColumnOrder(next)
      return next
    })
  }

  /** 列设置：切换可选列可见性并持久化。 */
  const toggleColumn = (column: InstanceOptionalColumn) => {
    setVisibleColumns((prev) => {
      const next = new Set(prev)
      if (next.has(column)) next.delete(column)
      else next.add(column)
      writeStoredColumns(next)
      return next
    })
  }


  /** 应用保存的视图：导航回快照 search（状态由同步 effect 落位），并恢复列集合。 */
  const applySavedView = (view: SavedInstanceView) => {
    navigate(`/instances${view.search}`)
    const next = new Set(view.columns)
    setVisibleColumns(next)
    writeStoredColumns(next)
  }

  /** 保存当前视图（同名覆盖）；快照 = 当前 location.search + 当前列集合。 */
  const saveCurrentView = () => {
    const name = viewNameDraft.trim()
    if (!name) return
    const next = [
      ...savedViews.filter((view) => view.name !== name),
      { name, search: location.search, columns: [...visibleColumns] },
    ]
    setSavedViews(next)
    writeStoredViews(next)
    setSaveViewOpen(false)
  }

  const deleteSavedView = (name: string) => {
    const next = savedViews.filter((view) => view.name !== name)
    setSavedViews(next)
    writeStoredViews(next)
  }

  /** 
   * 行装配（ADR-103）：行展示件已入包（平铺表与分组树表共用），此处只注入数据与回调。
   * 逐行判别留在应用侧：节点名（查节点表）、代理画像、搭建中、运行态漂移、
   * 三个动作的按 id 在途态（别的行在提交不该禁用本行）。
   * proxy 内联展开与批量勾选是跨行共享状态，故仍由本页持有——虚拟窗口滚动会卸载行，
   * 行内自持会丢失展开态；勾选还与表头全选、批量栏同源。
   */
  const renderRow = (inst: InstanceInfo) => (
    <InstanceRowView
      inst={inst}
      nodeName={nodeName(inst.nodeId)}
      selected={selectedIdSet.has(inst.id)}
      onToggleSelect={() => toggleOne(inst.id)}
      isProxy={isProxyInstance(inst)}
      provisioning={isProvisioningInstance(inst)}
      drift={runtimeDriftOf(inst)}
      metrics={windowMetrics.get(inst.uuid)}
      businessStatus={businessById.get(inst.id)}
      columns={visibleColumns}
      columnOrder={columnOrder}
      proxyExpanded={expandedProxies.has(inst.id)}
      onToggleProxy={() => toggleProxy(inst.id)}
      renderBackends={() => <BackendsInlineContainer proxyId={inst.id} />}
      starting={start.isPending && start.variables === inst.id}
      stopping={stop.isPending && stop.variables === inst.id}
      menu={buildMenu(inst)}
      onOpenInstance={openInstance}
      onStart={() => setLifecycleTarget({ id: inst.id, name: inst.name, op: 'start' })}
      onStop={() => setLifecycleTarget({ id: inst.id, name: inst.name, op: 'stop' })}
      onKill={() => setKillTarget({ id: inst.id, name: inst.name })}
      onContextMenu={(row, x, y) => setContextMenu({ inst: row, x, y })}
    />
  )

  return (
    // 阶段 6 页面迁移：外壳与页头改用布局层原语（PageShell / PageHeader）。
    // data-page 由 PageShell spread 透传，e2e 的就绪信号依赖它。
    // variant="fixed"：本页的滚动发生在**表格内部**（表头吸顶 + 容器内滚），不是整页滚动。
    // `default` 变体会给一个 overflow-auto 的整页滚动容器，页内 `flex-1` 就拿不到
    // 「视口剩余高度」，退回按内容撑高、又被外层滚动——两层滚动叠加，表格底部被推出视口。
    // fixed 是 overflow-hidden，高度钉在视口内，`flex-1` 才有意义。
    <PageShell data-page="instances" variant="fixed">
      {/*
        【页头只留小标题 + 右侧按钮】此前标题下还有一行「42 台服务器 · 36 运行中 · 3 异常 ·
        2,431 在线玩家」的统计副标题——那些数字已经由下面的 KPI 卡片承担，两处重复表达同一信息，
        既占高度又分散注意力。现在标题行只负责「我在哪一页」这一件事。
      */}
      <PageHeader
        className="[&_[data-slot=page-header]]:gap-0"
        title={t('instances.title')}
        description={t('instances.subtitle')}
        actions={
          /* 
            【四种新建入口收成一个分裂按钮】
            「一键搭建 / 搭建代理 / 导入现有服务器 / 创建实例」四个并列按钮占满整个页头右侧，
            每个都是同样权重，用户无法一眼分出主次——而它们其实是同一件事（弄一个实例出来）
            的四种方式。改为：主按钮直接进最常用的「创建实例」，右侧▾ 展开另外三种。
            主次由此分明，页头宽度也不再被四个按钮吃掉。
          */
          <div className="flex items-stretch">
            <Button onClick={() => navigate('/instances/new')} className="rounded-r-none">
              <Plus className="size-4" /> {t('instances.createInstance')}
            </Button>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  aria-label={t('instances.moreCreateWays')}
                  title={t('instances.moreCreateWays')}
                  className="rounded-l-none border-l border-primary-foreground/25 px-1.5"
                >
                  <ChevronDown className="size-4" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-52">
                <DropdownMenuItem onSelect={() => setShowProvision(true)}>
                  <Zap className="size-4" /> {t('provision.entry')}
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={() => setShowProvisionProxy(true)}>
                  <Globe className="size-4" /> {t('proxy.entry')}
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={() => setShowImport(true)}>
                  <HardDriveDownload className="size-4" /> {t('importServer.entry')}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        }
      />

      {/*
        概览区：KPI 卡片（图标 + 大数字，可点一键筛选）。
        与原来的 SummaryChips（一排 pill）相比，这里读的是**数字**而不是文字——
        「有多少台在跑、有多少台挂了」是这一页最先被扫的信息，值得给到卡片级的权重。
        卡片直接复用 ui 包的 `StatCard`，只额外传 `onClick` / `active`；
        激活态由 `statusFilter` 驱动，点同一张再点一次即取消（与旧 chip 行为一致）。
      */}
      <div role="group" aria-label={t('instances.summaryLabel')} className="grid gap-2.5 [grid-template-columns:repeat(auto-fit,minmax(150px,1fr))]">
        {summaryStatCards.map((card) => (
          <StatCard
            key={card.key}
            label={card.label}
            value={card.value}
            icon={<card.icon className="size-3.5" aria-hidden="true" />}
            tone={card.tone}
            onClick={card.onClick}
            active={card.active}
          />
        ))}
      </div>

      {/* 多维筛选 + 分组视图（137）：sticky + 可折叠，避免长筛选条在移动端翻屏。 */}
      {/*
        工具栏收成一张明确的卡片：此前它只是 `jm-toolbar-surface`（半透明白底），
        与页面底色差不到 1% 明度，视觉上是「飘着的一排控件」而不是「一块工具区」。
        改成带描边与内边距的实心面板后，搜索 / 筛选 / 视图切换才有归属。
      */}
      {/* 吸附工具栏：Card 承载统一表面（圆角/描边/底色/阴影）。
          原为块级容器且内部两段无间距，故 gap 置零；sticky 定位与毛玻璃按需保留。 */}
      {/*
        【工具栏收敛为「一行主导航 + 一个可折叠筛选面板」】
        改前是 4 行控件（搜索/排序/页大小/收起筛选 → 卡片列表切换/列设置/视图/折叠展开 →
        群组/环境/标签/节点/状态 → 分组），其中「排序/排序方向/每页条数/列设置/保存视图/
        全部折叠/全部展开」都是低频设置，却与「搜索」和「卡片/列表」同为一级可见，
        一级控件因此被淹没，用户每次都要在四行里找。

        现在一级只留三个动作：搜索、看哪种视图（卡片/列表）、筛什么（带条件数徽标）；
        其余按语义归入两个菜单——「显示」放呈现相关的（排序/每页/列/分组维度/折叠），
        「视图」只放保存的快照。低频不再占位，但功能一个都没删。
      */}
      {/*
        【去掉 Card 外壳，与数据区合并成一层】此前筛选条是独立一张卡片（自带 border/底色/
        内边距），下面又是另一张表格卡片——两层壳之间还有 12px 间距，首屏被吃掉约 115px。
        现在筛选条直接作为数据区的头部：保留吸附与毛玻璃，但不再有自己的边框，
        视觉上与表格同属一块。
      */}
      <div className="sticky top-16 z-20 -mx-1 rounded-lg bg-background/85 px-1 py-1.5 backdrop-blur-sm">
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative min-w-52 flex-1 sm:flex-none">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              type="search"
              aria-label="搜索实例"
              value={searchQuery}
              onChange={(event) => setSearchQueryFilter(event.target.value)}
              placeholder="搜索实例 / host / 标签"
              className="h-8 pl-8"
            />
          </div>
          <div className="ml-auto flex items-center gap-2">
            {/* 视图切换：卡片 / 列表。一级常驻——它决定「怎么看这份数据」。 */}
            <ViewToggle
              value={view}
              onChange={setViewParam}
              cardLabel={t('grouping.viewCard')}
              listLabel={t('grouping.viewList')}
            />
            {/* 筛选：面板开合 + 生效条件数徽标——徽标让「我筛过东西」无需展开即可见。 */}
            <Button
              variant={filtersCollapsed ? 'outline' : 'default'}
              size="sm"
              onClick={() => setFiltersCollapsed((v) => !v)}
              aria-expanded={!filtersCollapsed}
              aria-controls="instances-advanced-filters"
            >
              <SlidersHorizontal className="size-4" />
              {t('grouping.filter')}
              {activeFilterCount > 0 && (
                <span className="rounded-full bg-background/25 px-1.5 text-[11px] tabular-nums">
                  {activeFilterCount}
                </span>
              )}
            </Button>
            {/* 显示：排序 / 排序方向 / 分组维度 / 每页 / 列 / 折叠——「这份数据怎么排布」。 */}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" size="sm">
                  <Settings2 className="size-4" /> {t('grouping.display')}
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-56">
                <DropdownMenuLabel>{t('grouping.sortBy')}</DropdownMenuLabel>
                <DropdownMenuRadioGroup value={sortKey} onValueChange={(v) => setSortKeyParam(v as InstanceSortKey)}>
                  {(['createdAt', 'name', 'status', 'nodeId'] as const).map((key) => (
                    <DropdownMenuRadioItem
                      key={key}
                      value={key}
                      // 就地单选：保持菜单打开，避免「改一项就要重开一次」。
                      onSelect={(event) => event.preventDefault()}
                    >
                      {t(`grouping.sort_${key}`)}
                    </DropdownMenuRadioItem>
                  ))}
                </DropdownMenuRadioGroup>
                <DropdownMenuSeparator />
                <DropdownMenuLabel>{t('grouping.sortOrder')}</DropdownMenuLabel>
                <DropdownMenuRadioGroup value={sortOrder} onValueChange={(v) => setSortOrderParam(v as InstanceSortOrder)}>
                  <DropdownMenuRadioItem value="asc" onSelect={(event) => event.preventDefault()}>
                    {t('grouping.orderAsc')}
                  </DropdownMenuRadioItem>
                  <DropdownMenuRadioItem value="desc" onSelect={(event) => event.preventDefault()}>
                    {t('grouping.orderDesc')}
                  </DropdownMenuRadioItem>
                </DropdownMenuRadioGroup>
                <DropdownMenuSeparator />
                <DropdownMenuLabel>{t('grouping.groupBy')}</DropdownMenuLabel>
                <DropdownMenuRadioGroup value={groupBy} onValueChange={(v) => setGroupByParam(v as GroupDimension)}>
                  {GROUP_DIMENSIONS.map((dim) => (
                    <DropdownMenuRadioItem key={dim} value={dim} onSelect={(event) => event.preventDefault()}>
                      {t(`grouping.dim_${dim}`)}
                    </DropdownMenuRadioItem>
                  ))}
                </DropdownMenuRadioGroup>
                <DropdownMenuSeparator />
                <DropdownMenuLabel>{t('grouping.pageSize')}</DropdownMenuLabel>
                <DropdownMenuRadioGroup value={String(pageSize)} onValueChange={setPageSizeParam}>
                  {INSTANCE_PAGE_SIZES.map((size) => (
                    <DropdownMenuRadioItem
                      key={size}
                      value={String(size)}
                      onSelect={(event) => event.preventDefault()}
                    >
                      {t('grouping.pageSizeValue', { count: size })}
                    </DropdownMenuRadioItem>
                  ))}
                </DropdownMenuRadioGroup>
                {/* 列设置只对列表视图有意义：卡片没有「列」这一维度，在卡片视图下显示
                    这个入口等于给一个点了没反应（或只影响切回列表后的样子）的控件。 */}
                {view === 'list' && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuLabel>{t('instances.columnsLabel')}</DropdownMenuLabel>
                    {INSTANCE_OPTIONAL_COLUMNS.map((column) => (
                      <DropdownMenuCheckboxItem
                        key={column}
                        checked={visibleColumns.has(column)}
                        onCheckedChange={() => toggleColumn(column)}
                        onSelect={(event) => event.preventDefault()}
                      >
                        {t(COLUMN_LABEL_KEY[column])}
                      </DropdownMenuCheckboxItem>
                    ))}
                  </>
                )}
                {/* 折叠控制只在有分组头时才有意义（平铺视图无处可折叠）。 */}
                {groupBy !== 'none' && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem onSelect={() => setAllCollapsed(true, allCollapsibleGroupKeys)}>
                      {t('grouping.collapseAll')}
                    </DropdownMenuItem>
                    <DropdownMenuItem
                      disabled={collapsedGroups.size === 0}
                      onSelect={() => setAllCollapsed(false, [])}
                    >
                      {t('grouping.expandAll')}
                    </DropdownMenuItem>
                    {groupBy === 'groupTree' && (
                      <DropdownMenuItem onSelect={() => setGroupManagerOpen((v) => !v)}>
                        <FolderTree className="size-4" /> {t('instanceGroups.manage')}
                      </DropdownMenuItem>
                    )}
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
            {/* 视图：只放保存的快照（整串 search + 列集合），与「显示」菜单分工明确。 */}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" size="sm">
                  <Bookmark className="size-4" /> {t('instances.viewsLabel')}
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-56">
                <DropdownMenuLabel>{t('instances.viewsSaved')}</DropdownMenuLabel>
                {savedViews.length === 0 ? (
                  <DropdownMenuItem disabled>{t('instances.viewsEmpty')}</DropdownMenuItem>
                ) : (
                  savedViews.map((view) => (
                    <DropdownMenuItem key={view.name} onSelect={() => applySavedView(view)}>
                      <span className="min-w-0 flex-1 truncate">{view.name}</span>
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon-xs"
                        aria-label={t('instances.viewDelete', { name: view.name })}
                        title={t('instances.viewDelete', { name: view.name })}
                        // 菜单项内的次级动作：按下不得触发 Radix 的项选择，故阻止冒泡链。
                        onPointerDown={(event) => event.stopPropagation()}
                        onClick={(event) => {
                          event.stopPropagation()
                          deleteSavedView(view.name)
                        }}
                        className="text-muted-foreground/70 hover:text-destructive"
                      >
                        <Trash2 className="size-3.5" />
                      </Button>
                    </DropdownMenuItem>
                  ))
                )}
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  onSelect={() => {
                    setViewNameDraft('')
                    setSaveViewOpen(true)
                  }}
                >
                  <Plus className="size-4" /> {t('instances.saveView')}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </div>
        {!filtersCollapsed && (
          <div id="instances-advanced-filters" className="mt-2 flex flex-wrap items-center gap-2 border-t pt-2">
            <FilterSelect
              label={t('grouping.filterNetwork')}
              value={networkId}
              onChange={setNetworkFilter}
              options={(networks ?? []).map((n) => ({ value: String(n.id), label: n.name }))}
            />
            {/* 已知局限（本次不修，需后端补 facet）：环境 / 标签候选由**已加载分页**推导
                （见 `envOptions` / `tagOptions`），因此
                ① 尚未加载到的环境 / 标签在此根本选不到——在「已加载 200 / 共 1200」时
                   用户可能找不到某个确实存在的标签；
                ② 滚动续取后候选会突然变多，同一个下拉的内容随加载进度变化。
                根治要后端在 /instances/search（或 aggregate）上给出全量 facet（distinct 值 + 计数），
                前端不擅自改接口契约，故此处只标注局限。 */}
            <FilterSelect
              label={t('grouping.filterEnv')}
              value={env}
              onChange={setEnvFilter}
              options={envOptions.map((e) => ({ value: e, label: t(`grouping.env_${e}`, { defaultValue: e }) }))}
            />
            <FilterSelect
              label={t('grouping.filterTag')}
              value={tag}
              onChange={setTagFilter}
              options={tagOptions.map((tg) => ({ value: tg, label: tg }))}
            />
            <FilterSelect
              label={t('grouping.filterNode')}
              value={nodeId}
              onChange={setNodeFilter}
              options={(nodes ?? []).map((n) => ({ value: String(n.id), label: n.name }))}
            />
            <FilterSelect
              label={t('grouping.filterStatus')}
              value={statusFilter}
              onChange={setStatusFilterParam}
              options={Object.entries(statusConfig).map(([k, v]) => ({ value: k, label: v.text }))}
            />
            {hasActiveFilter && (
              <Button variant="ghost" size="sm" onClick={resetFilters} className="text-muted-foreground">
                {t('grouping.clearFilters')}
              </Button>
            )}
          </div>
        )}
      </div>

      <ProvisionServerDialog open={showProvision} onClose={() => setShowProvision(false)} />
      <ProvisionProxyDialog open={showProvisionProxy} onClose={() => setShowProvisionProxy(false)} />
      <ImportServerWizard open={showImport} onClose={() => setShowImport(false)} />
      {manageProxy && (
        <ProxyRegistrationsDialog proxyId={manageProxy.id} proxyName={manageProxy.name} onClose={() => setManageProxy(null)} />
      )}
      {cloneTarget && (
        <CloneInstanceDialog sourceId={cloneTarget.id} sourceName={cloneTarget.name} onClose={() => setCloneTarget(null)} />
      )}
      {editConfigTarget && (
        <EditInstanceConfigDialog
          instanceId={editConfigTarget.id}
          instanceName={editConfigTarget.name}
          nodeId={editConfigTarget.nodeId}
          jdkId={editConfigTarget.jdkId ?? 0}
          startCommand={editConfigTarget.startCommand}
          autoRestart={editConfigTarget.autoRestart}
          onClose={() => setEditConfigTarget(null)}
        />
      )}
      {tagsTarget && (
        <InstanceTagsDialog
          instanceId={tagsTarget.id}
          instanceName={tagsTarget.name}
          tags={tagsTarget.tags}
          onClose={() => setTagsTarget(null)}
        />
      )}
      {limitsTarget && (
        <EditInstanceLimitsDialog
          instanceId={limitsTarget.id}
          instanceName={limitsTarget.name}
          processType={limitsTarget.processType}
          cpuLimit={limitsTarget.cpuLimit}
          memLimitMb={limitsTarget.memLimitMb}
          diskLimitMb={limitsTarget.diskLimitMb}
          onClose={() => setLimitsTarget(null)}
        />
      )}

      {/*阶段 6 补丁：数据未到时不再只给一行「加载中」。页头与（吸附的）筛选条先渲染，
          数据区用同壳骨架顶上：上面一条计数行占位、下面一张列表外壳 + 行占位，
          与就绪态「计数行 + 列表」两段结构一致，数据到达原地替换而不整页跳。 */}
      {isLoading && instances.length === 0 ? (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-2 px-1">
            <Skeleton className="h-4 w-40" />
            <Skeleton className="h-8 w-24" />
          </div>
          <DataPanelSkeleton rows={12} />
        </div>
      ) : (
        /* 
          数据区用 flex 列：让内部的虚拟表靠 `flex-1` 吃掉剩余高度（表格容器已改成 flex-1）。
          此前这里是 `div.space-y-3`——纯块级、不分配高度，表格只能用
          `max-h-[calc(100vh-20rem)]` 猜一个定值，页面一变高就算错、把列表顶出视口。
          `min-h-0` 是 flex 列里子项可收缩的前提，缺了它表格仍会被内容撑高、溢出照旧。
        */
        <div className="flex min-h-0 flex-1 flex-col gap-3">
          {/*
            批量栏只在列表视图出现：卡片视图的卡片**没有勾选入口**（卡片组件不接选择相关 props），
            所以从列表选中若干实例再切到卡片时，会出现一个「选着东西但无法取消」的批量栏，
            且用户没法在卡片里继续加选——这是无效 UI，也会让人以为卡片也能批量操作。
          */}
          {selectedIds.length > 0 && (
            <InstanceBatchBar
              selected={selectedInstances}
              onClear={clearSelection}
              onRetainFailed={setSelectedIds}
              onInvert={invertAll}
            />
          )}
          {view === 'card' ? (
            /* 
              卡片视图同样需要分页条：数据是服务端分页（每页 50 条），而卡视图走的是
              虚拟分组渲染路径，此前既没有分页条、onNeedMore 也是空实现——
              第 2 页及之后的实例在卡片视图里完全够不着（实测 1200+ 实例只看到 28 张）。
              这里用 flex 列把分页条固定在底部，卡区吃掉剩余高度。
            */
            <div className="flex min-h-0 flex-1 flex-col gap-2">
            <VirtualizedGroupedCardView
              // 卡片视图与表格视图共用同一行模型（buildInstanceTreeRows）：折叠、大区/小区两级、
              // 多级分组树的展开/收起行为由构造一致，不再各写一套分段逻辑。
              rows={treeRows}
              // 分页后「总数」= 当前页行数：虚拟滚动的高度模型必须与实际行数一致，
              // 否则会按 1200 条算出行盒、而只有 50 条内容，表现为大片空白或整片骨架屏。
              totalCount={treeRows.length}
              loadedCount={instances.length}
              onNeedMore={() => {}}
              onToggleCollapse={toggleGroupCollapsed}
              scrollStorageKey={scrollStorageKey}
              nodeName={nodeName}
              buildMenu={buildMenu}
              onOpenInstance={openInstance}
              emptyLabel={hasActiveFilter ? t('grouping.noMatch') : t('instances.empty')}
              renderCard={(args) => (
                <InstanceWorktableCard
                  {...args}
                  /* 
                    卡片视图的启停/右键：与列表视图走完全相同的页面级流程。
                    此前卡片自己 useStartInstance().mutate()，绕过了二次确认——
                    同一台实例在列表里会确认、在卡片里一点就停，安全语义不一致。
                  */
                  onStart={() => setLifecycleTarget({ id: args.inst.id, name: args.inst.name, op: 'start' })}
                  onStop={() => setLifecycleTarget({ id: args.inst.id, name: args.inst.name, op: 'stop' })}
                  onRestart={() => setLifecycleTarget({ id: args.inst.id, name: args.inst.name, op: 'restart' })}
                  onContextMenu={(card, x, y) => setContextMenu({ inst: card, x, y })}
                  selected={selectedIdSet.has(args.inst.id)}
                  onToggleSelect={(modifiers) => toggleOneWithModifiers(args.inst.id, modifiers)}
                  /* 身份徽章开关跟随列设置：卡片与表格展示同一组信息，用户关掉的列在卡片里也不显示。 */
                  showType={visibleColumns.has('type')}
                  showRole={visibleColumns.has('role')}
                  showTags={visibleColumns.has('tags')}
                />
              )}
            />
            <InstancePaginationBar
              loaded={instances.length}
              total={totalCount}
              page={page}
              pageSize={pageSize}
              isFetching={isFetching}
              onPageChange={(next) => updateUrl({ page: String(next) })}
            />
            </div>
          ) : groupBy === 'none' ? (
            <PaginatedInstanceTable
              instances={instances}
              total={totalCount}
              page={page}
              pageSize={pageSize}
              isFetching={isFetching}
              onPageChange={(next) => updateUrl({ page: String(next) })}
                      visibleColumns={visibleColumns}
              columnOrder={columnOrder}
              header={
                <InstanceTableHeader
                  t={t}
                  allSelected={allSelected}
                  onToggleAll={toggleAll}
                  sortKey={sortKey}
                  sortOrder={sortOrder}
                  onSort={setTableSortParam}
                  selectAllLabel={selectAllLabel}
                  visibleColumns={visibleColumns}
                  columnOrder={columnOrder}
                  onReorderColumn={reorderColumn}
                />
              }
              renderRow={renderRow}
              emptyLabel={hasActiveFilter ? t('grouping.noMatch') : t('instances.empty')}
            />
          ) : (
            <PaginatedInstanceTable
              instances={instances}
              total={totalCount}
              page={page}
              pageSize={pageSize}
              isFetching={isFetching}
              onPageChange={(next) => updateUrl({ page: String(next) })}
                      visibleColumns={visibleColumns}
              treeRows={treeRows}
              renderGroupRow={(row) => (
                <TableRow
                  key={row.key}
                  data-testid="instances-group-row"
                  data-group-depth={row.depth}
                  data-collapsed={row.collapsed ? 'true' : 'false'}
                  className="border-b bg-muted/40"
                >
                  <TableCell colSpan={columnCount} className="px-4 py-1.5">
                    <InstanceGroupHeaderContent
                      label={row.label}
                      depth={row.depth}
                      count={row.count}
                      health={row.health}
                      collapsed={row.collapsed}
                      onToggle={() => toggleGroupCollapsed(row.collapseKey)}
                    />
                  </TableCell>
                </TableRow>
              )}
              header={
                <InstanceTableHeader
                  t={t}
                  allSelected={allSelected}
                  onToggleAll={toggleAll}
                  sortKey={sortKey}
                  sortOrder={sortOrder}
                  onSort={setTableSortParam}
                  selectAllLabel={selectAllLabel}
                  visibleColumns={visibleColumns}
                />
              }
              renderRow={renderRow}
              emptyLabel={hasActiveFilter ? t('grouping.noMatch') : t('instances.empty')}
            />
          )}
          {/* 组织分组管理：收敛到 groupTree 维度下的可选面板，保留建组/移入能力。 */}
          {groupBy === 'groupTree' && groupManagerOpen && <InstanceGroupManager />}
          {/* 健康度悬浮面板：**全表单例**（全表只渲染这一个，与行数无关）。 */}
          <InstanceHealthHoverLayer />
          {/*
            实例行右键菜单：同样**全表单例**。动作集合由 buildContextMenuActions 生成，
            与「⋯」下拉菜单共用同一批 mutation / 弹窗目标——两处入口不会各写一套判据。
          */}
          {contextMenu && (
            <InstanceContextMenu
              inst={contextMenu.inst}
              position={{ x: contextMenu.x, y: contextMenu.y }}
              actions={buildContextMenuActions({
                inst: contextMenu.inst,
                t: (key, fallback, opts) => t(key, { defaultValue: fallback, ...opts }),
                onStart: () => setLifecycleTarget({ id: contextMenu.inst.id, name: contextMenu.inst.name, op: 'start' }),
                onStop: () => setLifecycleTarget({ id: contextMenu.inst.id, name: contextMenu.inst.name, op: 'stop' }),
                onRestart: () => setLifecycleTarget({ id: contextMenu.inst.id, name: contextMenu.inst.name, op: 'restart' }),
                onEditConfig: () => setEditConfigTarget(contextMenu.inst),
                onTags: () =>
                  setTagsTarget({
                    id: contextMenu.inst.id,
                    name: contextMenu.inst.name,
                    tags: parseTags(contextMenu.inst.tags),
                  }),
                onLimits: () =>
                  setLimitsTarget({
                    id: contextMenu.inst.id,
                    name: contextMenu.inst.name,
                    processType: contextMenu.inst.processType,
                    cpuLimit: contextMenu.inst.cpuLimit ?? 0,
                    memLimitMb: contextMenu.inst.memLimitMb ?? 0,
                    diskLimitMb: contextMenu.inst.diskLimitMb ?? 0,
                  }),
                onProxy: () => setManageProxy({ id: contextMenu.inst.id, name: contextMenu.inst.name }),
                onClone: () => setCloneTarget({ id: contextMenu.inst.id, name: contextMenu.inst.name }),
                onDelete: () =>
                  setDeleteTarget({
                    id: contextMenu.inst.id,
                    name: contextMenu.inst.name,
                    inPlace: !!contextMenu.inst.workDirInPlace,
                    running: contextMenu.inst.status !== 'STOPPED' && contextMenu.inst.status !== 'CRASHED',
                  }),
              })}
              onClose={() => setContextMenu(null)}
            />
          )}
        </div>
      )}

      {/* 保存视图：命名后落 jm.instances.views（同名覆盖）。 */}
      <Dialog open={saveViewOpen} onOpenChange={setSaveViewOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>{t('instances.saveViewTitle')}</DialogTitle>
          </DialogHeader>
          <Input
            value={viewNameDraft}
            onChange={(event) => setViewNameDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') saveCurrentView()
            }}
            placeholder={t('instances.viewNamePlaceholder')}
            aria-label={t('instances.viewNamePlaceholder')}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setSaveViewOpen(false)}>
              {t('common.cancel', { defaultValue: '取消' })}
            </Button>
            <Button disabled={!viewNameDraft.trim()} onClick={saveCurrentView}>
              {t('common.save', { defaultValue: '保存' })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <DangerConfirm
        open={deleteTarget !== null}
        title={t('danger.deleteInstanceTitle', { name: deleteTarget?.name ?? '' })}
        description={[
          // 运行中删除先停止再删除，确认时明示进程处置方式。
          deleteTarget?.running ? t('danger.deleteInstanceStopFirst') : null,
          deleteTarget?.inPlace ? t('importServer.deleteInPlaceDesc') : t('danger.deleteInstanceDesc'),
        ].filter(Boolean).join(' ')}
        confirmLabel={t('common.delete')}
        confirmText={deleteTarget?.name}
        scope="group"
        onConfirm={() => { if (deleteTarget) del.mutate(deleteTarget.id); setDeleteTarget(null) }}
        onCancel={() => setDeleteTarget(null)}
      />

      {/*
        启停二次确认：三个动作各自给一句「会发生什么」的说明。
        - 停止会把在线玩家直接踢下线，不可撤销地打断一局游戏；
        - 重启同理但会自己回来；
        - 启动本身无破坏性，但仍确认一次（与另两个保持同一交互，避免「有的确认有的不确认」）。
        scope 不传：启停只影响单台实例，与 node.operate 那类平台级权限无关。
      */}
      <DangerConfirm
        open={lifecycleTarget !== null}
        title={t(
          lifecycleTarget?.op === 'stop'
            ? 'danger.stopInstanceTitle'
            : lifecycleTarget?.op === 'restart'
              ? 'danger.restartInstanceTitle'
              : 'danger.startInstanceTitle',
          { name: lifecycleTarget?.name ?? '' },
        )}
        description={t(
          lifecycleTarget?.op === 'stop'
            ? 'danger.stopInstanceDesc'
            : lifecycleTarget?.op === 'restart'
              ? 'danger.restartInstanceDesc'
              : 'danger.startInstanceDesc',
        )}
        confirmLabel={t(
          lifecycleTarget?.op === 'stop'
            ? 'instances.stop'
            : lifecycleTarget?.op === 'restart'
              ? 'instances.restart'
              : 'instances.start',
        )}
        pending={
          (lifecycleTarget?.op === 'start' && start.isPending) ||
          (lifecycleTarget?.op === 'stop' && stop.isPending) ||
          (lifecycleTarget?.op === 'restart' && restart.isPending)
        }
        onConfirm={() => {
          if (!lifecycleTarget) return
          const { id, op } = lifecycleTarget
          if (op === 'start') start.mutate(id)
          else if (op === 'stop') stop.mutate(id)
          else restart.mutate(id)
          setLifecycleTarget(null)
        }}
        onCancel={() => setLifecycleTarget(null)}
      />

      <DangerConfirm
        open={killTarget !== null}
        title={t('danger.killInstanceTitle', { name: killTarget?.name ?? '' })}
        description={t('danger.killInstanceDesc')}
        confirmLabel={t('instances.kill')}
        scope="group"
        onConfirm={() => { if (killTarget) kill.mutate(killTarget.id); setKillTarget(null) }}
        onCancel={() => setKillTarget(null)}
      />

      {/* 接管运行态二次确认：会给该服造成一次真实重启，故与强杀同款二次确认。 */}
      <DangerConfirm
        open={adoptTarget !== null}
        title={t('serverConsole.runtimeDriftAdoptTitle', { name: adoptTarget?.name ?? '' })}
        description={t('serverConsole.runtimeDriftAdoptDesc', { pid: adoptTarget?.pid ?? 0 })}
        confirmLabel={t('serverConsole.runtimeDriftAdopt')}
        scope="group"
        onConfirm={() => { if (adoptTarget) adopt.mutate(adoptTarget.id); setAdoptTarget(null) }}
        onCancel={() => setAdoptTarget(null)}
      />
    </PageShell>
  )
}


