import { useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router'
import { useQueries } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Network, GitBranch, List } from 'lucide-react'
import api from '@/api/client'
import DangerConfirm from '@/components/DangerConfirm'
import { Panel } from '@/components/ui/panel'
import { StatusBadge } from '@/components/ui/status-badge'
import { MODAL_OVERLAY, MODAL_PANEL } from '@/components/ui/scrollable-dialog'
import { FieldLabel, FieldError } from '@/components/ui/field-label'
import { validateRequired } from '@/lib/form-validation'
import { instanceStatusLevel, statusColorVar } from '@/lib/threshold'
import { cn } from '@/lib/utils'
import { memberHealth, type MemberHealth } from '@/lib/topology'
import TopologyGraph from '@/components/console/TopologyGraph'
import { useInstances } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import {
  useNetworks,
  useNetwork,
  useCreateNetwork,
  useDeleteNetwork,
  useAddNetworkMembers,
  useRemoveNetworkMember,
  useNetworkAction,
  type NetworkSummary,
  type NetworkDetail,
} from '@/api/networks'

/** 实例运行状态 → i18n 文案键（复用实例页既有键，FR-160 统一 StatusBadge）。 */
const STATUS_LABEL: Record<string, string> = {
  RUNNING: 'instances.running',
  STOPPED: 'instances.stopped',
  STARTING: 'instances.starting',
  STOPPING: 'instances.stopping',
  CRASHED: 'instances.crashed',
}

/**
 * 群组（Network 软标签）管理页（FR-032 / FR-145 / ADR-007）：
 * 列表（成员健康分布）/ 拓扑（proxy↔backend 注册关系）两视图，详情可深链（?network=&view=）改为可寻址双栏。
 */
export default function NetworksPage() {
  const { t } = useTranslation()
  const { data: networks, isLoading } = useNetworks()
  const { data: proxies } = useInstances({ role: 'proxy' })
  const [createOpen, setCreateOpen] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<NetworkSummary | null>(null)
  const del = useDeleteNetwork()

  // 详情与视图存入 URL，支持深链 / 刷新还原（FR-145 可寻址；完整可寻址归 FR-128）。
  const [searchParams, setSearchParams] = useSearchParams()
  const detailId = searchParams.get('network') ? Number(searchParams.get('network')) : null
  const view = searchParams.get('view') === 'topology' ? 'topology' : 'list'

  const setView = (v: 'list' | 'topology') => {
    const next = new URLSearchParams(searchParams)
    if (v === 'topology') next.set('view', 'topology')
    else next.delete('view')
    setSearchParams(next, { replace: true })
  }
  const openDetail = (id: number) => {
    const next = new URLSearchParams(searchParams)
    next.set('network', String(id))
    setSearchParams(next)
  }
  const closeDetail = () => {
    const next = new URLSearchParams(searchParams)
    next.delete('network')
    setSearchParams(next)
  }

  const confirmDelete = () => {
    if (!deleteTarget) return
    del.mutate(deleteTarget.id, {
      onSuccess: () => toast.success(t('networks.deleted')),
      onError: () => toast.error(t('common.error')),
    })
    setDeleteTarget(null)
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-1">
        <h1 className="text-2xl font-bold">{t('networks.title')}</h1>
        <div className="flex items-center gap-2">
          <div className="inline-flex rounded-md border p-0.5">
            <ViewTab active={view === 'list'} onClick={() => setView('list')} icon={<List className="size-3.5" />}>
              {t('networks.viewList')}
            </ViewTab>
            <ViewTab active={view === 'topology'} onClick={() => setView('topology')} icon={<GitBranch className="size-3.5" />}>
              {t('networks.viewTopology')}
            </ViewTab>
          </div>
          <button
            onClick={() => setCreateOpen(true)}
            className="px-3 py-2 text-sm bg-primary text-primary-foreground rounded-md transition-colors hover:bg-primary/90"
          >
            {t('networks.create')}
          </button>
        </div>
      </div>
      <p className="text-xs text-muted-foreground mb-4">{t('networks.subtitle')}</p>

      {view === 'topology' ? (
        <Panel
          title={t('networks.topoTitle')}
          icon={<Network className="size-4" />}
          bodyClassName="p-4"
        >
          <p className="mb-3 text-xs text-muted-foreground">{t('networks.topoSubtitle')}</p>
          <TopologyGraph proxies={proxies ?? []} />
          <TopologyLegend />
        </Panel>
      ) : isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : (
        <NetworkList networks={networks ?? []} onView={openDetail} onDelete={setDeleteTarget} />
      )}

      {createOpen && <CreateNetworkModal onClose={() => setCreateOpen(false)} />}
      {detailId !== null && <NetworkDetailPanel networkId={detailId} onClose={closeDetail} />}

      <DangerConfirm
        open={deleteTarget !== null}
        title={t('networks.deleteConfirm', { name: deleteTarget?.name ?? '' })}
        confirmLabel={t('common.delete')}
        scope="platform"
        onConfirm={confirmDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

function ViewTab({
  active,
  onClick,
  icon,
  children,
}: {
  active: boolean
  onClick: () => void
  icon: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <button
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        'inline-flex items-center gap-1.5 rounded px-2.5 py-1 text-xs font-medium transition-colors',
        active ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground',
      )}
    >
      {icon}
      {children}
    </button>
  )
}

/** 群组列表：卡片化行 + 成员健康分布（成员状态经各群组详情并行拉取统计）。 */
function NetworkList({
  networks,
  onView,
  onDelete,
}: {
  networks: NetworkSummary[]
  onView: (id: number) => void
  onDelete: (n: NetworkSummary) => void
}) {
  const { t } = useTranslation()

  // 每群组一条详情查询并行拉取成员，用于行内健康分布（群组数动态，用 useQueries）。
  const details = useQueries({
    queries: networks.map((n) => ({
      queryKey: ['networks', n.id],
      queryFn: async () => {
        const { data } = await api.get<NetworkDetail>(`/networks/${n.id}`)
        return data
      },
      enabled: !!n.id,
    })),
  })

  if (networks.length === 0) {
    return <p className="text-muted-foreground text-center py-8">{t('networks.empty')}</p>
  }

  return (
    <div className="space-y-2.5">
      {networks.map((n, i) => {
        const detail = details[i]?.data
        const health = detail ? memberHealth(detail.members) : null
        return (
          <Panel key={n.id} hoverable className="px-0" bodyClassName="px-4 py-3">
            <div className="flex items-start justify-between gap-3">
              <button onClick={() => onView(n.id)} className="min-w-0 text-left group">
                <div className="flex items-center gap-2">
                  <span className="font-semibold group-hover:text-primary transition-colors">{n.name}</span>
                  <span className="text-xs text-muted-foreground">
                    {t('networks.memberCount', { count: n.memberCount })}
                  </span>
                </div>
                {n.description && <p className="mt-0.5 truncate text-sm text-muted-foreground">{n.description}</p>}
              </button>
              <div className="flex shrink-0 items-center gap-3">
                <button className="text-xs text-primary hover:underline" onClick={() => onView(n.id)}>
                  {t('networks.manage')}
                </button>
                <button className="text-xs text-status-danger hover:underline" onClick={() => onDelete(n)}>
                  {t('common.delete')}
                </button>
              </div>
            </div>
            <div className="mt-2.5">
              <HealthDistribution health={health} loading={!detail} />
            </div>
          </Panel>
        )
      })}
    </div>
  )
}

/** 成员健康分布条（运行/过渡/崩溃/停止分段着色 + 计数摘要）。 */
function HealthDistribution({ health, loading }: { health: MemberHealth | null; loading: boolean }) {
  const { t } = useTranslation()
  if (loading || !health) {
    return <div className="h-1.5 w-full animate-pulse rounded-full bg-muted" />
  }
  if (health.total === 0) {
    return <p className="text-xs text-muted-foreground">{t('networks.noMembers')}</p>
  }
  const segs: { value: number; className: string; label: string }[] = [
    { value: health.running, className: 'bg-status-success', label: t('networks.healthRunning') },
    { value: health.transitioning, className: 'bg-status-warning', label: t('networks.healthTransitioning') },
    { value: health.crashed, className: 'bg-status-danger', label: t('networks.healthCrashed') },
    { value: health.stopped, className: 'bg-muted-foreground/40', label: t('networks.healthStopped') },
  ]
  return (
    <div>
      <div className="flex h-1.5 w-full overflow-hidden rounded-full bg-muted">
        {segs.map((s, i) =>
          s.value > 0 ? (
            <div
              key={i}
              className={s.className}
              style={{ width: `${(s.value / health.total) * 100}%` }}
              title={`${s.label}: ${s.value}`}
            />
          ) : null,
        )}
      </div>
      <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-[11px] text-muted-foreground">
        {segs
          .filter((s) => s.value > 0)
          .map((s, i) => (
            <span key={i} className="inline-flex items-center gap-1">
              <span className={cn('size-1.5 rounded-full', s.className)} />
              {s.label} {s.value}
            </span>
          ))}
      </div>
    </div>
  )
}

/** 拓扑图例：状态色 + 启用/禁用连线说明。 */
function TopologyLegend() {
  const { t } = useTranslation()
  const items = [
    { className: 'bg-status-success', label: t('networks.healthRunning') },
    { className: 'bg-status-warning', label: t('networks.healthTransitioning') },
    { className: 'bg-status-danger', label: t('networks.healthCrashed') },
    { className: 'bg-muted-foreground/50', label: t('networks.topoDisabled') },
  ]
  return (
    <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 border-t pt-3 text-[11px] text-muted-foreground">
      {items.map((it, i) => (
        <span key={i} className="inline-flex items-center gap-1.5">
          <span className={cn('size-2 rounded-full', it.className)} />
          {it.label}
        </span>
      ))}
    </div>
  )
}

function CreateNetworkModal({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const create = useCreateNetwork()

  const nameError = validateRequired(name)

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (nameError) return
    create.mutate(
      { name, description: description || undefined },
      {
        onSuccess: () => {
          toast.success(t('networks.created'))
          onClose()
        },
        onError: (err: Error & { response?: { data?: { error?: string; message?: string } } }) => {
          if (err.response?.data?.error === 'NETWORK_NAME_CONFLICT') {
            toast.error(t('networks.nameConflict'))
            return
          }
          toast.error(err.response?.data?.message || t('networks.createFailed'))
        },
      },
    )
  }

  return (
    <div className={MODAL_OVERLAY}>
      <div className={`${MODAL_PANEL} max-w-md`}>
        <h2 className="text-lg font-bold mb-4">{t('networks.create')}</h2>
        <form onSubmit={submit} className="space-y-3">
          <div>
            <FieldLabel required>{t('networks.name')}</FieldLabel>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm aria-invalid:border-destructive"
              placeholder="survival"
              aria-invalid={!!nameError}
            />
            <FieldError error={nameError} />
          </div>
          <div>
            <FieldLabel>{t('networks.description')}</FieldLabel>
            <input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              className="w-full mt-1 px-3 py-2 border rounded-md bg-background text-sm"
            />
          </div>
          <div className="flex justify-end gap-2 pt-2">
            <button type="button" onClick={onClose} className="px-4 py-2 text-sm border rounded-md hover:bg-accent">
              {t('common.cancel')}
            </button>
            <button type="submit" disabled={create.isPending || !!nameError} className="px-4 py-2 text-sm bg-primary text-primary-foreground rounded-md disabled:opacity-50">
              {create.isPending ? t('common.creating') : t('common.create')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

/**
 * 群组详情：可寻址双栏（左成员 / 右候选），消除原嵌套滚动模态（FR-145）。
 * 成员用 StatusBadge；候选含节点·状态·端口并可筛。
 */
function NetworkDetailPanel({ networkId, onClose }: { networkId: number; onClose: () => void }) {
  const { t } = useTranslation()
  const { data: detail } = useNetwork(networkId)
  const { data: instances } = useInstances()
  const { data: nodes } = useNodes()
  const addMembers = useAddNetworkMembers(networkId)
  const removeMember = useRemoveNetworkMember(networkId)
  const action = useNetworkAction(networkId)
  const [selected, setSelected] = useState<number[]>([])
  const [candFilter, setCandFilter] = useState('')

  const nodeName = useMemo(() => {
    const m = new Map<number, string>()
    for (const n of nodes ?? []) m.set(n.id, n.name)
    return (id: number) => m.get(id) ?? `#${id}`
  }, [nodes])

  const memberIds = useMemo(() => new Set(detail?.members.map((m) => m.instanceId)), [detail])
  const candidates = useMemo(() => {
    const q = candFilter.trim().toLowerCase()
    return (instances ?? [])
      .filter((i) => !memberIds.has(i.id))
      .filter((i) => !q || i.name.toLowerCase().includes(q))
  }, [instances, memberIds, candFilter])

  const roleLabel = (role: string) => t(`networks.role_${role}`, { defaultValue: role })
  const statusLabel = (s: string) => (STATUS_LABEL[s] ? t(STATUS_LABEL[s]) : s)
  const health = detail ? memberHealth(detail.members) : null

  const runBatch = (act: 'start' | 'stop') => {
    action.mutate(act, {
      onSuccess: (res) => toast.success(t('networks.batchResult', { succeeded: res.succeeded, failed: res.failed })),
      onError: () => toast.error(t('common.error')),
    })
  }

  const toggleSel = (id: number, on: boolean) =>
    setSelected((prev) => (on ? [...prev, id] : prev.filter((x) => x !== id)))

  const addSelected = () => {
    if (selected.length === 0) return
    addMembers.mutate(selected, {
      onSuccess: () => {
        toast.success(t('networks.added', { count: selected.length }))
        setSelected([])
      },
      onError: () => toast.error(t('common.error')),
    })
  }

  return (
    <div className={MODAL_OVERLAY} onClick={onClose}>
      <div
        className="flex max-h-[88vh] w-full max-w-4xl flex-col rounded-2xl border bg-card text-card-foreground shadow-lift"
        onClick={(e) => e.stopPropagation()}
      >
        {/* 头部 */}
        <div className="flex shrink-0 items-center justify-between gap-3 border-b px-5 py-3.5">
          <div className="min-w-0">
            <h2 className="truncate text-lg font-bold">{detail?.name}</h2>
            {health && (
              <p className="mt-0.5 text-xs text-muted-foreground">
                {t('networks.memberCount', { count: health.total })}
                {health.total > 0 && (
                  <>
                    {' · '}
                    <span className="text-status-success">{t('networks.healthRunning')} {health.running}</span>
                    {health.crashed > 0 && (
                      <span className="text-status-danger"> · {t('networks.healthCrashed')} {health.crashed}</span>
                    )}
                  </>
                )}
              </p>
            )}
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <button
              onClick={() => runBatch('start')}
              disabled={action.isPending}
              className="rounded-md border px-2.5 py-1.5 text-xs hover:bg-accent disabled:opacity-50"
            >
              {t('networks.batchStart')}
            </button>
            <button
              onClick={() => runBatch('stop')}
              disabled={action.isPending}
              className="rounded-md border px-2.5 py-1.5 text-xs hover:bg-accent disabled:opacity-50"
            >
              {t('networks.batchStop')}
            </button>
            <button onClick={onClose} className="text-sm text-muted-foreground hover:text-foreground">
              {t('common.close')}
            </button>
          </div>
        </div>

        {/* 双栏：左成员 / 右候选 */}
        <div className="grid min-h-0 flex-1 grid-cols-1 gap-px overflow-hidden bg-border md:grid-cols-2">
          {/* 左：成员 */}
          <div className="flex min-h-0 flex-col bg-card">
            <div className="shrink-0 px-4 pt-3 pb-2 text-xs font-semibold tracking-wide text-muted-foreground">
              {t('networks.members')}
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-3">
              {detail && detail.members.length === 0 ? (
                <p className="px-2 py-6 text-center text-xs text-muted-foreground">{t('networks.noMembers')}</p>
              ) : (
                <ul className="space-y-1">
                  {detail?.members.map((m) => (
                    <li
                      key={m.instanceId}
                      className="flex items-center justify-between gap-2 rounded-lg px-2.5 py-2 hover:bg-accent/60"
                    >
                      <div className="flex min-w-0 items-center gap-2">
                        <StatusBadge
                          level={instanceStatusLevel(m.status)}
                          label={statusLabel(m.status)}
                          pulse={m.status === 'STARTING' || m.status === 'STOPPING'}
                        />
                        <span className="truncate text-sm font-medium">{m.name}</span>
                        <span className="shrink-0 text-[11px] text-muted-foreground">{roleLabel(m.role)}</span>
                      </div>
                      <button
                        className="shrink-0 text-xs text-status-danger hover:underline"
                        onClick={() => removeMember.mutate(m.instanceId)}
                      >
                        {t('networks.removeMember')}
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>

          {/* 右：候选 */}
          <div className="flex min-h-0 flex-col bg-card">
            <div className="flex shrink-0 items-center justify-between gap-2 px-4 pt-3 pb-2">
              <span className="text-xs font-semibold tracking-wide text-muted-foreground">
                {t('networks.addMembers')}
              </span>
              <input
                value={candFilter}
                onChange={(e) => setCandFilter(e.target.value)}
                placeholder={t('networks.filterCandidates')}
                className="w-36 rounded-md border bg-background px-2 py-1 text-xs"
              />
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto px-2">
              {candidates.length === 0 ? (
                <p className="px-2 py-6 text-center text-xs text-muted-foreground">{t('networks.noCandidates')}</p>
              ) : (
                <ul className="space-y-1">
                  {candidates.map((i) => {
                    const on = selected.includes(i.id)
                    return (
                      <li key={i.id}>
                        <label
                          className={cn(
                            'flex cursor-pointer items-center gap-2.5 rounded-lg px-2.5 py-2 transition-colors',
                            on ? 'bg-primary/10' : 'hover:bg-accent/60',
                          )}
                        >
                          <input type="checkbox" checked={on} onChange={(e) => toggleSel(i.id, e.target.checked)} />
                          <span
                            className="size-1.5 shrink-0 rounded-full"
                            style={{ backgroundColor: statusColorVar(instanceStatusLevel(i.status)) }}
                          />
                          <span className="min-w-0 flex-1 truncate text-sm">{i.name}</span>
                          <span className="shrink-0 text-[11px] text-muted-foreground">
                            {roleLabel((i as { role?: string }).role || 'universal')}
                            {' · '}
                            {nodeName(i.nodeId)}
                            {i.serverPort ? ` · :${i.serverPort}` : ''}
                          </span>
                        </label>
                      </li>
                    )
                  })}
                </ul>
              )}
            </div>
            <div className="flex shrink-0 justify-end border-t p-3">
              <button
                onClick={addSelected}
                disabled={selected.length === 0 || addMembers.isPending}
                className="rounded-md bg-primary px-3 py-1.5 text-xs text-primary-foreground transition-colors hover:bg-primary/90 disabled:opacity-50"
              >
                {t('networks.addSelected', { count: selected.length })}
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
