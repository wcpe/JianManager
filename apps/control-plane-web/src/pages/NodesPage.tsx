import { useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  Box,
  ChevronsLeft,
  ChevronsRight,
  Plus,
  Search,
  Server,
} from 'lucide-react'
import {
  useNodes,
  useSetNodeMaintenance,
  useDrainNode,
  useDeleteNode,
  useArchivedNodes,
  usePurgeArchivedNode,
  type NodeInfo,
  type ArchivedNode,
  type NodeDeleteBlockedInstance,
} from '@/api/nodes'
import { useInstanceAggregate, useInstanceSearch } from '@/api/instances'
import { useMetricSeries, useMetricSeriesBatch } from '@/api/metrics'
import { Badge } from '@jianmanager/ui/components/badge'
import { ObjectPageHeader } from '@jianmanager/ui/components/shell'
import { Panel } from '@jianmanager/ui/components/panel'
import { Input } from '@jianmanager/ui/components/input'
import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { ResourceGauge } from '@jianmanager/ui/components/gauge'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { SummaryChips, type SummaryChip } from '@jianmanager/ui/components/summary-chips'
import { CardsGrid, DataPanelSkeleton, PageHeader, PageShell, ScopeBar, Segment, Segments } from '@jianmanager/ui/components/layout'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@jianmanager/ui/components/dropdown-menu'
import { TimeSeriesChart, type ChartSeries } from '@jianmanager/ui'
import { RangePicker, type MetricRange } from '@jianmanager/ui'
import { resourceLevel } from '@jianmanager/ui'
import { summarizeNodes } from '@/lib/node-summary'
import {
  nodeStatusLevel,
  filterNodes,
  resolveSelectedNode,
  loadNodeListCollapsed,
  persistNodeListCollapsed,
} from '@/lib/node-list'
import { cn } from '@jianmanager/ui'

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  scrollableDialogContentClass,
  ScrollableDialogBody,
} from '@jianmanager/ui/components/scrollable-dialog'
import NodeJDKTab from '@/components/nodes/NodeJDKTab'
import NodeLogRuntimeTab from '@/components/nodes/NodeLogRuntimeTab'
import NodePortsTab from '@/components/nodes/NodePortsTab'
import NodeArtifactCacheTab from '@/components/nodes/NodeArtifactCacheTab'
import NodeProxyTab from '@/components/nodes/NodeProxyTab'
import NodeProbeVersionTab from '@/components/nodes/NodeProbeVersionTab'
import NodeRepairTab from '@/components/nodes/NodeRepairTab'
import DangerConfirm from '@/components/DangerConfirm'
import AddNodeDialogContainer from '@/components/nodes/AddNodeDialogContainer'
import { Button } from '@jianmanager/ui/components/button'
import { BlockedByInstancesDialog } from '@jianmanager/ui/components/views/nodes/BlockedByInstancesDialog'
import {
  ArchivedNodeListRow,
  formatBytes,
  NodeActionsMenu,
  NodeCard,
  NodeListRow,
  NodeOverviewSection,
  NodeRailIcon,
} from '@jianmanager/ui/components/views/nodes/NodeListParts'
import { ArchivedNodeDetailPane } from '@jianmanager/ui/components/views/nodes/ArchivedNodeDetailPane'
import {
  COMPARE_TARGET_CAP,
  NodeInstanceCompare,
  NodeMonitorCharts,
} from '@jianmanager/ui/components/views/nodes/NodeCharts'
import {
  DETAIL_TABS,
  NodeDetailPane,
  type DetailTab,
} from '@jianmanager/ui/components/views/nodes/NodeDetailPane'


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
  const { data: nodes, isLoading } = useNodes({
    refetchInterval: 30_000,
    enabled: !isArchive,
  })
  const { data: archivedNodes, isLoading: archivedLoading } = useArchivedNodes({
    enabled: isArchive,
  })
  // 各节点实例数走服务端聚合（FR-247/FR-270）：byNode 每节点计数已在服务端算好，
  // 本页不再全量拉取实例再前端归并。
  const { data: aggregate } = useInstanceAggregate()

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
  // 切换活跃/归档视图（FR-393）；切视图时清 node/tab，避免跨视图幽灵选中。
  const setView = (next: NodesView) => {
    const params = new URLSearchParams(searchParams)
    if (next === 'active') params.delete('view')
    else params.set('view', 'archive')
    params.delete('node')
    params.delete('tab')
    setSearchParams(params)
    setQuery('')
  }
  // 切换激活分段写入 URL（默认 overview 省略，保持链接简洁）。
  const setTab = (next: DetailTab) => {
    const params = new URLSearchParams(searchParams)
    if (next === 'overview') params.delete('tab')
    else params.set('tab', next)
    setSearchParams(params)
  }
  // 左栏收缩为窄图标轨（FR-177）：收缩态持久化（localStorage）。
  const [collapsed, setCollapsed] = useState(loadNodeListCollapsed)

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

  const filtered = useMemo(() => filterNodes(nodes ?? [], query), [nodes, query])
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
    return pool[0].id
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

  const toggleCollapsed = () => {
    setCollapsed((c) => {
      const next = !c
      persistNodeListCollapsed(next)
      return next
    })
  }

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
  const listLoading = isArchive ? archivedLoading : isLoading

  // 阶段 6 页面重做（照原型 `nodesPage()`）：本页拆成**列表态**与**详情态**。
  //
  // 判据用 `selectedId` —— 它就是「URL 里有没有 node 参数」（null 即没有）。
  // 不能用 `effectiveSelectedId`：后者在无参数时会回退到第一个节点，
  // 那样列表态会错误地自动选中一个，用户以为进了详情。
  if (selectedId === null) {
    return (
      <PageShell data-page="nodes">
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
        <ScopeBar>
          <SummaryChips chips={summaryChips} />
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
        {listLoading ? (
          <DataPanelSkeleton />
        ) : (
          <CardsGrid className="min-h-0 flex-1">
            {filtered.map((node) => (
              <NodeCard
                key={node.id}
                node={node}
                instanceCount={instanceCountByNode.get(node.id) ?? 0}
                onOpen={() => setSelectedId(node.id)}
              />
            ))}
          </CardsGrid>
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
      {/* 左栏：可收缩节点列表（窄图标轨 ⇄ 展开），收缩态持久 */}
      <aside
        className={cn(
          'flex min-h-0 shrink-0 flex-col rounded-lg border bg-card/95 shadow-soft backdrop-blur-sm transition-[width] duration-200 ease-ios',
          collapsed ? 'w-full lg:w-14' : 'w-full lg:w-72',
        )}
      >
        {collapsed ? (
          <div className="flex min-h-0 flex-1 flex-col items-center gap-1.5 p-2">
            <button
              type="button"
              onClick={toggleCollapsed}
              aria-label={t('nodes.expandList')}
              title={t('nodes.expandList')}
              className="grid size-9 w-full place-items-center rounded-lg text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
            >
              <ChevronsRight className="size-4" />
            </button>
            <div className="flex min-h-0 flex-1 flex-col items-center gap-1.5 overflow-y-auto scrollbar-none">
              {isArchive
                ? filteredArchived.map((node) => (
                    <button
                      key={node.id}
                      type="button"
                      onClick={() => setSelectedId(node.id)}
                      title={node.name}
                      aria-label={node.name}
                      className={cn(
                        'grid size-9 place-items-center rounded-lg border text-xs font-semibold transition-colors',
                        node.id === effectiveSelectedId
                          ? 'border-primary/40 bg-primary/10 text-primary'
                          : 'border-transparent text-muted-foreground hover:bg-accent/60',
                      )}
                    >
                      {node.name.slice(0, 1).toUpperCase()}
                    </button>
                  ))
                : filtered.map((node) => (
                    <NodeRailIcon
                      key={node.id}
                      node={node}
                      selected={node.id === effectiveSelectedId}
                      onSelect={() => setSelectedId(node.id)}
                    />
                  ))}
            </div>
          </div>
        ) : (
          <>
            <div className="shrink-0 space-y-2 border-b p-3">
              <div className="flex items-center justify-between gap-2">
                <h1 className="text-sm font-bold">{t('nodes.title')}</h1>
                <button
                  type="button"
                  onClick={toggleCollapsed}
                  aria-label={t('nodes.collapseList')}
                  title={t('nodes.collapseList')}
                  className="grid size-7 shrink-0 place-items-center rounded text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
                >
                  <ChevronsLeft className="size-4" />
                </button>
              </div>
              {/* 活跃 | 归档 页面级分段（FR-393，URL ?view=） */}
              <Segments className="w-full" aria-label={t('nodes.title')}>
                {([
                  ['active', 'nodes.viewActive'],
                  ['archive', 'nodes.viewArchive'],
                ] as const).map(([k, labelKey]) => (
                  <Segment key={k} className="flex-1" active={view === k} onClick={() => setView(k)}>
                    {t(labelKey)}
                  </Segment>
                ))}
              </Segments>
              {!isArchive && (
                <>
                  {/* 集群汇总头：状态计数 chip + CPU/内存/磁盘聚合水位（复用 summarizeNodes，FR-144） */}
                  <SummaryChips chips={summaryChips} />
                  <div className="grid grid-cols-3 gap-1.5">
                    <StatCard label={t('nodes.cpu')} value={gauge(summary.cpuPct)} bar={summary.cpuPct !== null ? { value: summary.cpuPct, level: resourceLevel(summary.cpuPct) } : undefined} />
                    <StatCard label={t('nodes.memory')} value={gauge(summary.memPct)} bar={summary.memPct !== null ? { value: summary.memPct, level: resourceLevel(summary.memPct) } : undefined} />
                    <StatCard label={t('nodes.disk')} value={gauge(summary.diskPct)} bar={summary.diskPct !== null ? { value: summary.diskPct, level: resourceLevel(summary.diskPct) } : undefined} />
                  </div>
                </>
              )}
              <div className="relative">
                <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
                <Input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder={t('nodes.searchPlaceholder')}
                  className="h-8 pl-8 text-sm"
                  aria-label={t('nodes.searchPlaceholder')}
                />
              </div>
              {!isArchive && (
                <Button size="sm" className="w-full" onClick={() => setAddOpen(true)}>
                  <Plus className="size-4" /> {t('nodes.enroll.addNode')}
                </Button>
              )}
            </div>
            <div className="min-h-0 flex-1 space-y-1 overflow-y-auto p-2">
              {listLoading ? (
                <p className="px-2 py-4 text-sm text-muted-foreground">{t('common.loading')}</p>
              ) : isArchive ? (
                (archivedNodes?.length ?? 0) === 0 ? (
                  <p className="px-2 py-4 text-sm text-muted-foreground">{t('nodes.archivedEmpty')}</p>
                ) : filteredArchived.length === 0 ? (
                  <p className="px-2 py-4 text-sm text-muted-foreground">{t('nodes.searchEmpty')}</p>
                ) : (
                  filteredArchived.map((node) => (
                    <ArchivedNodeListRow
                      key={node.id}
                      node={node}
                      selected={node.id === effectiveSelectedId}
                      onSelect={() => setSelectedId(node.id)}
                    />
                  ))
                )
              ) : (nodes?.length ?? 0) === 0 ? (
                <p className="px-2 py-4 text-sm text-muted-foreground">{t('nodes.empty')}</p>
              ) : filtered.length === 0 ? (
                <p className="px-2 py-4 text-sm text-muted-foreground">{t('nodes.searchEmpty')}</p>
              ) : (
                filtered.map((node) => (
                  <NodeListRow
                    key={node.id}
                    node={node}
                    instanceCount={instanceCountByNode.get(node.id) ?? 0}
                    selected={node.id === effectiveSelectedId}
                    onSelect={() => setSelectedId(node.id)}
                  />
                ))
              )}
            </div>
          </>
        )}
      </aside>

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
            renderTab={(tabKey, paneNode) => (
              <>
                {tabKey === 'overview' && <NodeOverviewSection node={paneNode} />}
                {tabKey === 'instances' && <NodeInstanceCompareContainer node={paneNode} />}
                {tabKey === 'runtime' && (
                  <div className="space-y-4">
                    <NodeJDKTab nodeId={paneNode.id} active />
                    <NodeLogRuntimeTab
                      nodeId={paneNode.id}
                      os={paneNode.os}
                      arch={paneNode.arch}
                      online={paneNode.status === 1}
                    />
                  </div>
                )}
                {tabKey === 'cache' && <NodeArtifactCacheTab nodeId={paneNode.id} />}
                {tabKey === 'ports' && (
                  <Panel title={t('ports.title')}>
                    <NodePortsTab nodeId={paneNode.id} />
                  </Panel>
                )}
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
                {tabKey === 'repair' && <NodeRepairTab node={paneNode} active />}
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
/** 实例对比图的取数接线层（ADR-097）：视图已入包，此处注入服务端搜索与批量指标。 */
function NodeInstanceCompareContainer({ node }: { node: NodeInfo }) {
  const [metric, setMetric] = useState('inst_tps')
  // 服务端按节点过滤分页取前 12（名称升序）；total 为该节点实例总数（提示中的 N）。
  const { data: search } = useInstanceSearch({
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
    <NodeInstanceCompare
      metric={metric}
      onMetricChange={setMetric}
      total={search?.total ?? 0}
      series={series}
    />
  )
}

/** 节点监控曲线的取数接线层（ADR-097）。 */
function NodeMonitorChartsContainer({ node }: { node: NodeInfo }) {
  const [range, setRange] = useState<MetricRange>('24h')
  const { data } = useMetricSeries({ scope: 'node', targetId: node.uuid, range })
  return <NodeMonitorCharts range={range} onRangeChange={setRange} series={data?.series ?? []} />
}

