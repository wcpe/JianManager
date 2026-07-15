import { Fragment, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { ArrowUpDown, ChevronRight, ChevronDown, Zap, Globe, Plus, FolderTree, HardDriveDownload, Search, SlidersHorizontal } from 'lucide-react'
import {
  useInfiniteInstanceSearch,
  useInstanceAggregate,
  useStartInstance,
  useStopInstance,
  useRestartInstance,
  useDeleteInstance,
  useKillInstance,
  isProvisioningInstance,
  type InstanceListParams,
  type InstanceSearchParams,
  type InstanceInfo,
} from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useNetworks } from '@/api/networks'
import { useRegistrations } from '@/api/registrations'
import { useConsoleStore } from '@/stores/console'
import DangerConfirm from '@/components/DangerConfirm'
import InstanceBatchBar from '@/components/InstanceBatchBar'
import ProvisionServerDialog from '@/components/ProvisionServerDialog'
import ProvisionProxyDialog from '@/components/ProvisionProxyDialog'
import ImportServerWizard from '@/components/ImportServerWizard'
import ProxyRegistrationsDialog from '@/components/ProxyRegistrationsDialog'
import CloneInstanceDialog from '@/components/CloneInstanceDialog'
import InstanceTagsDialog from '@/components/InstanceTagsDialog'
import EditInstanceLimitsDialog from '@/components/EditInstanceLimitsDialog'
import EditInstanceConfigDialog from '@/components/EditInstanceConfigDialog'
import { InstanceWorktableCard } from '@/components/console/InstanceWorktableCard'
import { InstanceGroupManager } from '@/components/console/InstanceGroupManager'
import {
  collectEnvs,
  collectTags,
  envOf,
  freeTagsOf,
  groupInstances,
  parseTags,
  type GroupDimension,
} from '@/components/console/instance-grouping'
import { summarizeInstances, summaryFilterStatus, type SummaryFilterKey } from '@/lib/instance-summary'
import { Badge } from '@jianmanager/ui/components/badge'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { SummaryChips, type SummaryChip } from '@jianmanager/ui/components/summary-chips'
import { ViewToggle, type ViewMode } from '@jianmanager/ui/components/view-toggle'
import { cn, instanceStatusLevel } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { Input } from '@jianmanager/ui/components/input'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@jianmanager/ui/components/dropdown-menu'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import { useVirtualRows } from '@/lib/virtual-list'

/** Radix Select 不允许空字符串 value，用哨兵值表示「全部 / 不过滤」。 */
const ALL = '__all__'
const SCROLL_KEY_PREFIX = 'jm.instances.scroll:'

type InstanceUrlState = Partial<{
  q: string
  status: string
  page: string
  view: ViewMode
  groupBy: GroupDimension
  sort: InstanceSortKey
  order: InstanceSortOrder
  pageSize: string
  orgView: boolean
  networkId: string
  env: string
  tag: string
  nodeId: string
}>

type InstanceSortKey = NonNullable<InstanceSearchParams['sort']>
type InstanceSortOrder = NonNullable<InstanceSearchParams['order']>

const INSTANCE_SORT_KEYS: InstanceSortKey[] = ['name', 'status', 'createdAt', 'nodeId']
const INSTANCE_PAGE_SIZES = [50, 100, 200] as const

function readViewMode(searchParams: URLSearchParams): ViewMode {
  return searchParams.get('view') === 'list' ? 'list' : 'card'
}

function readGroupDimension(searchParams: URLSearchParams): GroupDimension {
  const value = searchParams.get('groupBy')
  if (value === 'node' || value === 'env' || value === 'status') return value
  return 'none'
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
  if (updates.view !== undefined) writeParam(searchParams, 'view', updates.view, 'card')
  if (updates.groupBy !== undefined) writeParam(searchParams, 'groupBy', updates.groupBy, 'none')
  if (updates.sort !== undefined) writeParam(searchParams, 'sort', updates.sort, 'createdAt')
  if (updates.order !== undefined) writeParam(searchParams, 'order', updates.order, 'asc')
  if (updates.pageSize !== undefined) writeParam(searchParams, 'pageSize', updates.pageSize, '200')
  if (updates.orgView !== undefined) {
    if (updates.orgView) searchParams.set('orgView', '1')
    else searchParams.delete('orgView')
  }
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
  // 批量操作选中的实例 ID 集合（FR-058）。
  const [selectedIds, setSelectedIds] = useState<number[]>([])
  // 工作台卡 ⇄ 列表视图（FR-136，§4.5）；运行实体默认卡片。
  const [view, setView] = useState<ViewMode>(() => readViewMode(searchParams))
  // 组织分组视图开关（FR-165，§4.4）：开启后切到「左分组树 + 右列表」专用形态，
  // 与既有筛选/groupBy 分组并列、互不破坏；关闭回到原平铺/分组视图。
  const [orgView, setOrgView] = useState(() => searchParams.get('orgView') === '1')
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
      setOrgView(nextParams.get('orgView') === '1')

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

  const start = useStartInstance()
  const stop = useStopInstance()
  const restart = useRestartInstance()
  const kill = useKillInstance()
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

  const groups = useMemo(() => groupInstances(instances, groupBy), [instances, groupBy])

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
  const setOrgViewParam = (value: boolean) => {
    setOrgView(value)
    updateUrl({ orgView: value })
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

  /** 按分组维度给出分组标题；env 维度的空 key 显示「未分环境」。 */
  const groupLabel = (key: string): string => {
    if (groupBy === 'node') return nodeName(Number(key))
    if (groupBy === 'env') return key === '' ? t('grouping.envNone') : t(`grouping.env_${key}`, { defaultValue: key })
    if (groupBy === 'status') return statusConfig[key]?.text ?? key
    return ''
  }

  const buildMenu = (inst: InstanceInfo) => (
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
      onDelete={() => setDeleteTarget({
        id: inst.id,
        name: inst.name,
        inPlace: !!inst.workDirInPlace,
        // FR-310：非停止态删除由后端编排「先停止（超时强杀）再删除」，确认框据此提示。
        running: inst.status !== 'STOPPED' && inst.status !== 'CRASHED',
      })}
    />
  )

  const toggleProxy = (id: number) =>
    setExpandedProxies((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  const renderRow = (inst: InstanceInfo) => {
    const st = statusConfig[inst.status] || statusConfig.STOPPED
    const instEnv = envOf(inst)
    const free = freeTagsOf(inst)
    const isProxy = inst.role === 'proxy'
    const proxyExpanded = expandedProxies.has(inst.id)
    return (
      <Fragment key={inst.id}>
        <TableRow data-state={selectedIds.includes(inst.id) ? 'selected' : undefined}>
          <TableCell>
            <Checkbox
              checked={selectedIds.includes(inst.id)}
              onCheckedChange={() => toggleOne(inst.id)}
              aria-label={inst.name}
            />
          </TableCell>
          <TableCell className="font-medium">
            <div className="flex items-center gap-1.5">
              {isProxy && (
                <button
                  type="button"
                  onClick={() => toggleProxy(inst.id)}
                  aria-label={t('proxy.manageBackends')}
                  className="text-muted-foreground hover:text-foreground"
                >
                  {proxyExpanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
                </button>
              )}
              <button
                type="button"
                className="text-left text-primary hover:underline"
                onClick={() => openInstance(inst.id)}
              >
                {inst.name}
              </button>
              {/* 就地导入徽章（FR-302）：工作目录在托管区外，删除实例不删原目录。 */}
              {inst.workDirInPlace && (
                <Badge variant="outline" className="shrink-0 border-amber-500/50 text-amber-600 dark:text-amber-400">
                  {t('importServer.inPlaceBadge')}
                </Badge>
              )}
            </div>
          </TableCell>
          <TableCell className="text-muted-foreground">{inst.type}</TableCell>
          {/* 节点:端口（FR-136）：serverPort 已有数据 */}
          <TableCell className="text-muted-foreground text-xs whitespace-nowrap">
            {nodeName(inst.nodeId)}
            {inst.serverPort > 0 && <span className="tabular-nums">:{inst.serverPort}</span>}
          </TableCell>
          <TableCell>
            <RoleBadge role={inst.role} />
          </TableCell>
          <TableCell>
            <div className="flex flex-wrap items-center gap-1">
              {instEnv && (
                <Badge variant="outline" className="border-primary/40 text-primary">
                  {t(`grouping.env_${instEnv}`, { defaultValue: instEnv })}
                </Badge>
              )}
              {free.map((tg) => (
                <Badge key={tg} variant="secondary" className="font-normal">
                  {tg}
                </Badge>
              ))}
              {!instEnv && free.length === 0 && <span className="text-muted-foreground text-xs">--</span>}
            </div>
          </TableCell>
          <TableCell>
            <StatusBadge
              level={instanceStatusLevel(inst.status)}
              label={st.text}
              pulse={inst.status === 'STARTING' || inst.status === 'STOPPING'}
            />
          </TableCell>
          <TableCell>
            <div className="flex items-center gap-1">
              {/* 主操作随状态，操作进行中禁用防连点（FR-138）；
                  搭建中硬性禁启（FR-331），tooltip 由外层 span 承载（禁用态 pointer-events-none）。 */}
              {(inst.status === 'STOPPED' || inst.status === 'CRASHED') && (
                <span title={isProvisioningInstance(inst) ? t('instances.provisioningBlocked') : undefined}>
                  <Button
                    variant="ghost"
                    size="xs"
                    disabled={isProvisioningInstance(inst) || (start.isPending && start.variables === inst.id)}
                    onClick={() => start.mutate(inst.id)}
                    aria-label={t('instances.start')}
                    className="text-green-600 hover:text-green-700"
                  >
                    {start.isPending && start.variables === inst.id ? t('instances.processing') : t('instances.start')}
                  </Button>
                </span>
              )}
              {inst.status === 'RUNNING' && (
                <>
                  <Button
                    variant="ghost"
                    size="xs"
                    disabled={stop.isPending && stop.variables === inst.id}
                    onClick={() => stop.mutate(inst.id)}
                    aria-label={t('instances.stop')}
                    className="text-yellow-600 hover:text-yellow-700"
                  >
                    {stop.isPending && stop.variables === inst.id ? t('instances.processing') : t('instances.stop')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="xs"
                    disabled={restart.isPending && restart.variables === inst.id}
                    onClick={() => restart.mutate(inst.id)}
                    aria-label={t('instances.restart')}
                    className="text-blue-600 hover:text-blue-700"
                  >
                    {restart.isPending && restart.variables === inst.id ? t('instances.processing') : t('instances.restart')}
                  </Button>
                </>
              )}
              {(inst.status === 'STARTING' || inst.status === 'STOPPING') && (
                <Button
                  variant="ghost"
                  size="xs"
                  onClick={() => setKillTarget({ id: inst.id, name: inst.name })}
                  aria-label={t('instances.kill')}
                  className="text-yellow-600 hover:text-yellow-700"
                >
                  {t('instances.kill')}
                </Button>
              )}
              {buildMenu(inst)}
            </div>
          </TableCell>
        </TableRow>
        {isProxy && proxyExpanded && (
          <TableRow className="bg-muted/30 hover:bg-muted/30">
            <TableCell colSpan={8} className="p-0">
              <BackendsInline proxyId={inst.id} />
            </TableCell>
          </TableRow>
        )}
      </Fragment>
    )
  }

  return (
    <div data-page="instances" className="jm-page-stack space-y-4">
      <div className="jm-page-header">
        <h1 className="jm-page-title">{t('instances.title')}</h1>
        {/* flex-wrap：四个入口按钮在移动端（390px）超行宽须换行——修 v0.15.0 验收 e2e
            抓出的移动端横向溢出 120px（FR-302 加第 4 按钮后顶爆无 wrap 的一行）。 */}
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
      </div>

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
            <Button
              variant={orgView ? 'default' : 'outline'}
              size="sm"
              onClick={() => setOrgViewParam(!orgView)}
              aria-pressed={orgView}
            >
              <FolderTree className="size-4" /> {t('instanceGroups.orgView')}
            </Button>
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
            {!orgView && (
              <div className="flex items-center gap-2">
                <span className="text-sm text-muted-foreground">{t('grouping.groupBy')}</span>
                <Select value={groupBy} onValueChange={(v) => setGroupByParam(v as GroupDimension)}>
                  <SelectTrigger size="sm" className="w-32">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">{t('grouping.dim_none')}</SelectItem>
                    <SelectItem value="node">{t('grouping.dim_node')}</SelectItem>
                    <SelectItem value="env">{t('grouping.dim_env')}</SelectItem>
                    <SelectItem value="status">{t('grouping.dim_status')}</SelectItem>
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

      {orgView ? (
        <InstanceGroupManager />
      ) : isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
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
              groups={groups}
              totalCount={totalCount}
              loadedCount={instances.length}
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
              groupLabel={groupLabel}
              emptyLabel={hasActiveFilter ? t('grouping.noMatch') : t('instances.empty')}
            />
          )}
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
    </div>
  )
}

type GroupedInstanceRow =
  | { kind: 'group'; key: string; label: string; count: number }
  | { kind: 'instance'; key: string; instance: InstanceInfo }

function buildGroupedInstanceRows(
  groups: { key: string; instances: InstanceInfo[] }[],
  groupLabel: (key: string) => string,
): GroupedInstanceRow[] {
  return groups.flatMap((group) => [
    { kind: 'group' as const, key: `group:${group.key || '__none__'}`, label: groupLabel(group.key), count: group.instances.length },
    ...group.instances.map((instance) => ({ kind: 'instance' as const, key: `instance:${instance.id}`, instance })),
  ])
}

function VirtualizedGroupedInstanceTable({
  groups,
  totalCount,
  loadedCount,
  onNeedMore,
  scrollStorageKey,
  header,
  renderRow,
  groupLabel,
  emptyLabel,
}: {
  groups: { key: string; instances: InstanceInfo[] }[]
  totalCount: number
  loadedCount: number
  onNeedMore: () => void
  scrollStorageKey: string
  header: React.ReactNode
  renderRow: (inst: InstanceInfo) => React.ReactNode
  groupLabel: (key: string) => string
  emptyLabel: string
}) {
  const rows = useMemo(() => buildGroupedInstanceRows(groups, groupLabel), [groupLabel, groups])
  const {
    containerRef,
    onScroll,
    range,
  } = useVirtualRows({
    total: rows.length,
    itemSize: 44,
    overscan: 10,
    fallbackViewportSize: 520,
  })
  const handleScroll = useStoredVirtualScroll(containerRef, onScroll, scrollStorageKey)

  useEffect(() => {
    if (range.end + 20 >= rows.length && loadedCount < totalCount) {
      onNeedMore()
    }
  }, [loadedCount, onNeedMore, range.end, rows.length, totalCount])

  return (
    <div
      ref={containerRef}
      onScroll={handleScroll}
      data-testid="instances-table-virtual"
      data-total-count={totalCount}
      className="max-h-[calc(100vh-20rem)] min-h-72 overflow-auto rounded-lg border bg-card/95 shadow-soft"
    >
      <Table>
        {header}
        <TableBody>
          {range.before > 0 && (
            <TableRow aria-hidden="true">
              <TableCell colSpan={8} className="p-0" style={{ height: range.before }} />
            </TableRow>
          )}
          {rows.slice(range.start, range.end).map((row) => row.kind === 'group' ? (
            <TableRow key={row.key} data-testid="instances-group-row" className="sticky top-9 z-10 bg-muted/80 backdrop-blur">
              <TableCell colSpan={8} className="h-11 px-4 py-2">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium">{row.label}</span>
                  <Badge variant="outline" className="font-normal">{row.count}</Badge>
                </div>
              </TableCell>
            </TableRow>
          ) : (
            <Fragment key={row.key}>{renderRow(row.instance)}</Fragment>
          ))}
          {range.after > 0 && (
            <TableRow aria-hidden="true">
              <TableCell colSpan={8} className="p-0" style={{ height: range.after }} />
            </TableRow>
          )}
          {rows.length === 0 && (
            <TableRow>
              <TableCell colSpan={8} className="text-center text-muted-foreground">
                {emptyLabel}
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
    </div>
  )
}

function VirtualizedInstanceTable({
  instances,
  totalCount,
  onNeedMore,
  scrollStorageKey,
  header,
  renderRow,
  emptyLabel,
}: {
  instances: InstanceInfo[]
  totalCount: number
  onNeedMore: () => void
  scrollStorageKey: string
  header: React.ReactNode
  renderRow: (inst: InstanceInfo) => React.ReactNode
  emptyLabel: string
}) {
  const {
    containerRef,
    onScroll,
    range,
  } = useVirtualRows({
    total: totalCount,
    itemSize: 44,
    overscan: 10,
    fallbackViewportSize: 520,
  })
  const handleScroll = useStoredVirtualScroll(containerRef, onScroll, scrollStorageKey)

  useEffect(() => {
    if (range.end + 20 >= instances.length && instances.length < totalCount) {
      onNeedMore()
    }
  }, [instances.length, onNeedMore, range.end, totalCount])

  const visible = []
  for (let index = range.start; index < range.end; index++) {
    visible.push({ index, inst: instances[index] })
  }

  return (
    <div
      ref={containerRef}
      onScroll={handleScroll}
      data-testid="instances-table-virtual"
      data-total-count={totalCount}
      className="max-h-[calc(100vh-20rem)] min-h-72 overflow-auto rounded-lg border bg-card/95 shadow-soft"
    >
      <Table>
        {header}
        <TableBody>
          {range.before > 0 && (
            <TableRow aria-hidden="true">
              <TableCell colSpan={8} className="p-0" style={{ height: range.before }} />
            </TableRow>
          )}
          {visible.map(({ index, inst }) => (
            <Fragment key={inst?.id ?? `placeholder-${index}`}>
              {inst ? renderRow(inst) : (
                <TableRow aria-hidden="true">
                  <TableCell colSpan={8} className="p-0" style={{ height: 44 }} />
                </TableRow>
              )}
            </Fragment>
          ))}
          {range.after > 0 && (
            <TableRow aria-hidden="true">
              <TableCell colSpan={8} className="p-0" style={{ height: range.after }} />
            </TableRow>
          )}
          {totalCount === 0 && (
            <TableRow>
              <TableCell colSpan={8} className="text-center text-muted-foreground">
                {emptyLabel}
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
    </div>
  )
}

function useStoredVirtualScroll(
  containerRef: React.RefObject<HTMLDivElement | null>,
  onScroll: () => void,
  storageKey: string,
) {
  const clearZeroTimerRef = useRef<number | null>(null)

  const cancelClearZeroTimer = useCallback(() => {
    if (clearZeroTimerRef.current == null) return
    window.clearTimeout(clearZeroTimerRef.current)
    clearZeroTimerRef.current = null
  }, [])

  useLayoutEffect(() => {
    const saved = Number(sessionStorage.getItem(storageKey) ?? 0)
    if (!Number.isFinite(saved) || saved <= 0) return

    let cancelled = false
    let attempts = 0
    let timer: number | null = null
    const restore = () => {
      if (cancelled) return
      const el = containerRef.current
      attempts += 1
      if (!el) {
        if (attempts < 20) timer = window.setTimeout(restore, 50)
        return
      }
      el.scrollTop = saved
      onScroll()
      if (el.scrollTop < saved && attempts < 20) timer = window.setTimeout(restore, 50)
    }

    timer = window.setTimeout(restore, 0)
    return () => {
      cancelled = true
      if (timer != null) window.clearTimeout(timer)
    }
  }, [containerRef, onScroll, storageKey])

  useEffect(() => cancelClearZeroTimer, [cancelClearZeroTimer])

  return useCallback(() => {
    onScroll()
    const el = containerRef.current
    if (!el) return

    const scrollTop = Math.round(el.scrollTop)
    if (scrollTop <= 0) {
      cancelClearZeroTimer()
      clearZeroTimerRef.current = window.setTimeout(() => {
        const currentKey = `${SCROLL_KEY_PREFIX}${window.location.pathname}${window.location.search}`
        if (currentKey === storageKey) sessionStorage.removeItem(storageKey)
        clearZeroTimerRef.current = null
      }, 150)
      return
    }

    cancelClearZeroTimer()
    sessionStorage.setItem(storageKey, String(scrollTop))
  }, [cancelClearZeroTimer, containerRef, onScroll, storageKey])
}

/**
 * 卡片视图（FR-136 工作台卡）：平铺或按分组维度分段渲染工作台卡网格。
 * 分组维度非 none 时每组一段（组头 + 该组卡片网格）。
 */
function CardView({
  groupBy,
  groups,
  totalCount,
  onNeedMore,
  scrollStorageKey,
  groupLabel,
  nodeName,
  buildMenu,
  hasActiveFilter,
  onOpenInstance,
}: {
  groupBy: GroupDimension
  groups: { key: string; instances: InstanceInfo[] }[]
  totalCount: number
  onNeedMore: () => void
  scrollStorageKey: string
  groupLabel: (key: string) => string
  nodeName: (id: number) => string
  buildMenu: (inst: InstanceInfo) => React.ReactNode
  hasActiveFilter: boolean
  onOpenInstance: (id: number) => void
}) {
  const { t } = useTranslation()
  const grid = (list: InstanceInfo[], count = list.length, onMore: () => void = () => {}, key = scrollStorageKey) => (
    <VirtualizedCardGrid
      instances={list}
      totalCount={count}
      onNeedMore={onMore}
      scrollStorageKey={key}
      nodeName={nodeName}
      buildMenu={buildMenu}
      onOpenInstance={onOpenInstance}
    />
  )

  const loadedTotal = groups.reduce((sum, g) => sum + g.instances.length, 0)
  if (totalCount === 0 && loadedTotal === 0) {
    return (
      <p className="text-center text-muted-foreground py-8">
        {hasActiveFilter ? t('grouping.noMatch') : t('instances.empty')}
      </p>
    )
  }

  if (groupBy === 'none') {
    return grid(groups[0]?.instances ?? [], totalCount, onNeedMore)
  }
  return (
    <div className="space-y-4">
      {groups.map((g) => (
        <div key={g.key || '__none__'} className="space-y-2">
          <div className="flex items-center gap-2 px-1">
            <span className="text-sm font-medium">{groupLabel(g.key)}</span>
            <Badge variant="outline" className="font-normal">{g.instances.length}</Badge>
          </div>
          {grid(g.instances, g.instances.length, () => undefined, `${scrollStorageKey}:group:${g.key || '__none__'}`)}
        </div>
      ))}
    </div>
  )
}

const CARD_ROW_HEIGHT = 244

function readCardColumns(): number {
  if (typeof window === 'undefined') return 3
  if (window.innerWidth >= 1280) return 3
  if (window.innerWidth >= 640) return 2
  return 1
}

function useCardColumns(): number {
  const [columns, setColumns] = useState(readCardColumns)

  useEffect(() => {
    const update = () => setColumns(readCardColumns())
    window.addEventListener('resize', update)
    return () => window.removeEventListener('resize', update)
  }, [])

  return columns
}

function VirtualizedCardGrid({
  instances,
  totalCount,
  onNeedMore,
  scrollStorageKey,
  nodeName,
  buildMenu,
  onOpenInstance,
}: {
  instances: InstanceInfo[]
  totalCount: number
  onNeedMore: () => void
  scrollStorageKey: string
  nodeName: (id: number) => string
  buildMenu: (inst: InstanceInfo) => React.ReactNode
  onOpenInstance: (id: number) => void
}) {
  const columns = useCardColumns()
  const rowCount = Math.ceil(totalCount / columns)
  const {
    containerRef,
    onScroll,
    range,
    totalSize,
  } = useVirtualRows({
    total: rowCount,
    itemSize: CARD_ROW_HEIGHT,
    overscan: 4,
    fallbackViewportSize: 720,
  })
  const handleScroll = useStoredVirtualScroll(containerRef, onScroll, scrollStorageKey)

  useEffect(() => {
    if (range.end * columns + 20 >= instances.length && instances.length < totalCount) {
      onNeedMore()
    }
  }, [columns, instances.length, onNeedMore, range.end, totalCount])

  const rows = []
  for (let rowIndex = range.start; rowIndex < range.end; rowIndex++) {
    const startIndex = rowIndex * columns
    const rowItems: Array<{ index: number; inst?: InstanceInfo }> = []
    for (let offset = 0; offset < columns; offset++) {
      const index = startIndex + offset
      if (index < totalCount) rowItems.push({ index, inst: instances[index] })
    }
    rows.push({ rowIndex, rowItems })
  }

  return (
    <div
      ref={containerRef}
      onScroll={handleScroll}
      data-testid="instances-card-virtual"
      data-total-count={totalCount}
      className="max-h-[calc(100vh-18rem)] min-h-96 overflow-auto pr-1"
    >
      <div className="relative" style={{ height: totalSize }}>
        {rows.map(({ rowIndex, rowItems }) => (
          <div
            key={rowIndex}
            className="absolute inset-x-0 grid gap-4"
            style={{
              top: rowIndex * CARD_ROW_HEIGHT,
              minHeight: CARD_ROW_HEIGHT - 16,
              gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))`,
            }}
          >
            {rowItems.map(({ index, inst }) => (
              <div key={inst?.id ?? `placeholder-${index}`} data-testid="instances-card-virtual-item">
                {inst ? (
                  <InstanceWorktableCard
                    inst={inst}
                    nodeName={nodeName(inst.nodeId)}
                    roleBadge={<RoleBadge role={inst.role} compact />}
                    menu={buildMenu(inst)}
                    onOpen={onOpenInstance}
                  />
                ) : (
                  <div className="h-[228px] rounded-lg border border-dashed bg-card/70" aria-hidden="true" />
                )}
              </div>
            ))}
          </div>
        ))}
      </div>
    </div>
  )
}

/** proxy 行 inline 展开的已注册 backend 摘要（FR-136），用既有 useRegistrations。 */
function BackendsInline({ proxyId }: { proxyId: number }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data, isLoading } = useRegistrations(proxyId)

  if (isLoading) {
    return <p className="px-6 py-3 text-sm text-muted-foreground">{t('common.loading')}</p>
  }
  const regs = data ?? []
  if (regs.length === 0) {
    return <p className="px-6 py-3 text-sm text-muted-foreground">{t('proxy.noBackends')}</p>
  }
  return (
    <div className="px-6 py-3">
      <div className="mb-2 text-xs font-medium text-muted-foreground">
        {t('proxy.registeredBackends', { count: regs.length })}
      </div>
      <ul className="space-y-1">
        {regs.map((r) => {
          const b = r.backend
          return (
            <li key={r.id} className="flex items-center gap-3 text-sm">
              <span className="text-muted-foreground">{r.priority}</span>
              {b ? (
                <button
                  type="button"
                  className="font-medium text-primary hover:underline"
                  onClick={() => navigate(`/instances/${b.id}`)}
                >
                  {b.name}
                </button>
              ) : (
                <span className="font-medium">{r.alias || `#${r.backendId}`}</span>
              )}
              {r.alias && <span className="text-xs text-muted-foreground">({r.alias})</span>}
              {b && (
                <StatusBadge
                  level={instanceStatusLevel(b.status)}
                  label={t(`instances.${b.status.toLowerCase()}`, b.status)}
                  className="ml-auto"
                />
              )}
              {b && b.serverPort > 0 && (
                <span className="text-xs tabular-nums text-muted-foreground">:{b.serverPort}</span>
              )}
              {!r.enabled && (
                <Badge variant="outline" className="text-muted-foreground">
                  {t('common.disabled')}
                </Badge>
              )}
            </li>
          )
        })}
      </ul>
    </div>
  )
}

/** 角色三态统一语义色徽标（FR-136）：proxy 主色 / backend 次色 / universal 中性。 */
function RoleBadge({ role, compact = false }: { role: string; compact?: boolean }) {
  const { t } = useTranslation()
  if (role === 'proxy') {
    return (
      <Badge variant="outline" className="border-primary/40 bg-accent text-primary">
        {t('networks.role_proxy')}
      </Badge>
    )
  }
  if (role === 'backend') {
    return (
      <Badge variant="outline" className="border-status-info/40 text-status-info">
        {t('networks.role_backend')}
      </Badge>
    )
  }
  if (compact) return null
  return <span className="text-muted-foreground text-xs">{t('networks.role_universal')}</span>
}

/** 实例表头（平铺与分组视图复用）。含批量全选复选框（FR-058）与节点:端口列（FR-136）。 */
function InstanceTableHeader({
  t,
  allSelected,
  onToggleAll,
  sortKey,
  sortOrder,
  onSort,
}: {
  t: (k: string, options?: Record<string, unknown>) => string
  allSelected: boolean
  onToggleAll: () => void
  sortKey: InstanceSortKey
  sortOrder: InstanceSortOrder
  onSort: (key: InstanceSortKey) => void
}) {
  return (
    <TableHeader className="bg-muted/50">
      <TableRow>
        <TableHead className="w-10">
          <Checkbox checked={allSelected} onCheckedChange={onToggleAll} aria-label={t('instanceBatch.selectAll')} />
        </TableHead>
        <SortableTableHead
          label={t('instances.name')}
          sortValue="name"
          currentSort={sortKey}
          currentOrder={sortOrder}
          onSort={onSort}
        />
        <TableHead>{t('instances.type')}</TableHead>
        <SortableTableHead
          label={t('instances.nodePort')}
          sortValue="nodeId"
          currentSort={sortKey}
          currentOrder={sortOrder}
          onSort={onSort}
        />
        <TableHead>{t('instances.role')}</TableHead>
        <TableHead>{t('grouping.tagsColumn')}</TableHead>
        <SortableTableHead
          label={t('instances.status')}
          sortValue="status"
          currentSort={sortKey}
          currentOrder={sortOrder}
          onSort={onSort}
        />
        <TableHead>{t('instances.actions')}</TableHead>
      </TableRow>
    </TableHeader>
  )
}

function SortableTableHead({
  label,
  sortValue,
  currentSort,
  currentOrder,
  onSort,
}: {
  label: string
  sortValue: InstanceSortKey
  currentSort: InstanceSortKey
  currentOrder: InstanceSortOrder
  onSort: (key: InstanceSortKey) => void
}) {
  const { t } = useTranslation()
  const active = currentSort === sortValue
  const orderLabel = currentOrder === 'desc' ? t('grouping.orderDesc') : t('grouping.orderAsc')
  return (
    <TableHead aria-sort={active ? (currentOrder === 'desc' ? 'descending' : 'ascending') : 'none'}>
      <button
        type="button"
        onClick={() => onSort(sortValue)}
        aria-label={active ? t('grouping.sortHeaderActive', { label, order: orderLabel }) : t('grouping.sortHeader', { label })}
        className="inline-flex items-center gap-1 rounded px-1 py-0.5 text-left transition-colors hover:bg-accent/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      >
        <span>{label}</span>
        <ArrowUpDown className={cn('size-3.5', active ? 'text-primary' : 'text-muted-foreground/60')} aria-hidden="true" />
      </button>
    </TableHead>
  )
}

interface FilterOption {
  value: string
  label: string
}

/** 单个筛选下拉：含「全部」哨兵项 + 给定选项；无选项时禁用。 */
function FilterSelect({
  label,
  value,
  onChange,
  options,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  options: FilterOption[]
}) {
  const { t } = useTranslation()
  return (
    <Select value={value} onValueChange={onChange} disabled={options.length === 0}>
      <SelectTrigger size="sm" className="w-40" aria-label={label}>
        <SelectValue placeholder={label} />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={ALL}>
          {label}：{t('grouping.all')}
        </SelectItem>
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value}>
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/**
 * 实例行的「⋯」次要操作菜单（FR-138）：标签 / 资源限额 / 代理后端 / 克隆 / 删除收入下拉，
 * 行内只保留启停/重启主操作。运行态下克隆改禁用 + tooltip（非消失）；删除标红且运行态
 * 仍可用（FR-310：后端编排先停止再删除），tooltip 提示该行为。
 */
function InstanceRowMenu({
  inst,
  onTags,
  onEditConfig,
  onLimits,
  onProxy,
  onClone,
  onDelete,
}: {
  inst: InstanceInfo
  onTags: () => void
  onEditConfig: () => void
  onLimits: () => void
  onProxy: () => void
  onClone: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  // 克隆要求实例已停止（运行/过渡态禁用并提示原因，而非隐藏）；
  // 删除运行态可用（FR-310 先停再删），仅以 tooltip 提示行为。
  const stopped = inst.status === 'STOPPED' || inst.status === 'CRASHED'

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="xs" aria-label={t('instances.moreActions')} className="px-1.5">
          ⋯
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem onSelect={onEditConfig}>{t('instances.editConfig')}</DropdownMenuItem>
        <DropdownMenuItem onSelect={onTags}>{t('grouping.editTags')}</DropdownMenuItem>
        {inst.processType === 'docker' && (
          <DropdownMenuItem onSelect={onLimits}>{t('instances.resourceLimit')}</DropdownMenuItem>
        )}
        {inst.role === 'proxy' && (
          <DropdownMenuItem onSelect={onProxy}>{t('proxy.manageBackends')}</DropdownMenuItem>
        )}
        {inst.role === 'backend' && (
          <DropdownMenuItem
            title={stopped ? undefined : t('instances.cloneRunningHint')}
            className={stopped ? undefined : 'opacity-50 cursor-not-allowed'}
            onSelect={(e) => {
              if (!stopped) {
                e.preventDefault()
                return
              }
              onClone()
            }}
          >
            {t('clone.action')}
          </DropdownMenuItem>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem
          variant="destructive"
          title={stopped ? undefined : t('instances.deleteRunningHint')}
          onSelect={onDelete}
        >
          {t('common.delete')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
