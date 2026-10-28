import { useMemo, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { usePageContext } from '@/lib/assistant/page-context'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Plus, Search, Server } from 'lucide-react'
import { useNodes, useSetNodeMaintenance, useDrainNode, useDeleteNode, useArchivedNodes, usePurgeArchivedNode, useNodeStartupProgress, type NodeInfo, type ArchivedNode } from '@/api/nodes'
import type { NodeDeleteBlockedInstance } from '@/api/nodes'
import { useInstanceAggregate, useInstanceSearch } from '@/api/instances'
import { useNodeProxy, useUpdateNodeProxy } from '@/api/nodes'
import { useArtifactCache, useSetArtifactCacheCap } from '@/api/nodeRuntime'
import { describeCap } from '@/lib/artifacts/artifact-cache'
import { useNodePMConfig } from '@/api/pmConfig'
import { useNodeProbeVersion, useServerProbeCatalog, useSetNodeProbeVersion } from '@/api/artifactVersions'
import { useAuditLogs } from '@/api/audit'
import { parseTags } from '@/lib/instances/instance-grouping'
import { hasRuntimeDrift } from '@/lib/runtime-assets/runtime-drift'
import {
  useMetricSeries,
  useMetricSeriesBatch,
  useProcessTop,
  useManagedProcessDetail,
  useManagedProcessAction,
  type ProcessTopItem,
} from '@/api/metrics'
import type { ManagedProcessAction } from '@/lib/console/managed-process'
import { Panel } from '@jianmanager/ui/components/panel'
import { Input } from '@jianmanager/ui/components/input'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { SummaryChips, type SummaryChip } from '@jianmanager/ui/components/summary-chips'
import { CardsGrid, DataPanelSkeleton, PageHeader, PageShell, ScopeBar, Segment, Segments } from '@jianmanager/ui/components/layout'
import { type ChartSeries } from '@jianmanager/ui'
import { summarizeNodes } from '@/lib/nodes/node-summary'
import {
  filterNodes,
  resolveSelectedNode,
  type NodeStatusFilter,
} from '@/lib/nodes/node-list'
import { cn } from '@jianmanager/ui'

import NodeJDKTab from '@/components/nodes/NodeJDKTab'
import NodeLogRuntimeTab from '@/components/nodes/NodeLogRuntimeTab'
import NodePortsTab from '@/components/nodes/NodePortsTab'
import {
  NodeProcessPanel,
  NodeProcessDetailDialog,
  type ProcessSort,
  type ProcessInstanceRef,
} from '@/components/views/nodes/NodeProcessPanel'
import NodeArtifactCacheTab from '@/components/nodes/NodeArtifactCacheTab'
import NodeProxyTab from '@/components/nodes/NodeProxyTab'
import NodeProbeVersionTab from '@/components/nodes/NodeProbeVersionTab'
import NodeRepairTab from '@/components/nodes/NodeRepairTab'
import DangerConfirm from '@/components/views/common/DangerConfirm'
import AddNodeDialogContainer from '@/components/nodes/AddNodeDialogContainer'
import { Button } from '@jianmanager/ui/components/button'
import { BlockedByInstancesDialog } from '@/components/views/nodes/BlockedByInstancesDialog'
import { NodeCard } from '@/components/views/nodes/NodeListParts'
import { NodeOverviewSection } from '@/components/views/nodes/NodeOverviewSection'
import { NodeInstanceList, type NodeInstanceUsage } from '@/components/views/nodes/NodeInstanceList'
import { NodeTable, type NodeSortKey, type NodeSortOrder } from '@/components/views/nodes/NodeTable'
import { NodeRuntimeOverview } from '@/components/views/nodes/NodeRuntimeOverview'
import { NodeSelector } from '@/components/views/nodes/NodeSelector'
import { NodeConfigSection } from '@/components/views/nodes/NodeConfigSection'
import { useDynamicPageSize } from '@/lib/hooks/use-dynamic-page-size'
import {
  applyNodeColumnMove,
  readStoredNodeOrder,
  writeStoredNodeOrder,
  type NodeColumnKey,
} from '@/lib/nodes/node-columns'
import { ArchivedNodeDetailPane } from '@/components/views/nodes/ArchivedNodeDetailPane'
import { NodeStartupProgressPanel } from '@/components/views/nodes/NodeStartupProgressPanel'
import { COMPARE_TARGET_CAP, NodeInstanceCompare, NodeMonitorCharts, useNodeMetricRange } from '@/components/views/nodes/NodeCharts'
import { DETAIL_TABS, NodeDetailPane } from '@/components/views/nodes/NodeDetailPane'
import type { DetailTab } from '@/components/views/nodes/NodeDetailPane'


/** 待二次确认的危险节点操作（FR-048）。 */
type PendingAction = { kind: 'drain' | 'delete'; node: NodeInfo }

/** 节点下线被实例守卫 409 拒绝的上下文（FR-309）：节点 + 名下实例清单。 */
type DeleteConflict = { node: NodeInfo; instances: NodeDeleteBlockedInstance[] }

/** 归档清理被实例守卫 409 拒绝的上下文（FR-394）。 */
type PurgeConflict = { node: ArchivedNode; instances: NodeDeleteBlockedInstance[] }

/** 页面级视图：活跃 | 归档（FR-393，URL `?view=` 可寻址，默认 active）。 */
type NodesView = 'active' | 'archive'

function readNodesView(searchParams: URLSearchParams): NodesView {
  return searchParams.get('view') === 'archive' ? 'archive' : 'active'
}

/**
 * 列表 / 卡片两种呈现方式（用户可切换，选择持久化）。
 *
 * 【为什么参数叫 layout 而不是 view】`?view=` 已被「活跃 / 归档」占用（FR-393）。
 * 两个语义挤在一个参数里，将来加第三种（如「按机房」）就会互相覆盖。
 */
type NodesLayout = 'list' | 'card'

const NODES_LAYOUT_KEY = 'jm.nodes.layout'

function readNodesLayout(searchParams: URLSearchParams): NodesLayout {
  const fromUrl = searchParams.get('layout')
  if (fromUrl === 'card' || fromUrl === 'list') return fromUrl
  try {
    const stored = localStorage.getItem(NODES_LAYOUT_KEY)
    if (stored === 'card' || stored === 'list') return stored
  } catch {
    /* 无存储能力时用默认值 */
  }
  // 默认列表：节点数量通常偏少但字段多，列表的密度与可比性优于卡片。
  return 'list'
}



/** 从 URL `?tab=` 解析激活分段（FR-128 可寻址；非法值回退默认 overview）。 */
function readDetailTab(searchParams: URLSearchParams): DetailTab {
  const tab = searchParams.get('tab')
  if (tab === 'jdk') return 'runtime' // 旧链接兼容：tab=jdk → 运行时
  return DETAIL_TABS.includes(tab as DetailTab) ? (tab as DetailTab) : 'overview'
}




export default function NodesPage() {
  const { t } = useTranslation()
  // 选中节点与激活分段均入 URL（FR-128 可寻址）：`?node=<id>` 深链（命令面板 FR-241 跳转携带）、
  // `?tab=<DetailTab>` 激活分段（默认 overview 省略）；`?view=active|archive` 页面视图（FR-393）。
  const [searchParams, setSearchParams] = useSearchParams()
  const navigate = useNavigate()
  const view = readNodesView(searchParams)
  const isArchive = view === 'archive'

  // 页面上报（FR-518 §5.1）：`?node=<id>` 深链即「正在看哪个节点」，可直接派生。
  // 只发类型化引用，不发节点名/凭证（名称由后端按权限回填）。
  const contextNodeId = searchParams.get('node')
  usePageContext(
    useMemo(
      () => ({
        pageKey: 'nodes',
        ...(view ? { tab: view } : {}),
        ...(contextNodeId ? { entities: [{ type: 'node' as const, id: contextNodeId }] } : {}),
      }),
      [contextNodeId, view],
    ),
  )
  const { data: nodes, isLoading, isError: nodesError, refetch: refetchNodes } = useNodes({
    refetchInterval: 30_000,
    enabled: !isArchive,
  })
  const { data: archivedNodes, isLoading: archivedLoading, isError: archivedError, refetch: refetchArchived } = useArchivedNodes({
    enabled: isArchive,
  })
  // FR-486：正在启动的 Worker 进度。单独取数——它们尚未注册，不在 nodes 列表里。
  // 归档视图不取（归档页与「谁正在启动」无关，多一个轮询没意义）。
  const { data: startupProgress, isLoading: startupLoading } = useNodeStartupProgress({ enabled: !isArchive })
  // 各节点实例数走服务端聚合（FR-247/FR-270）：byNode 每节点计数已在服务端算好，
  // 本页不再全量拉取实例再前端归并。
  //
  // 归档视图用不到实例数（归档列表只显示名称/主机/下线时间），故按视图门控取数：
  // 原先无条件请求，切到「归档」仍照发一次 /instances/aggregate。
  const { data: aggregate } = useInstanceAggregate(undefined, !isArchive)

  const selectedId = (() => {
    const n = Number(searchParams.get('node'))
    return Number.isFinite(n) && n > 0 ? n : null
  })()
  const tab = readDetailTab(searchParams)
  const [query, setQuery] = useState('')
  const [pending, setPending] = useState<PendingAction | null>(null)
  // FR-309：下线被实例守卫 409 拒绝 → 清单模态；离线节点走强制下线需再过一道输入名称确认。
  const [conflict, setConflict] = useState<DeleteConflict | null>(null)
  const [forcePending, setForcePending] = useState<DeleteConflict | null>(null)
  // FR-394：归档清理确认 / 实例守卫 / force 确认。
  const [purgeTarget, setPurgeTarget] = useState<ArchivedNode | null>(null)
  const [purgeConflict, setPurgeConflict] = useState<PurgeConflict | null>(null)
  const [forcePurgePending, setForcePurgePending] = useState<PurgeConflict | null>(null)
  const [addOpen, setAddOpen] = useState(false)

  // 选中节点写入 URL（保留当前 tab/view 等其它参数）。
  const setSelectedId = (id: number) => {
    const next = new URLSearchParams(searchParams)
    next.set('node', String(id))
    setSearchParams(next)
  }
  // 切换激活分段写入 URL（默认 overview 省略，保持链接简洁）。
  const setTab = (next: DetailTab) => {
    const params = new URLSearchParams(searchParams)
    if (next === 'overview') params.delete('tab')
    else params.set('tab', next)
    setSearchParams(params)
  }
  const setMaintenance = useSetNodeMaintenance()
  const drain = useDrainNode()
  const del = useDeleteNode()
  const purge = usePurgeArchivedNode()

  // 集群汇总（FR-144）：在线/离线/维护计数 + 在线节点资源水位均值。
  const summary = useMemo(() => summarizeNodes(nodes ?? []), [nodes])
  // 各节点实例数（服务端聚合 byNode，列表/详情共用）。
  const instanceCountByNode = useMemo(() => {
    const map = new Map<number, number>()
    for (const { nodeId, count } of aggregate?.byNode ?? []) map.set(nodeId, count)
    return map
  }, [aggregate])

  const layout = readNodesLayout(searchParams)

  /**
   * 列顺序（表头拖拽换位，用户要求「表头参考服务器列表可以拖拽」）。
   * 与实例页同一套实现（lib/shared/column-order），但列定义独立（jm.nodes.columnOrder）。
   */
  const [columnOrder, setColumnOrder] = useState<NodeColumnKey[]>(readStoredNodeOrder)
  const reorderColumn = (from: NodeColumnKey, to: NodeColumnKey) => {
    setColumnOrder((prev) => {
      const next = applyNodeColumnMove(prev, from, to)
      writeStoredNodeOrder(next)
      return next
    })
  }

  /**
   * 分页（用户要求「底部分页」）。走 URL，与服务器页同款语义：可分享、刷新保持。
   * 节点数量通常很少，但「分页」保证了节点规模上来后不会一次性铺满整页。
   */
  /**
   * 状态筛选（用户提出）。走 URL 与其他筛选保持一致：可分享、刷新保持。
   * 与搜索词是 AND 关系（见 filterNodes）。
   */
  const statusFilter = (searchParams.get('status') as NodeStatusFilter | null) ?? 'all'
  const setStatusFilter = (next: NodeStatusFilter) => {
    const params = new URLSearchParams(searchParams)
    if (next === 'all') params.delete('status')
    else params.set('status', next)
    // 换筛选后回到第 1 页：否则可能停在一个已不存在于新结果集的页码上。
    params.delete('page')
    setSearchParams(params, { replace: true })
  }

  /**
   * 动态分页：页大小 = 表格当前「一屏能放几行」，翻页即换屏。
   * 固定行数在两种情况下都不对：节点少时容器铺满但只放两行；小屏时一页放不下要滚。
   */
  const nodesTableRef = useRef<HTMLDivElement | null>(null)
  const dynamicPageSize = useDynamicPageSize({
    containerRef: nodesTableRef,
    // 实测：单行 33px、表头 36px；分页条在容器外故传 0。
    rowHeight: 33,
    headerHeight: 36,
    footerHeight: 0,
  })
  const pageSize = Number(searchParams.get('size')) || dynamicPageSize
  const page = Math.max(1, Number(searchParams.get('page')) || 1)
  const setPage = (next: number) => {
    const params = new URLSearchParams(searchParams)
    params.set('page', String(next))
    setSearchParams(params, { replace: true })
  }

  /**
   * 排序状态（用户可点表头切换）。
   *
   * 走 URL（`?sort=&order=`）而不是组件 state：与服务器页一致，链接可分享、刷新不丢。
   * 只对列表视图有意义——卡片视图是「概览」而非「比较」，不做排序。
   */
  const sortKey = (searchParams.get('sort') as NodeSortKey | null) ?? null
  const sortOrder = (searchParams.get('order') as NodeSortOrder | null) ?? 'asc'
  const setSort = (key: NodeSortKey) => {
    const next = new URLSearchParams(searchParams)
    // 再点同一列切换方向；换列时从升序开始（与表格惯例一致）。
    if (sortKey === key && sortOrder === 'asc') next.set('order', 'desc')
    else {
      next.set('sort', key)
      next.set('order', 'asc')
    }
    if (sortKey !== key) next.set('sort', key)
    setSearchParams(next, { replace: true })
  }
  const setLayout = (next: NodesLayout) => {
    const params = new URLSearchParams(searchParams)
    params.set('layout', next)
    setSearchParams(params, { replace: true })
    try {
      localStorage.setItem(NODES_LAYOUT_KEY, next)
    } catch {
      /* 忽略：无存储能力时仅本次会话有效 */
    }
  }

  const filtered = useMemo(() => filterNodes(nodes ?? [], query, statusFilter), [nodes, query, statusFilter])
  /**
   * 排序后的节点列表（列表视图用）。
   *
   * 排序键与方向都来自 URL；未指定时保持接口返回顺序（后端已按在线/名称排过），
   * 不做「默认排序」——那会覆盖掉后端的合理默认。
   */
  const sortedNodes = useMemo(() => {
    if (!sortKey) return filtered
    const dir = sortOrder === 'desc' ? -1 : 1
    /*
      「异常优先」的权重：数值越小越靠前（升序即「问题最多的先出现」）。
      离线（最需要处理）→ 维护中 → CPU 高 → 其余。
      用 cpuUsage 作为第三层，是为了让「都正常的机器里最吃紧的」也排在前面。
    */
    const attentionWeight = (n: (typeof filtered)[number]): number => {
      if (n.status === 0) return 0 // 离线
      if (n.maintenance) return 1 // 维护中
      if ((n.cpuUsage ?? 0) >= 0.8) return 2 // 高负载
      return 3
    }
    const valueOf = (n: (typeof filtered)[number]): number | string => {
      switch (sortKey) {
        case 'attention':
          return attentionWeight(n)
        case 'name':
          return n.name
        case 'status':
          return n.status
        case 'cpu':
          return n.cpuUsage ?? 0
        case 'memory':
          return n.memoryUsage ?? 0
        case 'disk':
          return n.diskUsage ?? 0
        case 'instances':
          return instanceCountByNode.get(n.id) ?? 0
      }
    }
    return [...filtered].sort((a, b) => {
      const va = valueOf(a)
      const vb = valueOf(b)
      if (typeof va === 'string' || typeof vb === 'string') {
        return String(va).localeCompare(String(vb), 'zh-CN') * dir
      }
      return (va - vb) * dir
    })
  }, [filtered, sortKey, sortOrder, instanceCountByNode])

  /** 当前页的节点（分页切片）。分页只影响渲染，不影响排序与统计。 */
  const pagedNodes = useMemo(
    () => sortedNodes.slice((page - 1) * pageSize, page * pageSize),
    [sortedNodes, page, pageSize],
  )

  const filteredArchived = useMemo(() => {
    const q = query.trim().toLowerCase()
    const list = archivedNodes ?? []
    if (!q) return list
    return list.filter((n) => n.name.toLowerCase().includes(q) || n.host.toLowerCase().includes(q))
  }, [archivedNodes, query])
  // 有效选中（FR-232 进入默认选第一个 + FR-177 幽灵选中回退）：基于搜索后的 filtered 派生——
  // 未显式选中/选中项不在筛选结果内 → 回退筛选结果第一个；搜索无匹配 → null（右栏落空态，不留旧详情）。
  // 派生而非用 effect 同步 state（避免 set-state-in-effect 级联；selectedId 仍保留用户最后点选）。
  const effectiveSelectedId = useMemo(() => {
    const pool = isArchive ? filteredArchived : filtered
    if (pool.length === 0) return null
    if (selectedId !== null && pool.some((n) => n.id === selectedId)) return selectedId
    // pool 非空（上方已排除空池），首个元素必存在；判空仅用于类型收窄。
    return pool[0]?.id ?? null
  }, [filtered, filteredArchived, isArchive, selectedId])
  // 选中节点解析为实时列表对象（节点下线→回退第一个，右栏随轮询刷新而非陈旧快照）。
  const selected = useMemo(
    () => (isArchive ? null : resolveSelectedNode(filtered, effectiveSelectedId)),
    [filtered, effectiveSelectedId, isArchive],
  )
  const selectedArchived = useMemo(() => {
    if (!isArchive || effectiveSelectedId === null) return null
    return filteredArchived.find((n) => n.id === effectiveSelectedId) ?? null
  }, [filteredArchived, effectiveSelectedId, isArchive])


  const [maintenanceTarget, setMaintenanceTarget] = useState<NodeInfo | null>(null)

  const runMaintenance = (node: NodeInfo, enabled: boolean) => {
    setMaintenance.mutate(
      { id: node.id, enabled },
      {
        onSuccess: () =>
          toast.success(enabled ? t('nodes.maintenanceEnabled') : t('nodes.maintenanceDisabled')),
        onError: (e: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(e?.response?.data?.message || t('common.error')),
      },
    )
  }

  // 进入维护会中断新实例调度（不影响运行实例、可退出回退），故进入方向加二次确认；退出无害直接执行。
  const toggleMaintenance = (node: NodeInfo) => {
    if (!node.maintenance) {
      setMaintenanceTarget(node)
      return
    }
    runMaintenance(node, false)
  }

  const confirmMaintenance = () => {
    if (!maintenanceTarget) return
    runMaintenance(maintenanceTarget, true)
    setMaintenanceTarget(null)
  }

  const confirmPending = () => {
    if (!pending) return
    const { kind, node } = pending
    setPending(null)
    if (kind === 'drain') {
      drain.mutate(node.id, {
        onSuccess: (res) => toast.success(t('nodes.drainDone', { count: res.data.stoppedCount })),
        onError: (e: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(e?.response?.data?.message || t('common.error')),
      })
    } else {
      del.mutate({ id: node.id }, {
        onSuccess: () => toast.success(t('nodes.deleted')),
        onError: (e: Error & { response?: { status?: number; data?: { error?: string; message?: string; instances?: NodeDeleteBlockedInstance[] } } }) => {
          // FR-309：名下有实例被守卫拒绝 → 弹实例清单模态（离线节点内含强制下线入口）。
          if (e?.response?.status === 409 && e.response.data?.error === 'NODE_HAS_INSTANCES') {
            setConflict({ node, instances: e.response.data.instances ?? [] })
            return
          }
          toast.error(e?.response?.data?.message || t('common.error'))
        },
      })
    }
  }

  // FR-309 强制下线（仅离线节点）：级联删除名下实例的平台记录，明示不清理远端文件。
  const confirmForceDelete = () => {
    if (!forcePending) return
    const { node } = forcePending
    setForcePending(null)
    del.mutate({ id: node.id, force: true }, {
      onSuccess: (res) => toast.success(t('nodes.forceDeleted', { count: res.data.instancesPurged })),
      onError: (e: Error & { response?: { data?: { message?: string } } }) =>
        toast.error(e?.response?.data?.message || t('common.error')),
    })
  }

  // FR-394：归档清理（无 force）；409 → 实例清单 → force DangerConfirm。
  const confirmPurge = () => {
    if (!purgeTarget) return
    const node = purgeTarget
    setPurgeTarget(null)
    purge.mutate(
      { id: node.id },
      {
        onSuccess: () => toast.success(t('nodes.purged')),
        onError: (e: Error & { response?: { status?: number; data?: { error?: string; message?: string; instances?: NodeDeleteBlockedInstance[] } } }) => {
          if (e?.response?.status === 409 && e.response.data?.error === 'NODE_HAS_INSTANCES') {
            setPurgeConflict({ node, instances: e.response.data.instances ?? [] })
            return
          }
          toast.error(e?.response?.data?.message || t('common.error'))
        },
      },
    )
  }

  const confirmForcePurge = () => {
    if (!forcePurgePending) return
    const { node } = forcePurgePending
    setForcePurgePending(null)
    purge.mutate(
      { id: node.id, force: true },
      {
        onSuccess: (res) => toast.success(t('nodes.forcePurged', { count: res.data.instancesPurged })),
        onError: (e: Error & { response?: { data?: { message?: string } } }) =>
          toast.error(e?.response?.data?.message || t('common.error')),
      },
    )
  }

  const summaryChips: SummaryChip[] = [
    { key: 'online', label: t('nodes.online'), count: summary.online, level: 'success', breathing: summary.online > 0 },
    { key: 'offline', label: t('nodes.offline'), count: summary.offline, level: 'danger' },
    { key: 'maintenance', label: t('nodes.maintenance'), count: summary.maintenance, level: 'warning' },
  ]
  const gauge = (pct: number | null) => (pct === null ? '--' : `${pct.toFixed(0)}%`)

  /**
   * KPI 大卡片（用户要求「和服务器列表一样，顶部 KPI 大卡片」）。
   *
   * 与服务器页同一套 `StatCard` + auto-fit 网格：数字优先于文字——
   * 「几台在跑、几台挂了、集群还剩多少资源」是进这页最先要回答的三个问题。
   * 前三张点一下就按状态筛（与旧 SummaryChips 行为一致），后三张是集群资源水位，不可点。
   */
  const nodeStatCards: Array<{
    key: string
    label: string
    value: string
    tone: 'neutral' | 'success' | 'danger' | 'warning' | 'info'
    onClick?: () => void
    active?: boolean
  }> = [
    {
      key: 'total',
      label: t('nodes.total', '节点总数'),
      value: summary.total.toLocaleString('zh-CN'),
      tone: 'neutral',
      // 点「总数」= 取消状态筛选，回到全部（等价于服务器页的「全部」卡）。
      onClick: () => setStatusFilter('all'),
      active: statusFilter === 'all',
    },
    {
      key: 'online',
      label: t('nodes.online'),
      value: summary.online.toLocaleString('zh-CN'),
      tone: 'success',
      // 点 KPI 卡即按该状态筛（与服务器页 StatCard 同一手感）；再点一次取消。
      onClick: () => setStatusFilter(statusFilter === 'online' ? 'all' : 'online'),
      active: statusFilter === 'online',
    },
    {
      key: 'offline',
      label: t('nodes.offline'),
      value: summary.offline.toLocaleString('zh-CN'),
      tone: summary.offline > 0 ? 'danger' : 'neutral',
      onClick: () => setStatusFilter(statusFilter === 'offline' ? 'all' : 'offline'),
      active: statusFilter === 'offline',
    },
    {
      key: 'maintenance',
      label: t('nodes.maintenance'),
      value: summary.maintenance.toLocaleString('zh-CN'),
      tone: summary.maintenance > 0 ? 'warning' : 'neutral',
      onClick: () => setStatusFilter(statusFilter === 'maintenance' ? 'all' : 'maintenance'),
      active: statusFilter === 'maintenance',
    },
    {
      key: 'cpu',
      label: t('nodes.avgCpu', '平均 CPU'),
      value: gauge(summary.cpuPct),
      tone: 'info',
    },
    {
      key: 'mem',
      label: t('nodes.avgMem', '平均内存'),
      value: gauge(summary.memPct),
      tone: 'info',
    },
    {
      key: 'disk',
      label: t('nodes.avgDisk', '平均磁盘'),
      value: gauge(summary.diskPct),
      tone: 'info',
    },
  ]
  const listLoading = isArchive ? archivedLoading : isLoading
  /**
   * 列表取数失败（FR-201 同款纪律）：不判 `isError` 时失败会渲染成「暂无节点」空态，
   * 运营看到的是「节点都没了」而不是「这次没拉到」，据此误判集群已空。
   * 错误态与空态必须分开，且给出重试入口（查询自带 30s 轮询，但显式重试更快）。
   */
  const listError = isArchive ? archivedError : nodesError
  const retryList = () => void (isArchive ? refetchArchived() : refetchNodes())

  // 阶段 6 页面重做（照原型 `nodesPage()`）：本页拆成**列表态**与**详情态**。
  //
  // 判据用 `selectedId` —— 它就是「URL 里有没有 node 参数」（null 即没有）。
  // 不能用 `effectiveSelectedId`：后者在无参数时会回退到第一个节点，
  // 那样列表态会错误地自动选中一个，用户以为进了详情。
  if (selectedId === null) {
    // flex 列 + min-h-0：让第三段（表格）用 `flex-1` 吃掉剩余高度，
    // 这样表格是「铺满」的而不是「内容多高就多高、下面留白」。
    return (
      <PageShell variant="fixed" data-page="nodes" className="flex min-h-0 flex-col gap-3">
        <PageHeader
          title={t('nodes.title')}
          description={t('nodes.subtitle')}
          actions={
            <Button onClick={() => setAddOpen(true)}>
              <Plus className="size-4" />
              {t('nodes.enroll.addNode')}
            </Button>
          }
        />
        {/* 作用域条：状态计数即本页范围入口（点它按状态筛）+ 本地搜索（原型同款，
            搜索框在 scope-bar 内而不是页头操作区）。 */}
        {/*
          三段式（用户指定）：
          ① 顶部 KPI 大卡片  ② 节点运行概览图  ③ 铺满的节点表格
          原先是「一排 chip + 卡片网格」，2 个节点时页面约 62% 是空白。
        */}
        <div
          role="group"
          aria-label={t('nodes.summaryLabel', '节点概览')}
          /*
            7 张卡一行排下：可用宽度约 1040px（1280 视口减侧栏与内边距），
            (1040 - 6×gap 10) / 7 ≈ 139px。取 minmax(120px,1fr) 留余量——
            原先写 130px 时第 7 张被挤到第二行，白吃掉整行高度。
            窄屏由 auto-fit 自动降列，不会溢出。
          */
          className="grid gap-2.5 [grid-template-columns:repeat(auto-fit,minmax(120px,1fr))]"
        >
          {nodeStatCards.map((card) => (
            <div key={card.key} data-testid={`nodes-kpi-${card.key}`}>
            <StatCard
              label={card.label}
              value={card.value}
              tone={card.tone}
              {...(card.onClick ? { onClick: card.onClick, active: card.active } : {})}
            />
            </div>
          ))}
        </div>

        {/* ② 节点运行概览：集群平均水位 + 各节点负载对比（零额外请求，见组件内注释）。 */}
        {!isArchive && <NodeRuntimeOverview nodes={nodes ?? []} summary={summary} />}

        <ScopeBar>
          <SummaryChips chips={summaryChips} />
          {/*
            视图切换：列表（默认）/ 卡片。
            列表是「比较与巡检」的视角（列对齐、一屏多行），卡片是「少量节点时看概览」。
            选择写入 localStorage 并同步 URL，刷新与分享都保持。
          */}
          {/*
            「异常优先」快捷开关（用户提出「节点多了要能快速定位问题」）：
            一键把离线/维护/高负载排到最前，而不是让用户去表头里翻排序方向。
            等价于 sort=attention&order=asc，URL 可分享。
          */}
          <Button
            type="button"
            variant={sortKey === 'attention' ? 'default' : 'outline'}
            size="sm"
            onClick={() => {
              const params = new URLSearchParams(searchParams)
              if (sortKey === 'attention') {
                params.delete('sort')
                params.delete('order')
              } else {
                params.set('sort', 'attention')
                params.set('order', 'asc')
              }
              params.delete('page')
              setSearchParams(params, { replace: true })
            }}
            aria-pressed={sortKey === 'attention'}
            title={t('nodes.attentionSortHint', '把离线 / 维护中 / 高负载的节点排到最前')}
          >
            {t('nodes.attentionSort', '异常优先')}
          </Button>

          <Segments role="tablist" aria-label={t('nodes.layoutSwitch', '视图切换')}>
            <Segment active={layout === 'list'} onClick={() => setLayout('list')} role="tab" aria-selected={layout === 'list'}>
              {t('nodes.layoutList', '列表')}
            </Segment>
            <Segment active={layout === 'card'} onClick={() => setLayout('card')} role="tab" aria-selected={layout === 'card'}>
              {t('nodes.layoutCard', '卡片')}
            </Segment>
          </Segments>
          <div className="relative ml-auto">
            <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t('nodes.searchPlaceholder')}
              aria-label={t('nodes.searchPlaceholder')}
              className="h-8 w-56 pl-7"
            />
          </div>
        </ScopeBar>
        {/* FR-486：正在启动的 Worker。放在列表之上，因为它解释「为什么列表里还没有这台机器」——
            旧事故中 8m39s 的启动期里主控台完全看不到该节点，运维无从判断是没装好还是正在加载。 */}
        {!isArchive && (
          <NodeStartupProgressPanel items={startupProgress ?? []} loading={startupLoading} />
        )}
        {listLoading ? (
          <DataPanelSkeleton />
        ) : listError ? (
          <NodeListErrorState onRetry={retryList} />
        ) : (
          layout === 'card' ? (
            <CardsGrid className="min-h-0 flex-1">
              {sortedNodes.map((node) => (
                <NodeCard
                  key={node.id}
                  node={node}
                  instanceCount={instanceCountByNode.get(node.id) ?? 0}
                  onOpen={() => setSelectedId(node.id)}
                />
              ))}
            </CardsGrid>
          ) : (
            <NodeTable
              nodes={pagedNodes}
              instanceCountByNode={instanceCountByNode}
              sortKey={sortKey}
              sortOrder={sortOrder}
              onSort={setSort}
              onOpen={(id) => setSelectedId(id)}
              columnOrder={columnOrder}
              onReorderColumn={reorderColumn}
              containerRef={nodesTableRef}
              pagination={{
                page,
                pageSize,
                total: sortedNodes.length,
                onPageChange: setPage,
              }}
            />
          )
        )}
        <AddNodeDialogContainer open={addOpen} onClose={() => setAddOpen(false)} />
      </PageShell>
    )
  }

  return (
    // 全量对齐：详情态双栏（左栏节点列表 + 右栏详情）改用布局层 PageShell 的 fixed 壳态
    // ——固定视口、内部区域自行滚动，与列表态共用同一套留白与纵向节奏。
    // 原为手写骨架：jm-page-stack + h-[calc(100vh-8.25rem)] 硬编码视口高度 + 自定 gap。
    <PageShell variant="fixed" data-page="nodes" className="gap-3 lg:flex-row">
      {/*
        节点列表已从详情态移除（用户决定）：它与列表页功能重叠——
        列表页有完整的筛选/排序/分页，这里再放一份只有名字+状态点的列表，
        等于同一批数据展示两遍，还占掉内容区约 288px。
        切换节点改为顶部下拉选择器（见 NodeDetailPane 的 nodeSelector 插槽），
        与云控制台「先选目标、再看详情」的动线一致。
      */}

      {/* 右栏：活跃详情 / 归档只读详情（FR-393） */}
      {/* 阶段 6 第二步：详情态改用 PageShell 的 tool 变体。
          它原是裸 <section>——那时左栏列表与它同处一个 flex-row；两态分离后右栏独占整页，
          滚动收口到它自身（tool 变体是 gap-0 p-0，不叠加默认留白）。 */}
      <PageShell variant="tool" className="min-h-0 min-w-0 flex-1 overflow-y-auto">
        {isArchive ? (
          selectedArchived ? (
            <ArchivedNodeDetailPane
              key={selectedArchived.id}
              node={selectedArchived}
              onPurge={() => setPurgeTarget(selectedArchived)}
              purging={purge.isPending}
              onNavigate={navigate}
            />
          ) : (
            <div className="grid h-full place-items-center rounded-lg border border-dashed bg-card/50 shadow-soft">
              <div className="flex flex-col items-center gap-2 text-center text-muted-foreground">
                <Server className="size-8 opacity-40" />
                <p className="text-sm">{t('nodes.archiveSelectHint')}</p>
              </div>
            </div>
          )
        ) : selected ? (
          <NodeDetailPane
            key={selected.id}
            node={selected}
            instanceCount={instanceCountByNode.get(selected.id) ?? 0}
            tab={tab}
            onTab={setTab}
            onToggleMaintenance={() => toggleMaintenance(selected)}
            onDrain={() => setPending({ kind: 'drain', node: selected })}
            onDelete={() => setPending({ kind: 'delete', node: selected })}
            onNavigate={navigate}
            /* 顶部下拉选择器替代原左栏列表：切换节点不必退回列表页。 */
            nodeSelector={
              <NodeSelector
                nodes={nodes ?? []}
                selectedId={selected.id}
                onSelect={(id) => setSelectedId(id)}
              />
            }
            renderTab={(tabKey, paneNode) => (
              <>
                {tabKey === 'overview' && <NodeOverviewContainer node={paneNode} />}
                {/* 配置总览：汇总散落在各分区的可写配置 + 只读项，并给直达入口。 */}
                {tabKey === 'config' && <NodeConfigContainer node={paneNode} onTab={setTab} />}
                {/*
                  每个页签都由外层 Panel 提供标题与边框（ports/proxy/probe 早就这么做了），
                  否则切页签时有的分区有标题栏、有的像裸内容，用户会丢失「我在哪一区」的锚点。
                  面板内部不再重复渲染同名标题。
                */}
                {tabKey === 'runtime' && (
                  <div className="space-y-4">
                    <Panel title={t('nodes.jdkTitle')}>
                      <NodeJDKTab nodeId={paneNode.id} active />
                    </Panel>
                    <Panel title={t('logsRuntime.title')}>
                      <NodeLogRuntimeTab
                        nodeId={paneNode.id}
                        os={paneNode.os}
                        arch={paneNode.arch}
                        online={paneNode.status === 1}
                      />
                    </Panel>
                  </div>
                )}
                {tabKey === 'cache' && (
                  <Panel title={t('artifactCache.title')}>
                    <NodeArtifactCacheTab nodeId={paneNode.id} />
                  </Panel>
                )}
                {tabKey === 'process' && <NodeProcessTab node={paneNode} />}
                {tabKey === 'proxy' && (
                  <Panel title={t('nodeProxy.title')}>
                    <NodeProxyTab nodeId={paneNode.id} />
                  </Panel>
                )}
                {tabKey === 'probe' && (
                  <Panel title={t('probe.nodeVersionTitle')}>
                    <NodeProbeVersionTab nodeId={paneNode.id} />
                  </Panel>
                )}
                {tabKey === 'monitor' && <NodeMonitorChartsContainer node={paneNode} />}
                {tabKey === 'repair' && (
                  <Panel title={t('nodeRepair.title')}>
                    <NodeRepairTab node={paneNode} active />
                  </Panel>
                )}
              </>
            )}
          />
        ) : (
          <div className="grid h-full place-items-center rounded-lg border border-dashed bg-card/50 shadow-soft">
            <div className="flex flex-col items-center gap-2 text-center text-muted-foreground">
              <Server className="size-8 opacity-40" />
              <p className="text-sm">{t('nodes.selectHint')}</p>
            </div>
          </div>
        )}
      </PageShell>

      <AddNodeDialogContainer open={addOpen} onClose={() => setAddOpen(false)} />
      <DangerConfirm
        open={pending !== null}
        title={pending?.kind === 'drain' ? t('nodes.drainConfirmTitle') : t('nodes.deleteConfirmTitle')}
        description={
          pending?.kind === 'drain'
            ? t('nodes.drainConfirmDesc', { name: pending?.node.name })
            : t('nodes.deleteConfirmDesc', { name: pending?.node.name })
        }
        confirmLabel={pending?.kind === 'drain' ? t('nodes.drain') : t('nodes.delete')}
        confirmText={pending?.kind === 'delete' ? pending?.node.name : undefined}
        scope="platform"
        onConfirm={confirmPending}
        onCancel={() => setPending(null)}
      />
      {/* FR-309：下线被实例守卫拒绝的清单模态 + 离线节点强制下线确认（输入名称）。 */}
      <BlockedByInstancesDialog
        conflict={conflict}
        title={t('nodes.deleteBlockedTitle')}
        description={t('nodes.deleteBlockedDesc', { name: conflict?.node.name, count: conflict?.instances.length })}
        force={
          conflict && conflict.node.status !== 1
            ? { label: t('nodes.forceDelete'), hint: t('nodes.deleteBlockedForceHint') }
            : null
        }
        onClose={() => setConflict(null)}
        onForce={() => {
          setForcePending(conflict)
          setConflict(null)
        }}
      />
      <DangerConfirm
        open={forcePending !== null}
        title={t('nodes.forceDeleteConfirmTitle')}
        description={t('nodes.forceDeleteConfirmDesc', {
          name: forcePending?.node.name,
          count: forcePending?.instances.length,
        })}
        confirmLabel={t('nodes.forceDelete')}
        confirmText={forcePending?.node.name}
        scope="platform"
        onConfirm={confirmForceDelete}
        onCancel={() => setForcePending(null)}
      />
      <DangerConfirm
        open={maintenanceTarget !== null}
        title={t('nodes.maintenanceConfirmTitle')}
        description={t('nodes.maintenanceConfirmDesc', { name: maintenanceTarget?.name ?? '' })}
        confirmLabel={t('nodes.enterMaintenance')}
        pending={setMaintenance.isPending}
        onConfirm={confirmMaintenance}
        onCancel={() => setMaintenanceTarget(null)}
      />
      {/* FR-394：归档清理确认 + 实例守卫 + force 确认（文案明示不清理远端文件）。 */}
      <DangerConfirm
        open={purgeTarget !== null}
        title={t('nodes.purgeConfirmTitle')}
        description={t('nodes.purgeConfirmDesc', { name: purgeTarget?.name })}
        confirmLabel={t('nodes.purge')}
        confirmText={purgeTarget?.name}
        scope="platform"
        pending={purge.isPending}
        onConfirm={confirmPurge}
        onCancel={() => setPurgeTarget(null)}
      />
      <BlockedByInstancesDialog
        conflict={purgeConflict}
        title={t('nodes.purgeBlockedTitle')}
        description={t('nodes.purgeBlockedDesc', { name: purgeConflict?.node.name, count: purgeConflict?.instances.length })}
        force={{ label: t('nodes.forcePurge'), hint: t('nodes.purgeBlockedForceHint') }}
        onClose={() => setPurgeConflict(null)}
        onForce={() => {
          setForcePurgePending(purgeConflict)
          setPurgeConflict(null)
        }}
      />
      <DangerConfirm
        open={forcePurgePending !== null}
        title={t('nodes.forcePurgeConfirmTitle')}
        description={t('nodes.forcePurgeConfirmDesc', {
          name: forcePurgePending?.node.name,
          count: forcePurgePending?.instances.length,
        })}
        confirmLabel={t('nodes.forcePurge')}
        confirmText={forcePurgePending?.node.name}
        scope="platform"
        pending={purge.isPending}
        onConfirm={confirmForcePurge}
        onCancel={() => setForcePurgePending(null)}
      />
    </PageShell>
  )
}



/** 右栏详情主体：身份块 + 资源仪表 + 分段 Tabs（切段稳定工具条，布局不重组）。 */

/**
 * 节点列表取数失败态。
 *
 * 与「暂无节点」空态严格区分：`/nodes` 失败时若仍渲染空态，运营看到的是
 * 「节点都没了」——这是会直接导致误判集群规模的假信号。文案复用既有键
 * （`common.error` + `common.refresh`），不新增 i18n 键。
 */
function NodeListErrorState({ onRetry, compact = false }: { onRetry: () => void; compact?: boolean }) {
  const { t } = useTranslation()
  return (
    <div
      role="alert"
      data-testid="nodes-list-error"
      className={cn(
        'flex items-center gap-2 px-2 py-4 text-sm text-destructive',
        !compact && 'rounded-lg border border-destructive/30 bg-destructive/5',
      )}
    >
      <span>{t('common.error')}</span>
      <Button size="sm" variant="outline" onClick={onRetry}>
        {t('common.refresh')}
      </Button>
    </div>
  )
}

/** 从 mutation 错误里取后端消息，缺省回落到兜底文案。 */
function errMessage(err: unknown, fallback: string): string {
  const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
  return msg || fallback
}

/**
 * 「配置」页签：把四个可配置项的**当前生效值**取回来注入视图。
 *
 * 【为什么要单独取】这些值分散在四个专用端点（代理 / 制品缓存 / 包管理器 / 探针版本），
 * 而「配置总览」的用途正是「一眼看到现在是什么值、去哪儿改」。不取的话这一页只能写
 * 「见「XX」分区」，等于让用户再点四次。四个 hook 都是既有实现，命中缓存时几乎零成本。
 */
function NodeConfigContainer({ node, onTab }: { node: NodeInfo; onTab: (tab: DetailTab) => void }) {
  const { t } = useTranslation()
  /*
    这四项都是 **CP 侧记录**，节点离线也照样能读——
    离线时正是最需要看「它当前配置是什么、有哪些改动还没下发」的时候。
    （先前用 `enabled: online` 把它们一起关掉，离线节点整页退化成「见「XX」分区」。）
  */
  const { data: proxy } = useNodeProxy(node.id)
  const { data: cache } = useArtifactCache(node.id, { enabled: true })
  const { data: pm } = useNodePMConfig(node.id)
  const { data: probeSelection } = useNodeProbeVersion(node.id)
  const { data: probeCatalog } = useServerProbeCatalog()

  /*
    最近一次配置类变更：审计日志按 node 目标查一条即可覆盖整页四行。
    只看这几类动作（改代理/缓存上限/包管理器/探针版本），避免把「进入维护」也算进「配置变更」。
  */
  const { data: auditPage } = useAuditLogs({ targetType: 'node', targetId: String(node.id) })
  const CONFIG_ACTIONS = new Set([
    'node.proxy.set',
    'node.artifact_cache.set_cap',
    'node.pm_config.set',
    'node.probe_version.set',
  ])
  const recentEvent = auditPage?.pages
    .flatMap((p) => p.items)
    .find((e) => CONFIG_ACTIONS.has(e.action))

  // 内联快改的三个写入：复用各分区既有 mutation（不另写写入逻辑），成功后由 hook 自己失效缓存。
  const setCap = useSetArtifactCacheCap(node.id)
  const setProbe = useSetNodeProbeVersion(node.id)
  const setProxy = useUpdateNodeProxy(node.id)
  const savingInline = setCap.isPending || setProbe.isPending || setProxy.isPending

  const probeVersionId = probeSelection?.versionId ?? 0
  const pinned = probeCatalog?.versions.find((v) => v.id === probeVersionId)
  const globalDefault = probeCatalog?.versions.find((v) => v.id === probeCatalog?.package.defaultVersionId)

  return (
    <NodeConfigSection
      node={node}
      onTab={onTab}
      currentValues={{
        ...(proxy
          ? {
              proxy: {
                mode: proxy.mode,
                effectiveUrl: proxy.effectiveUrl,
                globalDefaultUrl: proxy.globalDefaultUrl,
                // 代理经心跳下发：节点离线即「待下发」。
                pending: !proxy.online,
              },
            }
          : {}),
        ...(cache ? { cacheCapBytes: cache.capBytes } : {}),
        ...(pm ? { pm: { pm: pm.pm, pmVersion: pm.pmVersion, registryCount: pm.registries.length } } : {}),
        ...(probeCatalog
          ? {
              probe: {
                inherited: probeVersionId === 0,
                version: (probeVersionId === 0 ? globalDefault?.version : pinned?.version) ?? '',
              },
            }
          : {}),
      }}
      onSaveCacheCap={async (capBytes) => {
        try {
          await setCap.mutateAsync(capBytes)
          toast.success(t('artifactCache.capSaved', { cap: describeCap(capBytes) }))
          return true
        } catch (err) {
          toast.error(errMessage(err, t('artifactCache.capFailed')))
          return false
        }
      }}
      onSaveProbeVersion={async (versionId) => {
        try {
          await setProbe.mutateAsync(versionId)
          toast.success(t('probe.nodeVersionSaved'))
          return true
        } catch (err) {
          toast.error(errMessage(err, t('probe.nodeVersionSaveFailed')))
          return false
        }
      }}
      onSaveProxy={async (body) => {
        try {
          await setProxy.mutateAsync(body)
          toast.success(t('nodeProxy.saved'))
          return true
        } catch (err) {
          toast.error(errMessage(err, t('nodeProxy.saveFailed')))
          return false
        }
      }}
      {...(probeCatalog
        ? {
            probeOptions: probeCatalog.versions.filter((v) => v.assetId > 0).map((v) => ({ id: v.id, version: v.version })),
            probeDefaultVersion: probeCatalog.versions.find((v) => v.id === probeCatalog.package.defaultVersionId)?.version ?? '',
          }
        : {})}
      savingInline={savingInline}
      {...(recentEvent
        ? {
            recentChange: {
              action: recentEvent.action,
              actionLabel: t(`audit.actions.${recentEvent.action}`, { defaultValue: recentEvent.action }),
              username: recentEvent.user?.username ?? '',
              createdAt: recentEvent.createdAt,
            },
          }
        : {})}
    />
  )
}

/**
 * 概览页签的取数接线层：节点规格清单 + 本节点实例汇总。
 *
 * 【为什么概览要取实例】清单只有规格与水位，1440×900 下剩余约 300px 是空白；
 * 实例汇总既填掉空白，也回答了「这台机器上跑着什么」。
 * 汇总走 `/instances/aggregate`（一次聚合，按 nodeId 过滤），列表取前 8 台按名称升序。
 */
function NodeOverviewContainer({ node }: { node: NodeInfo }) {
  const navigate = useNavigate()
  const list = useNodeInstanceListData(node.id)
  return (
    <NodeOverviewSection
      node={node}
      instances={list}
      // 「查看全部」去实例列表页（按该节点过滤）——不是切到本页的对比图页签。
      onOpenInstances={() => navigate(`/instances?nodeId=${node.id}`)}
      onOpenInstance={(id) => navigate(`/instances/${id}`)}
    />
  )
}

function useNodeInstanceListData(nodeId: number) {
  const { data: agg, isLoading: aggLoading, isError: aggError } = useInstanceAggregate({ nodeId })
  // 状态筛选走服务端：点「崩溃」时直接按 status 查询，不受条数上限影响。
  const [statusFilter, setStatusFilter] = useState<string | null>(null)
  const { data: page, isLoading: pageLoading, isError: pageError } = useInstanceSearch({
    nodeId,
    pageSize: 40,
    sort: 'name',
    order: 'asc',
    ...(statusFilter ? { status: statusFilter } : {}),
  })

  /*
    行序按「该先看谁」排：漂移 → 崩溃 → 启动中/停止中 → 运行 → 停止，同档按名称。
    服务端按名称排序时，崩溃实例会散落在几十行中间，等于没看出来。
  */
  const STATUS_PRIORITY: Record<string, number> = { CRASHED: 0, STARTING: 1, STOPPING: 2, RUNNING: 3, STOPPED: 4 }
  const items = [...(page?.items ?? [])].sort((a, b) => {
    const driftA = hasRuntimeDrift(a) ? -1 : 0
    const driftB = hasRuntimeDrift(b) ? -1 : 0
    if (driftA !== driftB) return driftA - driftB
    const pa = STATUS_PRIORITY[a.status] ?? 9
    const pb = STATUS_PRIORITY[b.status] ?? 9
    if (pa !== pb) return pa - pb
    return a.name.localeCompare(b.name)
  })

  const targetIds = items.map((i) => i.uuid)
  const { data: batch } = useMetricSeriesBatch({
    scope: 'instance',
    targetIds,
    range: '1h',
    metrics: ['inst_cpu_pct', 'inst_heap_used'],
  })
  const lastValue = (uuid: string, metricKey: string): number | undefined => {
    const series = batch?.series[uuid]?.find((x) => x.metricKey === metricKey && x.world === '')
    if (!series) return undefined
    for (let i = series.points.length - 1; i >= 0; i -= 1) {
      const p = series.points[i]
      if (p && p.avg != null) return p.avg
    }
    return undefined
  }
  const usage: NodeInstanceUsage = {}
  for (const it of items) {
    const cpu = lastValue(it.uuid, 'inst_cpu_pct')
    const heap = lastValue(it.uuid, 'inst_heap_used')
    usage[it.id] = { ...(cpu != null ? { cpuPct: cpu } : {}), ...(heap != null ? { heapBytes: heap } : {}) }
  }

  return {
    total: agg?.total ?? 0,
    byStatus: agg?.byStatus ?? {},
    items: items.map((i) => ({
      id: i.id,
      name: i.name,
      status: i.status,
      role: i.role,
      serverPort: i.serverPort,
      // 一行里只保留环境标签：其余自由标签在实例页有完整展块。
      envTags: parseTags(i.tags).filter((tag) => tag.startsWith('env:')),
      drift: hasRuntimeDrift(i),
      ...(i.statusReason ? { statusReason: i.statusReason } : {}),
    })),
    usage,
    isLoading: aggLoading || pageLoading,
    isError: aggError || pageError,
    activeStatus: statusFilter,
    onSelectStatus: setStatusFilter,
    // 超过一屏的台数只取前 40：如实说明，避免让人以为这节点只有 40 台。
    cappedHint: {
      shown: items.length,
      total: statusFilter ? (agg?.byStatus?.[statusFilter] ?? items.length) : (agg?.total ?? items.length),
    },
  }
}

/**
 * 「进程」页签：受管实例的进程清单 + 进程级处置 + 端口占用。
 *
 * 【为什么以进程为主语】实例级清单只回答「哪台挂了」；真正要处置的是「哪个 PID 在吃 CPU」。
 * 后端 `/metrics/processes/top` 只覆盖受管实例进程（游戏服本体及其包装器），
 * 因此处置前仍会按 instanceId + pid 二次校验归属（`/instances/:id/processes/:pid/detail`）。
 *
 * 【为什么把端口并进来】端口是「进程占用了什么」的一部分：同一台实例的 serverPort/queryPort
 * 与它的 PID 天然一一对应，分两个页签反而要来回切。
 */
function NodeProcessTab({ node }: { node: NodeInfo }) {
  const { t } = useTranslation()
  const [sort, setSort] = useState<ProcessSort>('cpu')
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState<ProcessTopItem | null>(null)
  const [pending, setPending] = useState<{ row: ProcessTopItem; action: ManagedProcessAction } | null>(null)

  // 节点离线时进程快照必然陈旧，干脆不取（空态会说明没有采样）。
  const online = node.status === 1
  const { data: top, isLoading, isError } = useProcessTop({
    nodeId: node.uuid,
    sort,
    limit: 100,
    enabled: online,
  })
  /*
    实例映射要尽量全：进程行按 instanceId 找名称/状态，取不满就会显示成 `#11` 这种
    本节点真实存在、但没进本页实例搜索窗口的 id。后端 pageSize 上限是 200，取满它。
  */
  const { data: search } = useInstanceSearch({ nodeId: node.id, pageSize: 200, sort: 'name', order: 'asc' })
  const instances = useMemo(() => {
    const map = new Map<number, ProcessInstanceRef>()
    for (const i of search?.items ?? []) map.set(i.id, { id: i.id, name: i.name, status: i.status, role: i.role })
    return map
  }, [search])

  const detail = useManagedProcessDetail(selected?.instanceId, selected?.pid, !!selected)
  const action = useManagedProcessAction()

  const runAction = async () => {
    if (!pending) return
    try {
      await action.mutateAsync({ instanceId: pending.row.instanceId, pid: pending.row.pid, action: pending.action })
      toast.success(t('nodes.processActionDone'))
      setPending(null)
      // 详情弹窗开着时同步刷新，避免用户看着过期数据再点一次。
      if (selected) void detail.refetch()
    } catch (err) {
      const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
      toast.error(msg || t('nodes.processActionFailed'))
      setPending(null)
    }
  }

  return (
    <div className="space-y-3">
      <NodeProcessPanel
        sort={sort}
        onSortChange={setSort}
        rows={top ?? []}
        instances={instances}
        isLoading={isLoading}
        isError={isError}
        query={query}
        onQueryChange={setQuery}
        onInspect={setSelected}
        onTerminate={(row) => setPending({ row, action: 'terminate' })}
        onKillTree={(row) => setPending({ row, action: 'kill_tree' })}
        {...(action.isPending && pending ? { pendingPid: pending.row.pid } : {})}
      />

      {/* 端口占用：原「端口」页签的内容，与进程同屏，减少来回切换。 */}
      <Panel title={t('ports.title')}>
        <NodePortsTab nodeId={node.id} />
      </Panel>

      <NodeProcessDetailDialog
        open={selected !== null}
        onOpenChange={(open) => {
          if (!open) setSelected(null)
        }}
        {...(detail.data ? { detail: detail.data } : {})}
        isLoading={detail.isLoading}
        isError={detail.isError}
        pending={action.isPending}
        onAction={(a) => {
          if (selected) setPending({ row: selected, action: a })
        }}
        {...(selected ? { fallbackName: instances.get(selected.instanceId)?.name ?? '' } : {})}
      />

      {/*
        处置二次确认：进程处置不可撤销，且「强杀进程树」会连子进程一起带走。
        确认文案里带上 PID 与实例名，避免在长清单里点错行。
      */}
      <DangerConfirm
        open={pending !== null}
        title={pending?.action === 'kill_tree' ? t('monitor.processKillTreeConfirm') : t('monitor.processTerminateConfirm')}
        description={`${t('nodes.processConfirmDesc', {
          pid: pending?.row.pid ?? '',
          name: pending ? (instances.get(pending.row.instanceId)?.name ?? `#${pending.row.instanceId}`) : '',
          action: pending?.action === 'terminate' ? t('monitor.processTerminate') : t('monitor.processKillTree'),
        })} ${t('nodes.processConfirmRootCaveat')}`}
        confirmLabel={pending?.action === 'kill_tree' ? t('monitor.processKillTree') : t('monitor.processTerminate')}
        scope="platform"
        onConfirm={() => void runAction()}
        onCancel={() => setPending(null)}
      />
    </div>
  )
}

/** 实例对比图的取数接线层（ADR-103）：视图已入包，此处注入服务端搜索与批量指标。 */
function NodeInstanceCompareContainer({ node }: { node: NodeInfo }) {
  const navigate = useNavigate()
  const { t } = useTranslation()
  // 清单与概览右栏同一份取数（聚合计数 + 分页清单 + CPU/堆），避免两处各写一遍。
  const list = useNodeInstanceListData(node.id)
  const [metric, setMetric] = useState('inst_tps')
  // 服务端按节点过滤分页取前 12（名称升序）；total 为该节点实例总数（提示中的 N）。
  const { data: search, isLoading, isError } = useInstanceSearch({
    nodeId: node.id,
    pageSize: COMPARE_TARGET_CAP,
    sort: 'name',
    order: 'asc',
  })
  const nodeInstances = search?.items ?? []
  const targetIds = nodeInstances.map((i) => i.uuid)
  // 单条批量查询替代逐实例 useQueries × N（FR-340）。
  const { data: batch } = useMetricSeriesBatch({ scope: 'instance', targetIds, range: '24h', metrics: [metric] })
  const series: ChartSeries[] = nodeInstances.map((inst) => {
    const s = batch?.series[inst.uuid]?.find((x) => x.metricKey === metric && x.world === '')
    return { key: inst.uuid, name: inst.name, points: (s?.points ?? []).map((p) => ({ ts: p.ts, value: p.avg })) }
  })
  return (
    <div className="space-y-3">
      {/*
        「实例」页签先给清单再给对比图：清单回答「这台机器上有哪些实例、谁出问题了」，
        对比图回答「谁快谁慢」。缺了清单时，看名字叫「实例」的页签却找不到实例。
      */}
      <Panel title={t('nodes.instanceList')}>
        <div className="flex max-h-[20rem] flex-col">
          <NodeInstanceList
            {...list}
            onOpenInstance={(id) => navigate(`/instances/${id}`)}
            testIdPrefix="node-instances"
          />
        </div>
      </Panel>
      <NodeInstanceCompare
        metric={metric}
        onMetricChange={setMetric}
        total={search?.total ?? 0}
        series={series}
        isLoading={isLoading}
        isError={isError}
      />
    </div>
  )
}

/**
 * 节点监控曲线的取数接线层（ADR-103）。
 *
 * 时间范围状态在这里：预设档位与自定义窗口互斥（见 `useNodeMetricRange`），
 * 任一变化都会改 queryKey 触发重新取数。
 */
function NodeMonitorChartsContainer({ node }: { node: NodeInfo }) {
  const rangeState = useNodeMetricRange()
  const { data, isFetching } = useMetricSeries({
    scope: 'node',
    targetId: node.uuid,
    range: rangeState.range,
    ...(rangeState.from && rangeState.to ? { from: rangeState.from, to: rangeState.to } : {}),
  })
  return (
    <div className="space-y-3">
      <NodeMonitorCharts
        range={{ ...rangeState, isFetching }}
        series={data?.series ?? []}
        nodeUuid={node.uuid}
        resolution={data?.resolution}
      />
      {/*
        实例指标对比并入监控页签：它回答的是「这些实例谁快谁慢」，
        与节点曲线同属「看趋势」；原先单开一个「实例」页签，页签名与内容也对不上。
      */}
      <NodeInstanceCompareContainer node={node} />
    </div>
  )
}

