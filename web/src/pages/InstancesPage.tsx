import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import {
  useInstances,
  useStartInstance,
  useStopInstance,
  useRestartInstance,
  useDeleteInstance,
  useKillInstance,
  type InstanceListParams,
} from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useNetworks } from '@/api/networks'
import { useConsoleStore } from '@/stores/console'
import DangerConfirm from '@/components/DangerConfirm'
import InstanceBatchBar from '@/components/InstanceBatchBar'
import CreateInstanceDialog from '@/components/CreateInstanceDialog'
import ProvisionServerDialog from '@/components/ProvisionServerDialog'
import ProvisionProxyDialog from '@/components/ProvisionProxyDialog'
import ProxyRegistrationsDialog from '@/components/ProxyRegistrationsDialog'
import CloneInstanceDialog from '@/components/CloneInstanceDialog'
import InstanceTagsDialog from '@/components/InstanceTagsDialog'
import EditInstanceLimitsDialog from '@/components/EditInstanceLimitsDialog'
import {
  collectEnvs,
  collectTags,
  envOf,
  freeTagsOf,
  groupInstances,
  parseTags,
  type GroupDimension,
} from '@/components/console/instance-grouping'
import { Badge } from '@/components/ui/badge'
import { StatusBadge } from '@/components/ui/status-badge'
import { instanceStatusLevel } from '@/lib/threshold'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

/** Radix Select 不允许空字符串 value，用哨兵值表示「全部 / 不过滤」。 */
const ALL = '__all__'

export default function InstancesPage() {
  const { t } = useTranslation()
  // 点实例名进统一的「运维控制台」（终端/文件/配置/Bot），不再跳老的实例详情页。
  const openInstance = useConsoleStore((s) => s.openInstance)
  const [showCreate, setShowCreate] = useState(false)
  const [showProvision, setShowProvision] = useState(false)
  const [showProvisionProxy, setShowProvisionProxy] = useState(false)
  const [manageProxy, setManageProxy] = useState<{ id: number; name: string } | null>(null)
  const [cloneTarget, setCloneTarget] = useState<{ id: number; name: string } | null>(null)
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

  // 多维筛选状态（FR-047）：群组 / 环境 / 标签 / 节点 / 状态任意组合，下发后端过滤。
  const [networkId, setNetworkId] = useState<string>(ALL)
  const [env, setEnv] = useState<string>(ALL)
  const [tag, setTag] = useState<string>(ALL)
  const [nodeId, setNodeId] = useState<string>(ALL)
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
  // 未过滤集合：用于填充环境/标签下拉选项，避免选项随筛选自我收敛。
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

  const envOptions = useMemo(() => collectEnvs(allInstances ?? []), [allInstances])
  const tagOptions = useMemo(() => collectTags(allInstances ?? []), [allInstances])
  const nodeName = (id: number) => nodes?.find((n) => n.id === id)?.name ?? t('console.unknownNode', { id })

  const groups = useMemo(() => groupInstances(instances ?? [], groupBy), [instances, groupBy])

  const hasActiveFilter =
    networkId !== ALL || env !== ALL || tag !== ALL || nodeId !== ALL || statusFilter !== ALL
  const resetFilters = () => {
    setNetworkId(ALL)
    setEnv(ALL)
    setTag(ALL)
    setNodeId(ALL)
    setStatusFilter(ALL)
  }

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

  const renderRow = (inst: NonNullable<typeof instances>[number]) => {
    const st = statusConfig[inst.status] || statusConfig.STOPPED
    const instEnv = envOf(inst)
    const free = freeTagsOf(inst)
    return (
      <TableRow key={inst.id} data-state={selectedIds.includes(inst.id) ? 'selected' : undefined}>
        <TableCell>
          <Checkbox
            checked={selectedIds.includes(inst.id)}
            onCheckedChange={() => toggleOne(inst.id)}
            aria-label={inst.name}
          />
        </TableCell>
        <TableCell className="font-medium">
          <button
            type="button"
            className="text-left text-primary hover:underline"
            onClick={() => openInstance(inst.id)}
          >
            {inst.name}
          </button>
        </TableCell>
        <TableCell className="text-muted-foreground">{inst.type}</TableCell>
        <TableCell>
          {inst.role === 'proxy' ? (
            <Badge variant="default">{t('networks.role_proxy')}</Badge>
          ) : inst.role === 'backend' ? (
            <Badge variant="secondary">{t('networks.role_backend')}</Badge>
          ) : (
            <span className="text-muted-foreground text-xs">{t('networks.role_universal')}</span>
          )}
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
          <StatusBadge level={instanceStatusLevel(inst.status)} label={st.text} />
        </TableCell>
        <TableCell>
          <div className="flex gap-1">
            <Button
              variant="ghost"
              size="xs"
              onClick={() => setTagsTarget({ id: inst.id, name: inst.name, tags: parseTags(inst.tags) })}
              className="text-muted-foreground hover:text-foreground"
            >
              {t('grouping.editTags')}
            </Button>
            {inst.processType === 'docker' && (
              <Button
                variant="ghost"
                size="xs"
                onClick={() => setLimitsTarget({
                  id: inst.id,
                  name: inst.name,
                  processType: inst.processType,
                  cpuLimit: inst.cpuLimit ?? 0,
                  memLimitMb: inst.memLimitMb ?? 0,
                  diskLimitMb: inst.diskLimitMb ?? 0,
                })}
                className="text-muted-foreground hover:text-foreground"
              >
                {t('instances.resourceLimit')}
              </Button>
            )}
            {inst.role === 'proxy' && (
              <Button
                variant="ghost"
                size="xs"
                onClick={() => setManageProxy({ id: inst.id, name: inst.name })}
                className="text-indigo-600 hover:text-indigo-700"
              >
                {t('proxy.manageBackends')}
              </Button>
            )}
            {inst.role === 'backend' && (inst.status === 'STOPPED' || inst.status === 'CRASHED') && (
              <Button
                variant="ghost"
                size="xs"
                onClick={() => setCloneTarget({ id: inst.id, name: inst.name })}
                className="text-indigo-600 hover:text-indigo-700"
              >
                {t('clone.action')}
              </Button>
            )}
            {(inst.status === 'STOPPED' || inst.status === 'CRASHED') && (
              <Button
                variant="ghost"
                size="xs"
                onClick={() => start.mutate(inst.id)}
                className="text-green-600 hover:text-green-700"
              >
                {t('instances.start')}
              </Button>
            )}
            {inst.status === 'RUNNING' && (
              <>
                <Button
                  variant="ghost"
                  size="xs"
                  onClick={() => stop.mutate(inst.id)}
                  className="text-yellow-600 hover:text-yellow-700"
                >
                  {t('instances.stop')}
                </Button>
                <Button
                  variant="ghost"
                  size="xs"
                  onClick={() => restart.mutate(inst.id)}
                  className="text-blue-600 hover:text-blue-700"
                >
                  {t('instances.restart')}
                </Button>
              </>
            )}
            {(inst.status === 'STARTING' || inst.status === 'STOPPING') && (
              <Button
                variant="ghost"
                size="xs"
                onClick={() => setKillTarget({ id: inst.id, name: inst.name })}
                className="text-yellow-600 hover:text-yellow-700"
              >
                {t('instances.kill')}
              </Button>
            )}
            {(inst.status === 'STOPPED' || inst.status === 'CRASHED') && (
              <Button
                variant="ghost"
                size="xs"
                onClick={() => setDeleteTarget({ id: inst.id, name: inst.name })}
                className="text-red-600 hover:text-red-700"
              >
                {t('common.delete')}
              </Button>
            )}
          </div>
        </TableCell>
      </TableRow>
    )
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-2xl font-bold">{t('instances.title')}</h1>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => setShowProvision(true)}>⚡ {t('provision.entry')}</Button>
          <Button variant="outline" onClick={() => setShowProvisionProxy(true)}>🌐 {t('proxy.entry')}</Button>
          <Button onClick={() => setShowCreate(true)}>+ {t('instances.createInstance')}</Button>
        </div>
      </div>

      {/* 多维筛选 + 分组视图（FR-047） */}
      <div className="flex flex-wrap items-center gap-2 mb-4">
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
          onChange={setNodeId}
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
        </div>
      </div>

      <ProvisionServerDialog open={showProvision} onClose={() => setShowProvision(false)} />
      <ProvisionProxyDialog open={showProvisionProxy} onClose={() => setShowProvisionProxy(false)} />
      <CreateInstanceDialog open={showCreate} onClose={() => setShowCreate(false)} />
      {manageProxy && (
        <ProxyRegistrationsDialog proxyId={manageProxy.id} proxyName={manageProxy.name} onClose={() => setManageProxy(null)} />
      )}
      {cloneTarget && (
        <CloneInstanceDialog sourceId={cloneTarget.id} sourceName={cloneTarget.name} onClose={() => setCloneTarget(null)} />
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

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <div className="space-y-3">
          {selectedIds.length > 0 && (
            <InstanceBatchBar selectedIds={selectedIds} onClear={clearSelection} />
          )}
          {groupBy === 'none' ? (
            <div className="border rounded-lg">
              <Table>
                <InstanceTableHeader t={t} allSelected={allSelected} onToggleAll={toggleAll} />
                <TableBody>
                  {(instances ?? []).map(renderRow)}
                  {(!instances || instances.length === 0) && (
                    <TableRow>
                      <TableCell colSpan={7} className="text-center text-muted-foreground">
                        {hasActiveFilter ? t('grouping.noMatch') : t('instances.empty')}
                      </TableCell>
                    </TableRow>
                  )}
                </TableBody>
              </Table>
            </div>
          ) : (
            <div className="space-y-4">
              {groups.map((g) => (
                <div key={g.key || '__none__'} className="border rounded-lg">
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

/** 实例表头（平铺与分组视图复用）。含批量全选复选框（FR-058）。 */
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
