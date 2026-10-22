// 列表行展示层已回迁应用侧（原 ADR-102 迁包已撤销）：平铺表与分组树表共用的行渲染件在包内；
// 本层保留取数（无限搜索 / 聚合 / 节点 / 群组 / 分组树）、URL 状态、批量选择与各弹窗目标的装配。
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { Zap, Globe, Plus, FolderTree, HardDriveDownload, Search, SlidersHorizontal, Columns3, Bookmark, Trash2, ServerCog, CircleCheck, CircleSlash, TriangleAlert } from 'lucide-react'
import { useInfiniteInstanceSearch, useInstanceAggregate, useStartInstance, useStopInstance, useRestartInstance, useDeleteInstance, useKillInstance, useAdoptInstanceRuntime, isProvisioningInstance, type InstanceInfo } from '@/api/instances'
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
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@jianmanager/ui/components/dialog'
import { Button } from '@jianmanager/ui/components/button'
import { Card } from '@jianmanager/ui/components/card'
import { Input } from '@jianmanager/ui/components/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { SCROLL_KEY_PREFIX, buildInstanceTreeRows, VirtualizedGroupedInstanceTable, VirtualizedInstanceTable } from '@/components/views/instances/VirtualizedInstanceTables'
import { FilterSelect, InstanceRowMenu, InstanceTableHeader } from '@/components/views/instances/InstanceTableParts'
import { useInstanceWindowMetrics } from '@/components/views/instances/useInstanceWindowMetrics'
import type { InstanceSortKey, InstanceSortOrder } from '@/components/views/instances/InstanceTableParts'
import { InstanceRowView } from '@/components/views/instances/InstanceRowView'
import { BackendsInline, VirtualizedGroupedCardView } from '@/components/views/instances/InstanceCardViews'

// 供针对性测试（rowMenu 门控）沿用既有导入路径。
export { InstanceRowMenu }

/** Radix Select 不允许空字符串 value，用哨兵值表示「全部 / 不过滤」。 */
/** 代理行内联后端摘要的取数接线层（ADR-102）：视图已入包，此处只做取数与跳转注入。 */
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
  /** 折叠的分组键（FR-452），逗号分隔、逐键 encodeURIComponent。 */
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
 * 视图模式：FR-452 起 `list`（分组树表）为 `/instances` 默认——64 台跨区可一屏折叠管理。
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
  heartbeat: 'instances.metrics.heartbeatColumn',
}

/** 默认分组维度：region/zone 两级（FR-452）。 */
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

/** 读取折叠分组键集合（FR-452）：URL `?collapsed=` 逗号分隔、逐键 decode。 */
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
  return INSTANCE_PAGE_SIZES.includes(value as (typeof INSTANCE_PAGE_SIZES)[number]) ? value : 200
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
  // 资源限额编辑目标（FR-079）：携带启动方式与当前限额回填。
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
  // 接管运行态漂移（FR-471）：先把目标置入待确认，DangerConfirm 确认后才发请求（与强杀同款纪律）。
  const [adoptTarget, setAdoptTarget] = useState<{ id: number; name: string; pid: number } | null>(null)
  // 批量操作选中的实例 ID 集合（FR-058）。
  const [selectedIds, setSelectedIds] = useState<number[]>([])
  // 工作台卡 ⇄ 列表视图（FR-136，§4.5）；运行实体默认卡片。
  const [view, setView] = useState<ViewMode>(() => readViewMode(searchParams))
  // 分组树表：折叠态入 URL（FR-452，`?collapsed=`），切换维度即时重排（客户端聚合）。
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(() => readCollapsed(searchParams))
  // 「管理分组」面板开关（FR-165 的组织分组能力在 groupTree 维度内可达，不再单开 orgView 视图）。
  const [groupManagerOpen, setGroupManagerOpen] = useState(false)
  // proxy 行 inline 展开已注册 backend 的代理 id 集合（FR-136）。
  const [expandedProxies, setExpandedProxies] = useState<Set<number>>(new Set())

  // 多维筛选状态（FR-047 / FR-268）：群组 / 环境 / 标签 / 节点 / 状态任意组合，下发后端过滤。
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
  const [page, setPage] = useState(() => readPage(searchParams))
  const [pageSize, setPageSize] = useState(() => readPageSize(searchParams))
  const [filtersCollapsed, setFiltersCollapsed] = useState(false)
  // 列设置（页面重构件）：可见可选列持久化于 jm.instances.columns；缺省全开（旧行为不变）。
  const [visibleColumns, setVisibleColumns] = useState<Set<InstanceOptionalColumn>>(readStoredColumns)
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
  const {
    data: searchData,
    isLoading,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
  } = useInfiniteInstanceSearch(instanceSearchParams, page)
  const instances = useMemo(() => searchData?.pages.flatMap((p) => p.items) ?? [], [searchData])
  const totalCount = searchData?.pages[0]?.total ?? instances.length
  const aggregateParams = useMemo(() => {
    const next: InstanceListParams = { ...params }
    delete next.status
    return next
  }, [params])
  const { data: aggregate } = useInstanceAggregate(aggregateParams)
  const { data: nodes } = useNodes()
  const { data: networks } = useNetworks()
  // FR-452 `network` / `groupTree` 维度取数：均为一次请求、无 N+1；按维度分别按需预取——
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

  // 批量选择（FR-058）：select-all 作用于当前筛选后的可见集合。
  //
  // 已选集合改 `Set`：行渲染按 `selectedIds.includes(id)` 判定，`includes` 是 O(已选数)——
  // 全选 1200 台后「可见行 × 1200」的线性扫描会成为渲染热点（对照 NetworksPageView 同款处理）。
  const selectedIdSet = useMemo(() => new Set(selectedIds), [selectedIds])
  const toggleOne = (id: number) =>
    setSelectedIds((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]))
  const allIds = useMemo(() => instances?.map((i) => i.id) ?? [], [instances])
  const allSelected = allIds.length > 0 && allIds.every((id) => selectedIdSet.has(id))
  const toggleAll = () => setSelectedIds(allSelected ? [] : allIds)
  const clearSelection = () => setSelectedIds([])
  // 选中实例的 {id,name,status}，供批量栏做状态感知禁用与部分失败明细（FR-139）。
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

  // 「实例→分组键」映射（FR-452）：仅 network 维度需要外部映射（键=群组名）；
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

  // 分组聚合（FR-452）：groupTree 走多级层级聚合（父组头 → 子组头 → 成员）；其余维度单级/两级。
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

  const loadMoreInstances = useCallback(() => {
    if (hasNextPage && !isFetchingNextPage) {
      void fetchNextPage()
    }
  }, [fetchNextPage, hasNextPage, isFetchingNextPage])

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
    const next = INSTANCE_PAGE_SIZES.includes(parsed as (typeof INSTANCE_PAGE_SIZES)[number]) ? parsed : 200
    setPageSize(next)
    updateUrl({ pageSize: String(next), page: '1' })
  }
  /** 切换某分组折叠态并写回 URL（FR-452，`?collapsed=`）。 */
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
   * 按分组维度给出分组标题（FR-452 各维度统一落此处）；空 key 显示该维度的「未分…」文案。
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

  // 树表行模型（FR-452）：分组头 + 成员行统一行模型，供单个虚拟表渲染。
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
    // 运行态漂移（FR-471）：无漂移为 undefined，据此决定是否给出「接管」菜单项。
    const drift = runtimeDriftOf(inst)
    return (
      <InstanceRowMenu
        inst={inst}
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
        // 接管运行态漂移（FR-471）：仅当目录下确有未纳管活进程时给出入口。
        onAdoptRuntime={
          drift ? () => setAdoptTarget({ id: inst.id, name: inst.name, pid: drift.pid }) : undefined
        }
        onDelete={() => setDeleteTarget({
          id: inst.id,
          name: inst.name,
          inPlace: !!inst.workDirInPlace,
          // FR-310：非停止态删除由后端编排「先停止（超时强杀）再删除」，确认框据此提示。
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
  const [visibleRange, setVisibleRange] = useState<{ start: number; end: number } | null>(null)
  const handleVisibleRange = useCallback((start: number, end: number) => {
    setVisibleRange((prev) => (prev && prev.start === start && prev.end === end ? prev : { start, end }))
  }, [])

  const visibleInstances = useMemo(() => {
    if (!visibleRange) return []
    const { start, end } = visibleRange
    if (groupBy === 'none') return instances.slice(start, end)
    return treeRows.slice(start, end).flatMap((row) => (row.kind === 'instance' ? [row.instance] : []))
  }, [groupBy, instances, treeRows, visibleRange])

  const windowMetrics = useInstanceWindowMetrics(visibleInstances)

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

  /** 当前表格总列数（固定 5 列 + 可见可选列）：占位/组头/展开行的 colSpan 必须同步。 */
  const columnCount = 10 - (INSTANCE_OPTIONAL_COLUMNS.length - visibleColumns.size)

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
   * 展开行判据（喂给虚拟高度模型）：与 `renderRow` 的渲染条件完全同源——
   * proxy 画像 + 该 id 在展开集合中。展开会在该实例项下追加一个 `INSTANCE_EXPANDED_ROW_HEIGHT`
   * 高的行，模型必须按同一判据计入，否则其后所有行的定位与滚动条长度都会漂移。
   */
  const isRowExpanded = useCallback(
    (inst: InstanceInfo) => isProxyInstance(inst) && expandedProxies.has(inst.id),
    [expandedProxies],
  )

  /**
   * 行装配（ADR-102）：行展示件已入包（平铺表与分组树表共用），此处只注入数据与回调。
   * 逐行判别留在应用侧：节点名（查节点表）、代理画像（FR-445）、搭建中、运行态漂移（FR-471）、
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
      columns={visibleColumns}
      columnCount={columnCount}
      proxyExpanded={expandedProxies.has(inst.id)}
      onToggleProxy={() => toggleProxy(inst.id)}
      renderBackends={() => <BackendsInlineContainer proxyId={inst.id} />}
      starting={start.isPending && start.variables === inst.id}
      stopping={stop.isPending && stop.variables === inst.id}
      restarting={restart.isPending && restart.variables === inst.id}
      menu={buildMenu(inst)}
      onOpenInstance={openInstance}
      onStart={() => start.mutate(inst.id)}
      onStop={() => stop.mutate(inst.id)}
      onRestart={() => restart.mutate(inst.id)}
      onKill={() => setKillTarget({ id: inst.id, name: inst.name })}
    />
  )

  return (
    // 阶段 6 页面迁移：外壳与页头改用布局层原语（PageShell / PageHeader）。
    // data-page 由 PageShell spread 透传，e2e 的就绪信号依赖它。
    /*
      variant="fixed"：本页的滚动发生在**表格内部**（虚拟表自带滚动容器 + 表头吸顶），
      不是整页滚动。`default` 变体给的是 `overflow-auto` 的整页滚动容器，它会让
      页内那个 `flex-1` 表格永远拿不到「视口剩余高度」（父级没有确定高度可分配），
      结果退回按内容撑高、又被外层滚动——两层滚动叠加，表格底部被推出视口。
      fixed 变体是 `overflow-hidden`，高度被钉在视口内，`flex-1` 才有意义。
    */
    <PageShell data-page="instances" variant="fixed">
      {/*
        【页头只留小标题 + 右侧按钮】此前标题下还有一行「42 台服务器 · 36 运行中 · 3 异常 ·
        2,431 在线玩家」的统计副标题——那些数字已经由下面的 KPI 卡片承担，两处重复表达同一信息，
        既占高度又分散注意力。现在标题行只负责「我在哪一页」这一件事。
      */}
      <PageHeader
        className="[&_[data-slot=page-header]]:gap-0"
        title={t('instances.title')}
        actions={
          // flex-wrap：四个入口按钮在移动端（390px）超行宽须换行——修 v0.15.0 验收 e2e
          // 抓出的移动端横向溢出 120px（FR-302 加第 4 按钮后顶爆无 wrap 的一行）。
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" onClick={() => setShowProvision(true)}>
              <Zap className="size-4" /> {t('provision.entry')}
            </Button>
            <Button variant="outline" onClick={() => setShowProvisionProxy(true)}>
              <Globe className="size-4" /> {t('proxy.entry')}
            </Button>
            <Button variant="outline" onClick={() => setShowImport(true)}>
              <HardDriveDownload className="size-4" /> {t('importServer.entry')}
            </Button>
            <Button onClick={() => navigate('/instances/new')}>
              <Plus className="size-4" /> {t('instances.createInstance')}
            </Button>
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

      {/* 多维筛选 + 分组视图（FR-047/137）：sticky + 可折叠，避免长筛选条在移动端翻屏。 */}
      {/*
        工具栏收成一张明确的卡片：此前它只是 `jm-toolbar-surface`（半透明白底），
        与页面底色差不到 1% 明度，视觉上是「飘着的一排控件」而不是「一块工具区」。
        改成带描边与内边距的实心面板后，搜索 / 筛选 / 视图切换才有归属。
      */}
      {/* 吸附工具栏：Card 承载统一表面（圆角/描边/底色/阴影）。
          原为块级容器且内部两段无间距，故 gap 置零；sticky 定位与毛玻璃按需保留。 */}
      <Card className="sticky top-16 z-20 p-2.5 backdrop-blur-sm">
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
          <Select value={sortKey} onValueChange={(v) => setSortKeyParam(v as InstanceSortKey)}>
            <SelectTrigger size="sm" className="w-36" aria-label={t('grouping.sortBy')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="createdAt">{t('grouping.sort_createdAt')}</SelectItem>
              <SelectItem value="name">{t('grouping.sort_name')}</SelectItem>
              <SelectItem value="status">{t('grouping.sort_status')}</SelectItem>
              <SelectItem value="nodeId">{t('grouping.sort_nodeId')}</SelectItem>
            </SelectContent>
          </Select>
          <Select value={sortOrder} onValueChange={(v) => setSortOrderParam(v as InstanceSortOrder)}>
            <SelectTrigger size="sm" className="w-28" aria-label={t('grouping.sortOrder')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="asc">{t('grouping.orderAsc')}</SelectItem>
              <SelectItem value="desc">{t('grouping.orderDesc')}</SelectItem>
            </SelectContent>
          </Select>
          <Select value={String(pageSize)} onValueChange={setPageSizeParam}>
            <SelectTrigger size="sm" className="w-28" aria-label={t('grouping.pageSize')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {INSTANCE_PAGE_SIZES.map((size) => (
                <SelectItem key={size} value={String(size)}>
                  {t('grouping.pageSizeValue', { count: size })}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            variant="outline"
            size="sm"
            onClick={() => setFiltersCollapsed((v) => !v)}
            aria-expanded={!filtersCollapsed}
            aria-controls="instances-advanced-filters"
          >
            <SlidersHorizontal className="size-4" />
            {filtersCollapsed ? t('grouping.showFilters') : t('grouping.hideFilters')}
          </Button>
          <div className="ml-auto flex items-center gap-2">
            {/* 视图切换：卡片 / 列表。与筛选条同一行——它是「怎么看这份数据」的一部分。 */}
            <ViewToggle
              value={view}
              onChange={setViewParam}
              cardLabel={t('grouping.viewCard')}
              listLabel={t('grouping.viewList')}
            />
            {/* 列设置（页面重构件）：切换可选列可见性，持久化于 jm.instances.columns。 */}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" size="sm">
                  <Columns3 className="size-4" /> {t('instances.columnsLabel')}
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-44">
                {INSTANCE_OPTIONAL_COLUMNS.map((column) => (
                  <DropdownMenuCheckboxItem
                    key={column}
                    checked={visibleColumns.has(column)}
                    onCheckedChange={() => toggleColumn(column)}
                    // 就地多选：保持菜单打开（连续勾选是常态）。
                    onSelect={(event) => event.preventDefault()}
                  >
                    {t(COLUMN_LABEL_KEY[column])}
                  </DropdownMenuCheckboxItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
            {/* 保存视图（页面重构件）：整串 search 快照 + 列集合；恢复由 URL 同步 effect 落位。 */}
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
            {hasActiveFilter && (
              <Button variant="ghost" size="sm" onClick={resetFilters} className="text-muted-foreground">
                {t('grouping.clearFilters')}
              </Button>
            )}
            {/* FR-452：分组树表提供全部折叠/展开（长列表一屏可管）。 */}
            {groupBy !== 'none' && view === 'list' && (
              <>
                <Button
                  variant="outline"
                  size="sm"
                  data-testid="instances-collapse-all"
                  onClick={() => setAllCollapsed(true, allCollapsibleGroupKeys)}
                >
                  {t('grouping.collapseAll')}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  data-testid="instances-expand-all"
                  disabled={collapsedGroups.size === 0}
                  onClick={() => setAllCollapsed(false, [])}
                >
                  {t('grouping.expandAll')}
                </Button>
              </>
            )}
            {/* 组织分组视图（FR-165）收敛为 groupTree 维度；分组管理入口随之挂在该维度下。 */}
            {groupBy === 'groupTree' && (
              <Button
                variant={groupManagerOpen ? 'default' : 'outline'}
                size="sm"
                onClick={() => setGroupManagerOpen((v) => !v)}
                aria-pressed={groupManagerOpen}
              >
                <FolderTree className="size-4" /> {t('instanceGroups.manage')}
              </Button>
            )}
          </div>
        </div>
        {!filtersCollapsed && (
          <div id="instances-advanced-filters" className="flex flex-wrap items-center gap-2">
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
            {(
              <div className="flex items-center gap-2">
                <span className="text-sm text-muted-foreground">{t('grouping.groupBy')}</span>
                <Select value={groupBy} onValueChange={(v) => setGroupByParam(v as GroupDimension)}>
                  <SelectTrigger size="sm" className="w-36" aria-label={t('grouping.groupBy')}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {GROUP_DIMENSIONS.map((dim) => (
                      <SelectItem key={dim} value={dim}>
                        {t(`grouping.dim_${dim}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            )}
          </div>
        )}
      </Card>

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

      {/* FR-502 阶段 6 补丁：数据未到时不再只给一行「加载中」。页头与（吸附的）筛选条先渲染，
          数据区用同壳骨架顶上：上面一条计数行占位、下面一张列表外壳 + 行占位，
          与就绪态「计数行 + 列表」两段结构一致，数据到达原地替换而不整页跳。 */}
      {isLoading ? (
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
          {selectedIds.length > 0 && (
            <InstanceBatchBar selected={selectedInstances} onClear={clearSelection} onRetainFailed={setSelectedIds} />
          )}
          {/* 加载可供性（FR-235）：已加载/总数计数 + 显式「加载更多」兜底（不依赖滚动触发）。
              分组视图下 total 为服务端总数，各组仅反映「当前已加载页」，组头计数即已加载子集。 */}
          <div className="flex flex-wrap items-center justify-between gap-2 px-1 text-sm text-muted-foreground">
            <span data-testid="instances-loaded-count">
              {t('instances.loadedOfTotal', {
                defaultValue: 'Loaded {{loaded}} / {{total}}',
                loaded: instances.length,
                total: totalCount,
              })}
              {groupBy !== 'none' && (
                <span className="ml-2 text-xs">
                  {t('instances.groupsLoadedOnly', { defaultValue: '(groups show loaded pages only)' })}
                </span>
              )}
            </span>
            {instances.length < totalCount && (
              <Button
                variant="outline"
                size="sm"
                data-testid="instances-load-more"
                disabled={isFetchingNextPage}
                onClick={loadMoreInstances}
              >
                {isFetchingNextPage
                  ? t('instances.loadingMore', { defaultValue: 'Loading…' })
                  : t('instances.loadMore', { defaultValue: 'Load more' })}
              </Button>
            )}
          </div>
          {view === 'card' ? (
            <VirtualizedGroupedCardView
              // 卡片视图与表格视图共用同一行模型（buildInstanceTreeRows）：折叠、大区/小区两级、
              // 多级分组树的展开/收起行为由构造一致，不再各写一套分段逻辑。
              rows={treeRows}
              totalCount={totalCount}
              loadedCount={instances.length}
              onNeedMore={loadMoreInstances}
              onToggleCollapse={toggleGroupCollapsed}
              scrollStorageKey={scrollStorageKey}
              nodeName={nodeName}
              buildMenu={buildMenu}
              onOpenInstance={openInstance}
              emptyLabel={hasActiveFilter ? t('grouping.noMatch') : t('instances.empty')}
              renderCard={(args) => <InstanceWorktableCard {...args} />}
            />
          ) : groupBy === 'none' ? (
            <VirtualizedInstanceTable
              instances={instances}
              totalCount={totalCount}
              onNeedMore={loadMoreInstances}
              isRowExpanded={isRowExpanded}
              onVisibleRangeChange={handleVisibleRange}
              columnCount={columnCount}
              scrollStorageKey={scrollStorageKey}
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
          ) : (
            <VirtualizedGroupedInstanceTable
              rows={treeRows}
              totalCount={totalCount}
              loadedCount={instances.length}
              onNeedMore={loadMoreInstances}
              onToggleCollapse={toggleGroupCollapsed}
              isRowExpanded={isRowExpanded}
              onVisibleRangeChange={handleVisibleRange}
              columnCount={columnCount}
              scrollStorageKey={scrollStorageKey}
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
          {/* 组织分组管理（FR-165）：收敛到 groupTree 维度下的可选面板，保留建组/移入能力。 */}
          {groupBy === 'groupTree' && groupManagerOpen && <InstanceGroupManager />}
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
          // FR-310：运行中删除先停止再删除，确认时明示进程处置方式。
          deleteTarget?.running ? t('danger.deleteInstanceStopFirst') : null,
          deleteTarget?.inPlace ? t('importServer.deleteInPlaceDesc') : t('danger.deleteInstanceDesc'),
        ].filter(Boolean).join(' ')}
        confirmLabel={t('common.delete')}
        confirmText={deleteTarget?.name}
        scope="group"
        onConfirm={() => { if (deleteTarget) del.mutate(deleteTarget.id); setDeleteTarget(null) }}
        onCancel={() => setDeleteTarget(null)}
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

      {/* 接管运行态二次确认（FR-471）：会给该服造成一次真实重启，故与强杀同款二次确认。 */}
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


