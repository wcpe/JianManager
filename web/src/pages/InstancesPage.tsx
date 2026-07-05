import { Fragment, useEffect, useMemo, useState } from 'react'
import { useSearchParams, useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { ChevronRight, ChevronDown, Zap, Globe, Plus, FolderTree } from 'lucide-react'
import {
  useInstances,
  useStartInstance,
  useStopInstance,
  useRestartInstance,
  useDeleteInstance,
  useKillInstance,
  type InstanceListParams,
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
import { instanceStatusLevel } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
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

export default function InstancesPage() {
  const { t } = useTranslation()
  // 点实例名进统一的「运维控制台」（终端/文件/配置/Bot），不再跳老的实例详情页。
  const openInstance = useConsoleStore((s) => s.openInstance)
  const navigate = useNavigate()
  const [showProvision, setShowProvision] = useState(false)
  const [showProvisionProxy, setShowProvisionProxy] = useState(false)
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
  const [deleteTarget, setDeleteTarget] = useState<{ id: number; name: string } | null>(null)
  const [killTarget, setKillTarget] = useState<{ id: number; name: string } | null>(null)
  // 批量操作选中的实例 ID 集合（FR-058）。
  const [selectedIds, setSelectedIds] = useState<number[]>([])
  // 工作台卡 ⇄ 列表视图（FR-136，§4.5）；运行实体默认卡片。
  const [view, setView] = useState<ViewMode>('card')
  // 组织分组视图开关（FR-165，§4.4）：开启后切到「左分组树 + 右列表」专用形态，
  // 与既有筛选/groupBy 分组并列、互不破坏；关闭回到原平铺/分组视图。
  const [orgView, setOrgView] = useState(false)
  // proxy 行 inline 展开已注册 backend 的代理 id 集合（FR-136）。
  const [expandedProxies, setExpandedProxies] = useState<Set<number>>(new Set())

  // 多维筛选状态（FR-047 / FR-268）：群组 / 环境 / 标签 / 节点 / 状态任意组合，下发后端过滤。
  const [networkId, setNetworkId] = useState<string>(ALL)
  const [env, setEnv] = useState<string>(ALL)
  const [tag, setTag] = useState<string>(ALL)
  const selectedNodeId = useConsoleStore((s) => s.selectedNodeId)
  const setSelectedNodeId = useConsoleStore((s) => s.setSelectedNodeId)
  const nodeId = selectedNodeId == null ? ALL : String(selectedNodeId)
  const setNodeFilter = (value: string) => setSelectedNodeId(value === ALL ? null : Number(value))
  // 顶栏集群徽标（FR-162）点击带 ?status= 跳转筛选。挂载时取初值；URL 变化时同步——
  // 用 React 推荐的渲染期「随外部值调整 state」模式（存上一次值比对，避免 effect 内 setState）。
  // 完整 URL 可寻址（全维度筛选进 URL、深链还原）归 FR-128，此处仅最小启用胶水。
  const [searchParams] = useSearchParams()
  const urlStatus = searchParams.get('status')
  const [statusFilter, setStatusFilter] = useState<string>(urlStatus ?? ALL)
  const [prevUrlStatus, setPrevUrlStatus] = useState(urlStatus)
  if (urlStatus !== prevUrlStatus) {
    setPrevUrlStatus(urlStatus)
    if (urlStatus) setStatusFilter(urlStatus)
  }
  // 分组视图维度。
  const [groupBy, setGroupBy] = useState<GroupDimension>('none')

  const params: InstanceListParams = useMemo(() => {
    const p: InstanceListParams = {}
    if (networkId !== ALL) p.networkId = Number(networkId)
    if (env !== ALL) p.env = env
    if (tag !== ALL) p.tag = tag
    if (nodeId !== ALL) p.nodeId = Number(nodeId)
    if (statusFilter !== ALL) p.status = statusFilter
    return p
  }, [networkId, env, tag, nodeId, statusFilter])

  const { data: instances, isLoading } = useInstances(params)
  // 未过滤集合：用于填充环境/标签下拉选项 + 汇总头计数（避免随筛选自我收敛）。
  const { data: allInstances } = useInstances()
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
      (allInstances ?? instances ?? [])
        .filter((i) => selectedIds.includes(i.id))
        .map((i) => ({ id: i.id, name: i.name, status: i.status })),
    [allInstances, instances, selectedIds],
  )

  const scopedAllInstances = useMemo(
    () => (selectedNodeId == null ? (allInstances ?? []) : (allInstances ?? []).filter((i) => i.nodeId === selectedNodeId)),
    [allInstances, selectedNodeId],
  )
  const envOptions = useMemo(() => collectEnvs(scopedAllInstances), [scopedAllInstances])
  const tagOptions = useMemo(() => collectTags(scopedAllInstances), [scopedAllInstances])
  const nodeName = (id: number) => nodes?.find((n) => n.id === id)?.name ?? t('console.unknownNode', { id })

  const groups = useMemo(() => groupInstances(instances ?? [], groupBy), [instances, groupBy])

  // 汇总头计数随页眉节点作用域收敛，但不受状态/标签等本页细筛选影响。
  const counts = useMemo(() => summarizeInstances(scopedAllInstances), [scopedAllInstances])

  const hasActiveFilter =
    networkId !== ALL || env !== ALL || tag !== ALL || nodeId !== ALL || statusFilter !== ALL
  const resetFilters = () => {
    setNetworkId(ALL)
    setEnv(ALL)
    setTag(ALL)
    setSelectedNodeId(null)
    setStatusFilter(ALL)
  }

  // 汇总 chip 点击 → 设状态筛选（再点同项=清空），可发现「不正常的」（§6.3 #4）。
  const applySummaryFilter = (key: SummaryFilterKey) => {
    const target = summaryFilterStatus(key)
    setStatusFilter((cur) => (cur === (target ?? ALL) ? ALL : (target ?? ALL)))
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
      onDelete={() => setDeleteTarget({ id: inst.id, name: inst.name })}
    />
  )

  const toggleProxy = (id: number) =>
    setExpandedProxies((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  const renderRow = (inst: NonNullable<typeof instances>[number]) => {
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
              {/* 主操作随状态，操作进行中禁用防连点（FR-138） */}
              {(inst.status === 'STOPPED' || inst.status === 'CRASHED') && (
                <Button
                  variant="ghost"
                  size="xs"
                  disabled={start.isPending && start.variables === inst.id}
                  onClick={() => start.mutate(inst.id)}
                  aria-label={t('instances.start')}
                  className="text-green-600 hover:text-green-700"
                >
                  {start.isPending && start.variables === inst.id ? t('instances.processing') : t('instances.start')}
                </Button>
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
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => setShowProvision(true)}>
            <Zap className="size-4" /> {t('provision.entry')}
          </Button>
          <Button variant="outline" onClick={() => setShowProvisionProxy(true)}>
            <Globe className="size-4" /> {t('proxy.entry')}
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
          onChange={setView}
          cardLabel={t('grouping.viewCard')}
          listLabel={t('grouping.viewList')}
        />
      </div>

      {/* 多维筛选 + 分组视图（FR-047） */}
      <div className="jm-toolbar-surface flex flex-wrap items-center gap-2 p-2">
        <FilterSelect
          label={t('grouping.filterNetwork')}
          value={networkId}
          onChange={setNetworkId}
          options={(networks ?? []).map((n) => ({ value: String(n.id), label: n.name }))}
        />
        <FilterSelect
          label={t('grouping.filterEnv')}
          value={env}
          onChange={setEnv}
          options={envOptions.map((e) => ({ value: e, label: t(`grouping.env_${e}`, { defaultValue: e }) }))}
        />
        <FilterSelect
          label={t('grouping.filterTag')}
          value={tag}
          onChange={setTag}
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
          onChange={setStatusFilter}
          options={Object.entries(statusConfig).map(([k, v]) => ({ value: k, label: v.text }))}
        />

        <div className="ml-auto flex items-center gap-2">
          {hasActiveFilter && (
            <Button variant="ghost" size="sm" onClick={resetFilters} className="text-muted-foreground">
              {t('grouping.clearFilters')}
            </Button>
          )}
          {/* 组织分组视图开关（FR-165，§4.4）：与 groupBy 维度分组并列。 */}
          <Button
            variant={orgView ? 'default' : 'outline'}
            size="sm"
            onClick={() => setOrgView((v) => !v)}
            aria-pressed={orgView}
          >
            <FolderTree className="size-4" /> {t('instanceGroups.orgView')}
          </Button>
          {!orgView && (
            <>
              <span className="text-sm text-muted-foreground">{t('grouping.groupBy')}</span>
              <Select value={groupBy} onValueChange={(v) => setGroupBy(v as GroupDimension)}>
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
            </>
          )}
        </div>
      </div>

      <ProvisionServerDialog open={showProvision} onClose={() => setShowProvision(false)} />
      <ProvisionProxyDialog open={showProvisionProxy} onClose={() => setShowProvisionProxy(false)} />
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
          {view === 'card' ? (
            <CardView
              groupBy={groupBy}
              groups={groups}
              groupLabel={groupLabel}
              nodeName={nodeName}
              buildMenu={buildMenu}
              hasActiveFilter={hasActiveFilter}
            />
          ) : groupBy === 'none' ? (
            <VirtualizedInstanceTable
              instances={instances ?? []}
              header={<InstanceTableHeader t={t} allSelected={allSelected} onToggleAll={toggleAll} />}
              renderRow={renderRow}
              emptyLabel={hasActiveFilter ? t('grouping.noMatch') : t('instances.empty')}
            />
          ) : (
            <div className="space-y-4">
              {groups.map((g) => (
                <div key={g.key || '__none__'} className="overflow-hidden rounded-lg border bg-card/95 shadow-soft">
                  <div className="flex items-center gap-2 px-4 py-2 bg-muted/50 border-b">
                    <span className="font-medium text-sm">{groupLabel(g.key)}</span>
                    <Badge variant="outline" className="font-normal">{g.instances.length}</Badge>
                  </div>
                  <Table>
                    <InstanceTableHeader t={t} allSelected={allSelected} onToggleAll={toggleAll} />
                    <TableBody>{g.instances.map(renderRow)}</TableBody>
                  </Table>
                </div>
              ))}
              {groups.length === 0 && (
                <p className="text-center text-muted-foreground py-8">
                  {hasActiveFilter ? t('grouping.noMatch') : t('instances.empty')}
                </p>
              )}
            </div>
          )}
        </div>
      )}

      <DangerConfirm
        open={deleteTarget !== null}
        title={t('danger.deleteInstanceTitle', { name: deleteTarget?.name ?? '' })}
        description={t('danger.deleteInstanceDesc')}
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

function VirtualizedInstanceTable({
  instances,
  header,
  renderRow,
  emptyLabel,
}: {
  instances: InstanceInfo[]
  header: React.ReactNode
  renderRow: (inst: InstanceInfo) => React.ReactNode
  emptyLabel: string
}) {
  const {
    containerRef,
    onScroll,
    range,
  } = useVirtualRows({
    total: instances.length,
    itemSize: 44,
    overscan: 10,
    fallbackViewportSize: 520,
  })
  const visible = instances.slice(range.start, range.end)

  return (
    <div
      ref={containerRef}
      onScroll={onScroll}
      data-testid="instances-table-virtual"
      data-total-count={instances.length}
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
          {visible.map(renderRow)}
          {range.after > 0 && (
            <TableRow aria-hidden="true">
              <TableCell colSpan={8} className="p-0" style={{ height: range.after }} />
            </TableRow>
          )}
          {instances.length === 0 && (
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

/**
 * 卡片视图（FR-136 工作台卡）：平铺或按分组维度分段渲染工作台卡网格。
 * 分组维度非 none 时每组一段（组头 + 该组卡片网格）。
 */
function CardView({
  groupBy,
  groups,
  groupLabel,
  nodeName,
  buildMenu,
  hasActiveFilter,
}: {
  groupBy: GroupDimension
  groups: { key: string; instances: InstanceInfo[] }[]
  groupLabel: (key: string) => string
  nodeName: (id: number) => string
  buildMenu: (inst: InstanceInfo) => React.ReactNode
  hasActiveFilter: boolean
}) {
  const { t } = useTranslation()
  const grid = (list: InstanceInfo[]) => (
    <VirtualizedCardGrid instances={list} nodeName={nodeName} buildMenu={buildMenu} />
  )

  const totalCount = groups.reduce((sum, g) => sum + g.instances.length, 0)
  if (totalCount === 0) {
    return (
      <p className="text-center text-muted-foreground py-8">
        {hasActiveFilter ? t('grouping.noMatch') : t('instances.empty')}
      </p>
    )
  }

  if (groupBy === 'none') {
    return grid(groups[0]?.instances ?? [])
  }
  return (
    <div className="space-y-4">
      {groups.map((g) => (
        <div key={g.key || '__none__'} className="space-y-2">
          <div className="flex items-center gap-2 px-1">
            <span className="text-sm font-medium">{groupLabel(g.key)}</span>
            <Badge variant="outline" className="font-normal">{g.instances.length}</Badge>
          </div>
          {grid(g.instances)}
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
  nodeName,
  buildMenu,
}: {
  instances: InstanceInfo[]
  nodeName: (id: number) => string
  buildMenu: (inst: InstanceInfo) => React.ReactNode
}) {
  const columns = useCardColumns()
  const rowCount = Math.ceil(instances.length / columns)
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
  const rows = []
  for (let rowIndex = range.start; rowIndex < range.end; rowIndex++) {
    const rowItems = instances.slice(rowIndex * columns, rowIndex * columns + columns)
    rows.push({ rowIndex, rowItems })
  }

  return (
    <div
      ref={containerRef}
      onScroll={onScroll}
      data-testid="instances-card-virtual"
      data-total-count={instances.length}
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
            {rowItems.map((inst) => (
              <div key={inst.id} data-testid="instances-card-virtual-item">
                <InstanceWorktableCard
                  inst={inst}
                  nodeName={nodeName(inst.nodeId)}
                  roleBadge={<RoleBadge role={inst.role} compact />}
                  menu={buildMenu(inst)}
                />
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
  const openInstance = useConsoleStore((s) => s.openInstance)
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
                  onClick={() => openInstance(b.id)}
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
}: {
  t: (k: string) => string
  allSelected: boolean
  onToggleAll: () => void
}) {
  return (
    <TableHeader className="bg-muted/50">
      <TableRow>
        <TableHead className="w-10">
          <Checkbox checked={allSelected} onCheckedChange={onToggleAll} aria-label={t('instanceBatch.selectAll')} />
        </TableHead>
        <TableHead>{t('instances.name')}</TableHead>
        <TableHead>{t('instances.type')}</TableHead>
        <TableHead>{t('instances.nodePort')}</TableHead>
        <TableHead>{t('instances.role')}</TableHead>
        <TableHead>{t('grouping.tagsColumn')}</TableHead>
        <TableHead>{t('instances.status')}</TableHead>
        <TableHead>{t('instances.actions')}</TableHead>
      </TableRow>
    </TableHeader>
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
      <SelectTrigger size="sm" className="w-40">
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
 * 行内只保留启停/重启主操作。运行态下克隆/删除改禁用 + tooltip（非消失），删除标红。
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
  // 克隆/删除要求实例已停止（运行/过渡态禁用并提示原因，而非隐藏）。
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
          className={stopped ? undefined : 'opacity-50 cursor-not-allowed'}
          onSelect={(e) => {
            if (!stopped) {
              e.preventDefault()
              return
            }
            onDelete()
          }}
        >
          {t('common.delete')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
