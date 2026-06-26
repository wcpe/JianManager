import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import {
  useBots,
  useBotSummary,
  useBotBatch,
  useCreateBot,
  type BotInfo,
  type BotSummaryGroup,
  type BotBatchAction,
  type BotListParams,
} from '@/api/bots'
import { useInstances } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useConsoleStore } from '@/stores/console'
import {
  statusCounts,
  toListParams,
  groupFilter,
  distribution,
  GROUP_BY_DIMS,
  BOT_STATUSES,
  type GroupByDim,
  type OverviewFilter,
  type BotStatusCounts,
  type Distribution,
} from './bots-overview'
import { BotHealthBar } from '@/components/console/BotHealthBar'
import { BotWorktableCard } from '@/components/console/BotWorktableCard'
import DangerConfirm from '@/components/DangerConfirm'
import { ViewToggle, type ViewMode } from '@/components/ui/view-toggle'
import { Plus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@/components/ui/scrollable-dialog'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { FieldLabel, FieldError } from '@/components/ui/field-label'
import { validateRequired, validateHost, validatePort, validateFields, hasErrors } from '@/lib/form-validation'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { cn } from '@/lib/utils'

const SENTINEL_ALL = 'all'

const BEHAVIOR_OPTIONS = ['idle', 'guard', 'follow', 'patrol'] as const

/**
 * 全局 Bot 管理页（FR-040 / ADR-009）。
 * 聚合优先、永不全量铺开：页顶概览卡片 + 分组总览（默认按实例），每组一行（实例/节点/健康条/总数/批量），
 * 展开才分页窥视该组首页 Bot；批量经 useBotBatch 按筛选委托；「在控制台打开」回到控制台工作区。
 */
export default function BotsPage() {
  const { t } = useTranslation()
  const [showCreate, setShowCreate] = useState(false)
  const [search, setSearch] = useState('')
  const [nodeId, setNodeId] = useState<number | null>(null)
  const [status, setStatus] = useState<string>('')
  const [groupBy, setGroupBy] = useState<GroupByDim>('instance')
  // 工作台卡 ⇄ 列表视图（FR-147，§4.5）；运行实体默认卡片。
  const [view, setView] = useState<ViewMode>('card')

  const debouncedSearch = useDebounced(search, 300)
  const filter: OverviewFilter = useMemo(
    () => ({
      q: debouncedSearch.trim() || undefined,
      nodeId: nodeId ?? undefined,
      status: status || undefined,
    }),
    [debouncedSearch, nodeId, status],
  )
  const baseParams = useMemo(() => toListParams(filter), [filter])

  // 全局概览：无 groupBy → total + byStatus（受工具栏筛选影响，便于「筛选后看分布」）
  const globalSummary = useBotSummary(baseParams)
  // 分布计数 + 实例/节点维度总览（一并取，分组维度切换时无需重查）
  const instanceSummary = useBotSummary({ ...baseParams, groupBy: 'instance' })
  const nodeSummary = useBotSummary({ ...baseParams, groupBy: 'node' })
  const statusSummary = useBotSummary({ ...baseParams, groupBy: 'status' })
  const behaviorSummary = useBotSummary({ ...baseParams, groupBy: 'behavior' })

  const summaryByDim: Record<GroupByDim, typeof instanceSummary> = {
    instance: instanceSummary,
    node: nodeSummary,
    status: statusSummary,
    behavior: behaviorSummary,
  }
  const activeSummary = summaryByDim[groupBy]

  const counts = statusCounts(globalSummary.data)
  const dist = distribution(instanceSummary.data, nodeSummary.data)
  const groups = activeSummary.data?.groups ?? []
  // 全局各状态精确计数，供舰队健康条多段着色（FR-147）。
  const byStatus = globalSummary.data?.byStatus
  const fleetTotal = globalSummary.data?.total ?? 0

  return (
    <div>
      <div className="mb-4 flex items-center justify-between">
        <h1 className="text-2xl font-bold">{t('bots.title')}</h1>
        <div className="flex gap-2">
          <Button variant="outline" disabled title={t('bots.stressTestSoon')}>
            {t('bots.stressTest')}
          </Button>
          <Button onClick={() => setShowCreate(true)}>
            <Plus className="size-4" /> {t('bots.createBot')}
          </Button>
        </div>
      </div>

      <SummaryCards
        counts={counts}
        dist={dist}
        loading={globalSummary.isLoading}
        fleetTotal={fleetTotal}
        byStatus={byStatus}
      />

      <Toolbar
        search={search}
        onSearch={setSearch}
        nodeId={nodeId}
        onNode={setNodeId}
        status={status}
        onStatus={setStatus}
        groupBy={groupBy}
        onGroupBy={setGroupBy}
        view={view}
        onView={setView}
      />

      {/* key=groupBy：维度切换时重挂 GroupOverview，自然复位其展开/选择状态（避免 effect 内 setState） */}
      <GroupOverview
        key={groupBy}
        groupBy={groupBy}
        groups={groups}
        baseFilter={filter}
        loading={activeSummary.isLoading}
        view={view}
      />

      <CreateBotDialog open={showCreate} onOpenChange={setShowCreate} />
    </div>
  )
}

/** 防抖：value 停止变化 delay 毫秒后才更新返回值，用于搜索输入。 */
function useDebounced<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const id = setTimeout(() => setDebounced(value), delay)
    return () => clearTimeout(id)
  }, [value, delay])
  return debounced
}

/** 页顶概览卡片：总计/在线/连接中/异常 + 分布（X 实例·Y 节点）+ 舰队健康条（多段）。 */
function SummaryCards({
  counts,
  dist,
  loading,
  fleetTotal,
  byStatus,
}: {
  counts: BotStatusCounts
  dist: Distribution
  loading: boolean
  /** 舰队 Bot 总数（健康条分母）。 */
  fleetTotal: number
  /** 各状态精确计数（健康条多段着色）。 */
  byStatus?: Record<string, number>
}) {
  const { t } = useTranslation()
  const cards = [
    { key: 'total', label: t('bots.total'), value: counts.total, color: 'text-foreground' },
    { key: 'online', label: t('bots.online'), value: counts.online, color: 'text-status-success' },
    { key: 'connecting', label: t('bots.connecting'), value: counts.connecting, color: 'text-status-warning' },
    { key: 'error', label: t('bots.abnormal'), value: counts.error, color: 'text-status-danger' },
  ]
  return (
    <div className="mb-4 space-y-3">
      <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
        {cards.map((card) => (
          <div key={card.key} className="rounded-xl border bg-card p-4 shadow-soft">
            <p className="text-sm text-muted-foreground">{card.label}</p>
            <p className={cn('mt-1 text-2xl font-bold tabular-nums', card.color)}>{loading ? '—' : card.value}</p>
            {card.key === 'total' && (
              <p className="mt-1 text-xs text-muted-foreground">
                {t('bots.distribution', { instances: dist.instances, nodes: dist.nodes })}
              </p>
            )}
          </div>
        ))}
      </div>
      {/* 舰队健康条（FR-147）：按全局 byStatus 多段着色 connected/connecting/error/stopped */}
      {fleetTotal > 0 && (
        <div className="rounded-xl border bg-card p-3 shadow-soft">
          <div className="mb-2 flex items-center gap-2 text-xs text-muted-foreground">
            <span>{t('bots.fleetHealth')}</span>
            <LegendDot className="bg-status-success" label={t('bots.statusKind.online')} />
            <LegendDot className="bg-status-warning" label={t('bots.statusKind.connecting')} />
            <LegendDot className="bg-status-danger" label={t('bots.statusKind.error')} />
            <LegendDot className="bg-muted-foreground/40" label={t('bots.status_stopped')} />
          </div>
          <BotHealthBar total={fleetTotal} online={counts.online} byStatus={byStatus} />
        </div>
      )}
    </div>
  )
}

/** 图例点：色块 + 文案，用于舰队健康条说明各段语义。 */
function LegendDot({ className, label }: { className: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1">
      <span className={cn('size-2 rounded-full', className)} />
      {label}
    </span>
  )
}

/** 工具栏：搜索 + 节点筛选 + 状态筛选 + 分组维度切换 + 卡/列表视图切换。 */
function Toolbar({
  search,
  onSearch,
  nodeId,
  onNode,
  status,
  onStatus,
  groupBy,
  onGroupBy,
  view,
  onView,
}: {
  search: string
  onSearch: (v: string) => void
  nodeId: number | null
  onNode: (v: number | null) => void
  status: string
  onStatus: (v: string) => void
  groupBy: GroupByDim
  onGroupBy: (v: GroupByDim) => void
  view: ViewMode
  onView: (v: ViewMode) => void
}) {
  const { t } = useTranslation()
  const { data: nodes } = useNodes()

  return (
    <div className="mb-3 flex flex-wrap items-center gap-2">
      <Input
        value={search}
        onChange={(e) => onSearch(e.target.value)}
        placeholder={t('bots.searchPlaceholder')}
        className="h-9 w-56"
      />
      <Select
        value={nodeId === null ? SENTINEL_ALL : String(nodeId)}
        onValueChange={(v: string) => onNode(v === SENTINEL_ALL ? null : Number(v))}
      >
        <SelectTrigger size="sm" className="w-40">
          <SelectValue placeholder={t('bots.allNodes')} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={SENTINEL_ALL}>{t('bots.allNodes')}</SelectItem>
          {nodes?.map((node) => (
            <SelectItem key={node.id} value={String(node.id)}>
              {node.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select
        value={status === '' ? SENTINEL_ALL : status}
        onValueChange={(v: string) => onStatus(v === SENTINEL_ALL ? '' : v)}
      >
        <SelectTrigger size="sm" className="w-36">
          <SelectValue placeholder={t('bots.allStatus')} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={SENTINEL_ALL}>{t('bots.allStatus')}</SelectItem>
          {BOT_STATUSES.map((s) => (
            <SelectItem key={s} value={s}>
              {t(`bots.status_${s}`)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>

      <div className="ml-auto flex items-center gap-2">
        <div className="flex items-center gap-1 rounded-md border p-0.5">
          <span className="px-2 text-xs text-muted-foreground">{t('bots.groupBy')}</span>
          {GROUP_BY_DIMS.map((dim) => (
            <Button
              key={dim}
              type="button"
              size="xs"
              variant={groupBy === dim ? 'default' : 'ghost'}
              onClick={() => onGroupBy(dim)}
            >
              {t(`bots.groupDim_${dim}`)}
            </Button>
          ))}
        </div>
        <ViewToggle
          value={view}
          onChange={onView}
          cardLabel={t('grouping.viewCard')}
          listLabel={t('grouping.viewList')}
        />
      </div>
    </div>
  )
}

/** 分组总览：每组一卡/行（含多段健康条 + 总数 + 批量 + 展开窥视 + 在控制台打开）。 */
function GroupOverview({
  groupBy,
  groups,
  baseFilter,
  loading,
  view,
}: {
  groupBy: GroupByDim
  groups: BotSummaryGroup[]
  baseFilter: OverviewFilter
  loading: boolean
  view: ViewMode
}) {
  const { t } = useTranslation()
  const [expanded, setExpanded] = useState<string | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())

  const selectedGroups = useMemo(
    () => groups.filter((g) => selected.has(g.key)),
    [groups, selected],
  )

  const toggle = (key: string) =>
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  const toggleExpand = (key: string) => setExpanded((cur) => (cur === key ? null : key))

  if (loading) {
    return <p className="text-muted-foreground">{t('common.loading')}</p>
  }

  return (
    <div className="space-y-3">
      {selectedGroups.length > 0 && (
        <BatchBar
          groupBy={groupBy}
          groups={selectedGroups}
          baseFilter={baseFilter}
          onClear={() => setSelected(new Set())}
        />
      )}

      {groups.length === 0 ? (
        <p className="rounded-lg border py-10 text-center text-muted-foreground">{t('bots.empty')}</p>
      ) : view === 'card' ? (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {groups.map((group) => (
            <BotWorktableCard
              key={group.key}
              groupBy={groupBy}
              group={group}
              checked={selected.has(group.key)}
              onCheck={() => toggle(group.key)}
              expanded={expanded === group.key}
              onToggleExpand={() => toggleExpand(group.key)}
              actions={<GroupActions groupBy={groupBy} group={group} baseFilter={baseFilter} />}
            >
              <GroupPeek params={groupFilter(groupBy, group, baseFilter)} />
            </BotWorktableCard>
          ))}
        </div>
      ) : (
        <div className="rounded-lg border">
          <Table>
            <TableHeader className="bg-muted/50">
              <TableRow>
                <TableHead className="w-10" />
                <TableHead>{t(`bots.groupDim_${groupBy}`)}</TableHead>
                <TableHead className="w-[34%]">{t('bots.health')}</TableHead>
                <TableHead className="w-20 text-right">{t('bots.count')}</TableHead>
                <TableHead className="w-44 text-right">{t('bots.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {groups.map((group) => (
                <GroupRow
                  key={group.key}
                  groupBy={groupBy}
                  group={group}
                  baseFilter={baseFilter}
                  checked={selected.has(group.key)}
                  onCheck={() => toggle(group.key)}
                  expanded={expanded === group.key}
                  onToggleExpand={() => toggleExpand(group.key)}
                />
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  )
}

/** 分组操作区（卡片/行复用）：在控制台打开（仅实例维度）+ 单组批量菜单。 */
function GroupActions({
  groupBy,
  group,
  baseFilter,
}: {
  groupBy: GroupByDim
  group: BotSummaryGroup
  baseFilter: OverviewFilter
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const openInstance = useConsoleStore((s) => s.openInstance)
  const openInConsole = () => {
    openInstance(Number(group.key))
    navigate('/')
  }
  return (
    <>
      {groupBy === 'instance' && (
        <Button variant="ghost" size="xs" onClick={openInConsole}>
          {t('bots.openInConsole')}
        </Button>
      )}
      <GroupBatchMenu groupBy={groupBy} group={group} baseFilter={baseFilter} />
    </>
  )
}

/** 单个分组行（列表视图）：勾选 + 标签 + 健康条 + 总数 + 操作（批量/在控制台打开/展开）。 */
function GroupRow({
  groupBy,
  group,
  baseFilter,
  checked,
  onCheck,
  expanded,
  onToggleExpand,
}: {
  groupBy: GroupByDim
  group: BotSummaryGroup
  baseFilter: OverviewFilter
  checked: boolean
  onCheck: () => void
  expanded: boolean
  onToggleExpand: () => void
}) {
  const { t } = useTranslation()

  return (
    <>
      <TableRow>
        <TableCell>
          <Checkbox checked={checked} onCheckedChange={onCheck} aria-label={t('bots.select')} />
        </TableCell>
        <TableCell>
          <button
            type="button"
            onClick={onToggleExpand}
            className="flex items-center gap-1.5 text-left font-medium hover:underline"
          >
            <span className="text-muted-foreground">{expanded ? '▾' : '▸'}</span>
            <span className="truncate">{group.label || group.key}</span>
          </button>
        </TableCell>
        <TableCell>
          <BotHealthBar total={group.total} online={group.online} />
        </TableCell>
        <TableCell className="text-right tabular-nums">{group.total}</TableCell>
        <TableCell>
          <div className="flex items-center justify-end gap-1">
            <GroupActions groupBy={groupBy} group={group} baseFilter={baseFilter} />
          </div>
        </TableCell>
      </TableRow>
      {expanded && (
        <TableRow className="bg-muted/30 hover:bg-muted/30">
          <TableCell colSpan={5} className="p-0">
            <GroupPeek params={groupFilter(groupBy, group, baseFilter)} />
          </TableCell>
        </TableRow>
      )}
    </>
  )
}

/** 每组批量操作菜单：设行为 / 停止 / 删除（经 useBotBatch，目标=该组筛选）。 */
function GroupBatchMenu({
  groupBy,
  group,
  baseFilter,
}: {
  groupBy: GroupByDim
  group: BotSummaryGroup
  baseFilter: OverviewFilter
}) {
  const { t } = useTranslation()
  const batch = useBotBatch()
  const params = groupFilter(groupBy, group, baseFilter)

  const run = (action: BotBatchAction, behavior?: string) => {
    batch.mutate(
      { action, filter: params, behavior },
      {
        onSuccess: (res) =>
          toast.success(t('bots.batchDone', { succeeded: res.succeeded, failed: res.failed })),
        onError: () => toast.error(t('bots.batchFailed')),
      },
    )
  }

  return (
    <Select
      value=""
      onValueChange={(v: string) => {
        if (v.startsWith('behavior:')) run('set-behavior', v.slice('behavior:'.length))
        else run(v as BotBatchAction)
      }}
    >
      <SelectTrigger size="sm" className="w-28" disabled={batch.isPending}>
        <SelectValue placeholder={t('bots.batch')} />
      </SelectTrigger>
      <SelectContent>
        {BEHAVIOR_OPTIONS.map((b) => (
          <SelectItem key={b} value={`behavior:${b}`}>
            {t('bots.setBehaviorTo', { behavior: t(`bots.${b}`) })}
          </SelectItem>
        ))}
        <SelectItem value="stop">{t('bots.batchStop')}</SelectItem>
        <SelectItem value="delete">{t('bots.batchDelete')}</SelectItem>
      </SelectContent>
    </Select>
  )
}

/** 行为是否需要目标参数（巡逻路径 / 跟随目标）。 */
const BEHAVIOR_NEEDS_TARGET = new Set(['follow', 'patrol'])

/**
 * 顶部批量条：对已勾选的多个分组逐组下发批量（每组一次调用，聚合结果）。
 * 删除前 DangerConfirm 二次确认 + 串行进度提示（FR-147）；选 follow/patrol 行为时旁置「配置」入口
 * 暴露目标参数（跟随目标 / 巡逻路径），随 set-behavior 一并下发（useBotBatch.target）。
 */
function BatchBar({
  groupBy,
  groups,
  baseFilter,
  onClear,
}: {
  groupBy: GroupByDim
  groups: BotSummaryGroup[]
  baseFilter: OverviewFilter
  onClear: () => void
}) {
  const { t } = useTranslation()
  const batch = useBotBatch()
  const [behavior, setBehavior] = useState<string>('')
  const [target, setTarget] = useState<string>('')
  const [showConfig, setShowConfig] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  // 串行进度：已处理组数 / 总组数（null=未在进行）。
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null)

  const totalSelected = groups.reduce((sum, g) => sum + g.total, 0)
  const needsTarget = BEHAVIOR_NEEDS_TARGET.has(behavior)

  // 逐组下发同一动作（后端批量按单一 filter 收敛，多组需多次调用），聚合成功/失败计数，逐组报进度
  const runAll = async (action: BotBatchAction, beh?: string, tgt?: string) => {
    let succeeded = 0
    let failed = 0
    setProgress({ done: 0, total: groups.length })
    for (let i = 0; i < groups.length; i++) {
      const g = groups[i]
      try {
        const res = await batch.mutateAsync({
          action,
          filter: groupFilter(groupBy, g, baseFilter),
          behavior: beh,
          target: tgt,
        })
        succeeded += res.succeeded
        failed += res.failed
      } catch {
        failed += g.total
      }
      setProgress({ done: i + 1, total: groups.length })
    }
    setProgress(null)
    toast.success(t('bots.batchDone', { succeeded, failed }))
    onClear()
  }

  const busy = batch.isPending || progress !== null

  return (
    <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-muted/40 p-2">
      <span className="text-sm font-medium">
        {t('bots.selectedGroups', { groups: groups.length, bots: totalSelected })}
      </span>
      {progress && (
        <span className="text-xs text-muted-foreground tabular-nums">
          {t('bots.batchProgress', { done: progress.done, total: progress.total })}
        </span>
      )}
      <div className="ml-auto flex items-center gap-2">
        <Select
          value={behavior}
          onValueChange={(v) => {
            setBehavior(v)
            // 切到不需目标的行为时清空已填目标，避免误带。
            if (!BEHAVIOR_NEEDS_TARGET.has(v)) setTarget('')
          }}
        >
          <SelectTrigger size="sm" className="w-32">
            <SelectValue placeholder={t('bots.setBehavior')} />
          </SelectTrigger>
          <SelectContent>
            {BEHAVIOR_OPTIONS.map((b) => (
              <SelectItem key={b} value={b}>
                {t(`bots.${b}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {needsTarget && (
          <Button size="sm" variant="outline" onClick={() => setShowConfig(true)}>
            {t('bots.behaviorConfig')}
            {target && <span className="ml-1 max-w-24 truncate text-xs text-muted-foreground">· {target}</span>}
          </Button>
        )}
        <Button
          size="sm"
          variant="outline"
          disabled={!behavior || busy || (needsTarget && !target)}
          title={needsTarget && !target ? t('bots.behaviorTargetRequired') : undefined}
          onClick={() => runAll('set-behavior', behavior, target || undefined)}
        >
          {t('bots.apply')}
        </Button>
        <Button size="sm" variant="outline" disabled={busy} onClick={() => runAll('stop')}>
          {t('bots.batchStop')}
        </Button>
        <Button
          size="sm"
          variant="destructive"
          disabled={busy}
          onClick={() => setConfirmDelete(true)}
        >
          {t('bots.batchDelete')}
        </Button>
        <Button size="sm" variant="ghost" disabled={busy} onClick={onClear}>
          {t('common.cancel')}
        </Button>
      </div>

      {/* 行为目标配置（跟随目标玩家名 / 巡逻路径点），随 set-behavior 一并下发 */}
      <BehaviorConfigDialog
        open={showConfig}
        behavior={behavior}
        target={target}
        onApply={(v) => {
          setTarget(v)
          setShowConfig(false)
        }}
        onClose={() => setShowConfig(false)}
      />

      {/* 多组批量删除二次确认（FR-147） */}
      <DangerConfirm
        open={confirmDelete}
        title={t('bots.batchDeleteTitle')}
        description={t('bots.batchDeleteConfirm', { count: totalSelected })}
        confirmLabel={t('bots.batchDelete')}
        scope="group"
        onConfirm={() => {
          setConfirmDelete(false)
          runAll('delete')
        }}
        onCancel={() => setConfirmDelete(false)}
      />
    </div>
  )
}

/** 行为参数化配置对话框（FR-147）：跟随=目标玩家名，巡逻=路径点（逗号分隔坐标）。 */
function BehaviorConfigDialog({
  open,
  behavior,
  target,
  onApply,
  onClose,
}: {
  open: boolean
  behavior: string
  target: string
  onApply: (target: string) => void
  onClose: () => void
}) {
  const { t } = useTranslation()
  const [value, setValue] = useState(target)
  const [prevOpen, setPrevOpen] = useState(open)
  // 打开时以当前 target 回填（渲染期同步，避免 effect）。
  if (open !== prevOpen) {
    setPrevOpen(open)
    if (open) setValue(target)
  }

  const isFollow = behavior === 'follow'
  const label = isFollow ? t('bots.followTarget') : t('bots.patrolPath')
  const placeholder = isFollow ? t('bots.followTargetPlaceholder') : t('bots.patrolPathPlaceholder')

  return (
    <Dialog open={open} onOpenChange={(v) => { if (!v) onClose() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t('bots.behaviorConfigTitle', { behavior: t(`bots.${behavior}`, behavior) })}</DialogTitle>
        </DialogHeader>
        <div className="space-y-2 py-1">
          <FieldLabel>{label}</FieldLabel>
          <Input value={value} onChange={(e) => setValue(e.target.value)} placeholder={placeholder} autoFocus />
          <p className="text-xs text-muted-foreground">
            {isFollow ? t('bots.followTargetHint') : t('bots.patrolPathHint')}
          </p>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => onApply(value.trim())}>{t('common.save')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** 展开窥视：仅拉该组首页 Bot（分页，绝不全量），用于核对组内成员。 */
function GroupPeek({ params }: { params: BotListParams }) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const peekSize = 10
  const { data, isLoading } = useBots({ ...params, page, pageSize: peekSize })

  if (isLoading) {
    return <p className="px-4 py-3 text-sm text-muted-foreground">{t('common.loading')}</p>
  }
  const items = data?.items ?? []
  const total = data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / peekSize))

  if (items.length === 0) {
    return <p className="px-4 py-3 text-sm text-muted-foreground">{t('bots.empty')}</p>
  }

  return (
    <div className="px-4 py-3">
      <ul className="divide-y text-sm">
        {items.map((bot) => (
          <PeekRow key={bot.id} bot={bot} />
        ))}
      </ul>
      <div className="mt-2 flex items-center justify-between text-xs text-muted-foreground">
        <span>{t('bots.peekTotal', { total })}</span>
        <div className="flex items-center gap-2">
          <Button
            size="xs"
            variant="ghost"
            disabled={page <= 1}
            onClick={() => setPage((p) => Math.max(1, p - 1))}
          >
            {t('bots.prevPage')}
          </Button>
          <span>{t('bots.pageOf', { page, totalPages })}</span>
          <Button
            size="xs"
            variant="ghost"
            disabled={page >= totalPages}
            onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
          >
            {t('bots.nextPage')}
          </Button>
        </div>
      </div>
    </div>
  )
}

const STATUS_COLOR: Record<string, string> = {
  connected: 'text-green-500',
  connecting: 'text-amber-500',
  error: 'text-red-500',
  stopped: 'text-muted-foreground',
  pending: 'text-muted-foreground',
}

/** 窥视行：单个 Bot 的名称 / 状态 / 行为（只读，单 Bot 详情见 FR-041）。 */
function PeekRow({ bot }: { bot: BotInfo }) {
  const { t } = useTranslation()
  return (
    <li className="flex items-center justify-between py-1.5">
      <span className="truncate font-medium">{bot.name}</span>
      <div className="flex items-center gap-4">
        <span className={cn('text-xs', STATUS_COLOR[bot.status] ?? 'text-muted-foreground')}>
          {t(`bots.status_${bot.status}`, bot.status)}
        </span>
        <span className="w-16 text-right text-xs text-muted-foreground">
          {t(`bots.${bot.behavior}`, bot.behavior)}
        </span>
      </div>
    </li>
  )
}

interface CreateBotDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

/** 新建 Bot 对话框（沿用 FR-009 既有表单，复用 useCreateBot）。 */
function CreateBotDialog({ open, onOpenChange }: CreateBotDialogProps) {
  const { t } = useTranslation()
  const { data: instances } = useInstances()
  const create = useCreateBot()

  const [name, setName] = useState('')
  const [instanceId, setInstanceId] = useState('')
  const [server, setServer] = useState('')
  const [port, setPort] = useState('25565')
  const [auth, setAuth] = useState('offline')
  const [behavior, setBehavior] = useState('idle')
  const [error, setError] = useState('')

  const instanceOptions: ComboboxOption[] = (instances ?? []).map((inst) => ({
    value: String(inst.id),
    label: inst.name,
  }))

  const errors = validateFields(
    { name, instanceId, server, port },
    {
      name: [validateRequired],
      instanceId: [validateRequired],
      server: [validateRequired, validateHost],
      port: [validateRequired, validatePort],
    },
  )

  const resetForm = () => {
    setName('')
    setInstanceId('')
    setServer('')
    setPort('25565')
    setAuth('offline')
    setBehavior('idle')
    setError('')
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (hasErrors(errors)) return
    setError('')
    create.mutate(
      {
        instanceId: Number(instanceId),
        name,
        config: { server, port: Number(port), auth },
        behavior,
      },
      {
        onSuccess: () => {
          onOpenChange(false)
          resetForm()
        },
        onError: (err: unknown) => {
          const msg =
            err instanceof Error && 'response' in err
              ? (err as { response?: { data?: { message?: string } } }).response?.data?.message
              : undefined
          setError(msg || t('bots.createFailed'))
        },
      },
    )
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className={`${scrollableDialogContentClass} sm:max-w-md`}>
        <DialogHeader>
          <DialogTitle>{t('bots.createBot')}</DialogTitle>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <ScrollableDialogBody className="space-y-3 py-1">
            {error && (
              <div className="rounded bg-destructive/10 p-2 text-sm text-destructive">{error}</div>
            )}

            <div className="space-y-1">
              <FieldLabel required>{t('bots.name')}</FieldLabel>
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="GuardBot"
                aria-invalid={!!errors.name}
              />
              <FieldError error={errors.name} />
            </div>

            <div className="space-y-1">
              <FieldLabel required>{t('bots.instance')}</FieldLabel>
              <Combobox
                options={instanceOptions}
                value={instanceId}
                onChange={(v: string) => {
                  setInstanceId(v)
                  // 选实例即默认连到该实例（本机回环 + 实例实际端口），避免端口填错连不进
                  const inst = instances?.find((i) => String(i.id) === v)
                  if (inst) {
                    setServer('127.0.0.1')
                    setPort(String(inst.serverPort && inst.serverPort > 0 ? inst.serverPort : 25565))
                  }
                }}
                allowCustom={false}
                placeholder={t('bots.selectInstance')}
                invalid={!!errors.instanceId}
              />
              <FieldError error={errors.instanceId} />
            </div>

            <div className="grid grid-cols-3 gap-3">
              <div className="col-span-2 space-y-1">
                <FieldLabel required>{t('bots.serverAddr')}</FieldLabel>
                <Input
                  value={server}
                  onChange={(e) => setServer(e.target.value)}
                  placeholder="mc.example.com"
                  aria-invalid={!!errors.server}
                />
                <FieldError error={errors.server} />
              </div>
              <div className="space-y-1">
                <FieldLabel required>{t('bots.port')}</FieldLabel>
                <Input
                  value={port}
                  onChange={(e) => setPort(e.target.value)}
                  type="number"
                  aria-invalid={!!errors.port}
                />
                <FieldError error={errors.port} />
              </div>
            </div>

            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1">
                <FieldLabel>{t('bots.authMethod')}</FieldLabel>
                <Select value={auth} onValueChange={setAuth}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="offline">{t('bots.offline')}</SelectItem>
                    <SelectItem value="microsoft">{t('bots.microsoft')}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1">
                <FieldLabel>{t('bots.initialBehavior')}</FieldLabel>
                <Select value={behavior} onValueChange={setBehavior}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {BEHAVIOR_OPTIONS.map((b) => (
                      <SelectItem key={b} value={b}>
                        {t(`bots.${b}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>
          </ScrollableDialogBody>

          <DialogFooter className="pt-4">
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                onOpenChange(false)
                resetForm()
              }}
            >
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={create.isPending || hasErrors(errors)}>
              {create.isPending ? t('common.creating') : t('common.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
