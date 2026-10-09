// 列表行展示层已回迁应用侧（原 ADR-097 迁包已撤销）：平铺表与分组树表共用的行渲染件在包内；
// 本层保留取数（无限搜索 / 聚合 / 节点 / 群组 / 分组树）、URL 状态、批量选择与各弹窗目标的装配。
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { Zap, Globe, Plus, FolderTree, HardDriveDownload, Search, SlidersHorizontal } from 'lucide-react'
import { useInfiniteInstanceSearch, useInstanceAggregate, useStartInstance, useStopInstance, useRestartInstance, useDeleteInstance, useKillInstance, useAdoptInstanceRuntime, isProvisioningInstance, type InstanceInfo } from '@/api/instances'
import type { InstanceListParams, InstanceSearchParams } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useNetworks } from '@/api/networks'
import { useTopology } from '@/api/topology'
import { useInstanceGroups } from '@/api/instanceGroups'
import { useRegistrations } from '@/api/registrations'
import { useConsoleStore } from '@/stores/console'
import DangerConfirm from '@/components/common/DangerConfirm'
import InstanceBatchBar from '@/components/instances/InstanceBatchBar'
import ProvisionServerDialog from '@/components/provision/ProvisionServerDialog'
import ProvisionProxyDialog from '@/components/provision/ProvisionProxyDialog'
import ImportServerWizard from '@/components/provision/ImportServerWizard'
import ProxyRegistrationsDialog from '@/components/instances/ProxyRegistrationsDialog'
import CloneInstanceDialog from '@/components/instances/CloneInstanceDialog'
import InstanceTagsDialog from '@/components/instances/InstanceTagsDialog'
import EditInstanceLimitsDialog from '@/components/instances/EditInstanceLimitsDialog'
import EditInstanceConfigDialog from '@/components/instances/EditInstanceConfigDialog'
import { resolveCapabilities } from '@/lib/capabilities'
import { InstanceWorktableCard } from '@/components/console/InstanceWorktableCard'
import { InstanceGroupManager } from '@/components/console/InstanceGroupManager'
import { buildGroupTreeSource, buildKeyMap, collectEnvs, collectTags, groupInstances, groupInstancesByGroupTree, parseTags, GROUP_DIMENSIONS } from '@/lib/instance-grouping'
import type { GroupDimension } from '@/lib/instance-grouping'
import { runtimeDriftOf } from '@/lib/runtime-drift'
import { summarizeInstances, summaryFilterStatus } from '@/lib/instance-summary'
import type { SummaryFilterKey } from '@/lib/instance-summary'
import { DataPanelSkeleton, PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { SummaryChips, type SummaryChip } from '@jianmanager/ui/components/summary-chips'
import { ViewToggle, type ViewMode } from '@jianmanager/ui/components/view-toggle'
import { Button } from '@jianmanager/ui/components/button'
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
import type { InstanceSortKey, InstanceSortOrder } from '@/components/views/instances/InstanceTableParts'
import { InstanceRowView } from '@/components/views/instances/InstanceRowView'
import { BackendsInline, CardView } from '@/components/views/instances/InstanceCardViews'

// 供针对性测试（rowMenu 门控）沿用既有导入路径。
export { InstanceRowMenu }

/** Radix Select 不允许空字符串 value，用哨兵值表示「全部 / 不过滤」。 */
/** 代理行内联后端摘要的取数接线层（ADR-097）：视图已入包，此处只做取数与跳转注入。 */
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
 * 视图模式：FR-452 起 `list`（分组树表）为 `/instances` 默认——64 台跨区可一屏折叠管理。
 * 卡片视图保留为紧凑模式（`?view=card`）。
 */
function readViewMode(searchParams: URLSearchParams): ViewMode {
  return searchParams.get('view') === 'card' ? 'card' : 'list'
}

/** 默认分组维度：region/zone 两级（FR-452）。 */
const DEFAULT_GROUP_DIMENSION: GroupDimension = 'region'

function readGroupDimension(searchParams: URLSearchParams): GroupDimension {
  const value = searchParams.get('groupBy')
  if (value && (GROUP_DIMENSIONS as string[]).includes(value)) return value as GroupDimension
  return DEFAULT_GROUP_DIMENSION
}

/** 读取折叠分组键集合（FR-452）：URL `?collapsed=` 逗号分隔、逐键 decode。 */
function readCollapsed(searchParams: URLSearchParams): Set<string> {
  const raw = searchParams.get('collapsed')
  if (!raw) return new Set()
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

export default function InstancesPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  // 实例入口直接写入 URL，避免列表页点击依赖临时 store 再由 Workspace 二次同步。
  const openInstance = useCallback((id: number) => navigate(`/instances/${id}`), [navigate])
  const location = useLocation()
  const [searchParams, setSearchParams] = useSearchParams()
  const hadUrlNodeRef = useRef(searchParams.has('nodeId'))
  const updateUrl = useCallback((updates: InstanceUrlState) => {
    const next = new URLSearchParams(searchParams)
    writeInstanceUrlState(next, updates)
    setSearchParams(next)
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
    ...(searchQuery.trim() ? { q: searchQuery.trim() } : {}),
    pageSize,
    sort: sortKey,
    order: sortOrder,
  }), [pageSize, params, searchQuery, sortKey, sortOrder])
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
  const toggleOne = (id: number) =>
    setSelectedIds((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]))
  const allIds = instances?.map((i) => i.id) ?? []
  const allSelected = allIds.length > 0 && allIds.every((id) => selectedIds.includes(id))
  const toggleAll = () => setSelectedIds(allSelected ? [] : allIds)
  const clearSelection = () => setSelectedIds([])
  // 选中实例的 {id,name,status}，供批量栏做状态感知禁用与部分失败明细（FR-139）。
  const selectedInstances = useMemo(
    () =>
      instances
        .filter((i) => selectedIds.includes(i.id))
        .map((i) => ({ id: i.id, name: i.name, status: i.status })),
    [instances, selectedIds],
  )

  const scopedAllInstances = useMemo(
    () => (selectedNodeId == null ? instances : instances.filter((i) => i.nodeId === selectedNodeId)),
    [instances, selectedNodeId],
  )
  const envOptions = useMemo(() => collectEnvs(scopedAllInstances), [scopedAllInstances])
  const tagOptions = useMemo(() => collectTags(scopedAllInstances), [scopedAllInstances])
  const nodeName = (id: number) => nodes?.find((n) => n.id === id)?.name ?? t('console.unknownNode', { id })

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

  const setSearchQueryFilter = (value: string) => {
    setSearchQuery(value)
    updateUrl({ q: value, page: '1' })
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
  const summaryChips: SummaryChip[] = [
    {
      key: 'all',
      label: t('grouping.all'),
      count: counts.total,
      active: statusFilter === ALL,
      onClick: () => resetFilters(),
    },
    {
      key: 'running',
      label: t('instances.running'),
      count: counts.running,
      level: 'success',
      breathing: counts.running > 0,
      active: statusFilter === 'RUNNING',
      onClick: () => applySummaryFilter('running'),
    },
    {
      key: 'stopped',
      label: t('instances.stopped'),
      count: counts.stopped,
      level: 'neutral',
      active: statusFilter === 'STOPPED',
      onClick: () => applySummaryFilter('stopped'),
    },
    {
      key: 'crashed',
      label: t('instances.crashed'),
      count: counts.crashed,
      level: 'danger',
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
   * 行装配（ADR-097）：行展示件已入包（平铺表与分组树表共用），此处只注入数据与回调。
   * 逐行判别留在应用侧：节点名（查节点表）、代理画像（FR-445）、搭建中、运行态漂移（FR-471）、
   * 三个动作的按 id 在途态（别的行在提交不该禁用本行）。
   * proxy 内联展开与批量勾选是跨行共享状态，故仍由本页持有——虚拟窗口滚动会卸载行，
   * 行内自持会丢失展开态；勾选还与表头全选、批量栏同源。
   */
  const renderRow = (inst: InstanceInfo) => (
    <InstanceRowView
      inst={inst}
      nodeName={nodeName(inst.nodeId)}
      selected={selectedIds.includes(inst.id)}
      onToggleSelect={() => toggleOne(inst.id)}
      isProxy={resolveCapabilities(inst).capabilities.includes('bcTopology')}
      provisioning={isProvisioningInstance(inst)}
      drift={runtimeDriftOf(inst)}
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
    <PageShell data-page="instances">
      <PageHeader
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

      {/* 汇总头：运行/停止/崩溃计数，可点设筛选（FR-136） + 视图切换 */}
      <div className="flex items-center gap-2">
        <SummaryChips chips={summaryChips} className="flex-1" />
        <ViewToggle
          value={view}
          onChange={setViewParam}
          cardLabel={t('grouping.viewCard')}
          listLabel={t('grouping.viewList')}
        />
      </div>

      {/* 多维筛选 + 分组视图（FR-047/137）：sticky + 可折叠，避免长筛选条在移动端翻屏。 */}
      <div className="sticky top-16 z-20 jm-toolbar-surface space-y-2 p-2">
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

      {/* FR-496 阶段 6 补丁：数据未到时不再只给一行「加载中」。页头与（吸附的）筛选条先渲染，
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
        <div className="space-y-3">
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
            <CardView
              groupBy={groupBy}
              groups={groups}
              totalCount={totalCount}
              onNeedMore={loadMoreInstances}
              scrollStorageKey={scrollStorageKey}
              groupLabel={groupLabel}
              nodeName={nodeName}
              buildMenu={buildMenu}
              hasActiveFilter={hasActiveFilter}
              onOpenInstance={openInstance}
              renderCard={(args) => <InstanceWorktableCard {...args} />}
            />
          ) : groupBy === 'none' ? (
            <VirtualizedInstanceTable
              instances={instances}
              totalCount={totalCount}
              onNeedMore={loadMoreInstances}
              scrollStorageKey={scrollStorageKey}
              header={
                <InstanceTableHeader
                  t={t}
                  allSelected={allSelected}
                  onToggleAll={toggleAll}
                  sortKey={sortKey}
                  sortOrder={sortOrder}
                  onSort={setTableSortParam}
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
              scrollStorageKey={scrollStorageKey}
              header={
                <InstanceTableHeader
                  t={t}
                  allSelected={allSelected}
                  onToggleAll={toggleAll}
                  sortKey={sortKey}
                  sortOrder={sortOrder}
                  onSort={setTableSortParam}
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


