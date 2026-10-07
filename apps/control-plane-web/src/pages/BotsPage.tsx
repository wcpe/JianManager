import { useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import {
  useBots,
  useBot,
  useBotEvents,
  useBotSummary,
  useBotBatch,
  useCreateBot,
  useCreateBotStressSession,
  useSendBotCommand,
  type BotInfo,
  type BotRealtimeEvent,
  type BotSummaryGroup,
  type BotBatchAction,
  type BotListParams,
} from '@/api/bots'
import { useNodes } from '@/api/nodes'
import { useDebounced } from '@/lib/use-debounced'
import { useTabParam } from '@/lib/use-tab-param'
import { BOTS_TABS, type BotsTab } from '@/lib/bot-load/url-state'
import BotLoadSessionsTab from '@/pages/bots/BotLoadSessionsTab'
import BotLoadTemplatesTab from '@/pages/bots/BotLoadTemplatesTab'
import BotLoadWizard from '@/components/bot-load/BotLoadWizard'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@jianmanager/ui/components/tabs'
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
import { BotHealthBar } from '@jianmanager/ui/components/views/console/BotHealthBar'
import {
  BehaviorConfigDialog,
  BotMetric,
  formatBotEvent,
  formatEventTime,
  formatPosition,
  LegendDot,
  PeekRow,
  SummaryCards,
} from '@jianmanager/ui/components/views/bots/BotListParts'
import { BotToolbar } from '@jianmanager/ui/components/views/bots/BotToolbar'
import { BotBatchBar } from '@jianmanager/ui/components/views/bots/BotBatchBar'
import { BotGroupOverview } from '@jianmanager/ui/components/views/bots/BotGroupOverview'
import { BotStressSessionDialog } from '@jianmanager/ui/components/views/bots/BotStressSessionDialog'
import { BotDetailDialog as BotDetailDialogView } from '@jianmanager/ui/components/views/bots/BotDetailDialog'
import { useDangerPermission } from '@/lib/danger'
import { BotWorktableCard } from '@/components/console/BotWorktableCard'
import DangerConfirm from '@/components/DangerConfirm'
import { ViewToggle, type ViewMode } from '@jianmanager/ui/components/view-toggle'
import { Activity, Plus, Send } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Input } from '@jianmanager/ui/components/input'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { scrollableDialogContentClass, ScrollableDialogBody } from '@jianmanager/ui/components/scrollable-dialog'
import { InstancePicker } from '@/components/InstancePicker'
import CreateBotDialogView from '@jianmanager/ui/components/views/instances/CreateBotDialog'
import { FieldLabel, FieldError } from '@jianmanager/ui/components/field-label'
import { validateRequired, validateHost, validatePort, validateFields, hasErrors } from '@/lib/form-validation'
import { useFieldGate } from '@/lib/use-field-gate'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import { cn } from '@jianmanager/ui'

/**
 * Bot 详情弹窗的取数接线层（ADR-097）：视图已入包，此处注入元数据、实时流与命令下发。
 */
function BotDetailDialog({ botId, onOpenChange }: { botId: number | null; onOpenChange: (open: boolean) => void }) {
  const { data: bot } = useBot(botId ?? 0)
  const realtime = useBotEvents(botId)
  const sendCommand = useSendBotCommand()
  return (
    <BotDetailDialogView
      botId={botId}
      onOpenChange={onOpenChange}
      bot={bot}
      realtime={realtime}
      onSendCommand={async (command) => {
        await sendCommand.mutateAsync({ id: botId ?? 0, command })
      }}
      sending={sendCommand.isPending}
      onNotify={(level, message) => (level === 'success' ? toast.success(message) : toast.error(message))}
    />
  )
}

const SENTINEL_ALL = 'all'

const BEHAVIOR_OPTIONS = ['idle', 'guard', 'follow', 'patrol'] as const


/**
 * 全局 Bot 管理页（FR-040 / ADR-009 + FR-371）。
 * URL 可寻址 tab：fleet / sessions / templates；旧链接默认 fleet。
 * 舰队 tab 保持聚合优先、永不全量铺开；会话与模板由 FR-371 承接，详情页由 FR-372。
 */
export default function BotsPage() {
  const { t } = useTranslation()
  const [tab, setTab] = useTabParam<BotsTab>('tab', 'fleet', BOTS_TABS)
  const [showWizard, setShowWizard] = useState(false)

  return (
    // 全量对齐：外壳与页头改用布局层原语。原为裸 <div> + 手写页头，且无 data-page。
    // 「创建运行」按钮按 tab 条件显示（仅 sessions/templates 下有意义）——PageHeader 的
    // actions 对 falsy 会短路，故该条件可原样保留。
    <PageShell data-page="bots">
      <PageHeader
        title={t('bots.title')}
        actions={
          (tab === 'sessions' || tab === 'templates') && (
            <Button variant="outline" onClick={() => setShowWizard(true)}>
              {t('botsLoad.createRun')}
            </Button>
          )
        }
      />

      <Tabs value={tab} onValueChange={setTab} className="w-full">
        <TabsList className="mb-4">
          <TabsTrigger value="fleet">{t('botsLoad.tabFleet')}</TabsTrigger>
          <TabsTrigger value="sessions">{t('botsLoad.tabSessions')}</TabsTrigger>
          <TabsTrigger value="templates">{t('botsLoad.tabTemplates')}</TabsTrigger>
        </TabsList>
        <TabsContent value="fleet">
          <BotFleetTab />
        </TabsContent>
        <TabsContent value="sessions">
          <BotLoadSessionsTab />
        </TabsContent>
        <TabsContent value="templates">
          <BotLoadTemplatesTab />
        </TabsContent>
      </Tabs>

      <BotLoadWizard open={showWizard} onOpenChange={setShowWizard} />
    </PageShell>
  )
}

/**
 * 舰队 tab：聚合优先、永不全量铺开（FR-040 行为保持）。
 * 页顶概览 + 分组总览；展开才分页窥视；批量经 useBotBatch 委托。
 */
function BotFleetTab() {
  const { t } = useTranslation()
  const [showCreate, setShowCreate] = useState(false)
  const [showStress, setShowStress] = useState(false)
  const [search, setSearch] = useState('')
  const [nodeId, setNodeId] = useState<number | null>(null)
  const [status, setStatus] = useState<string>('')
  const [groupBy, setGroupBy] = useState<GroupByDim>('instance')
  const [detailBotId, setDetailBotId] = useState<number | null>(null)
  // 工作台卡 ⇄ 列表视图（FR-147，§4.5）；运行实体默认卡片。
  const [view, setView] = useState<ViewMode>('card')
  // 节点筛选数据源（原在 Toolbar 内取数，视图入包后上提到容器）。
  const { data: nodes } = useNodes()
  // 分组总览的批量与门禁（原在 GroupOverview 内，视图入包后上提到容器）。
  const batch = useBotBatch()
  const { allowed: dangerAllowed } = useDangerPermission('group')
  // 压测会话创建（原在 StressSessionDialog 内，视图入包后上提到容器）。
  const createStressSession = useCreateBotStressSession()

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
      <div className="mb-3 flex justify-end gap-2">
        <Button variant="outline" onClick={() => setShowStress(true)}>
          {t('bots.stressTest')}
        </Button>
        <Button onClick={() => setShowCreate(true)}>
          <Plus className="size-4" /> {t('bots.createBot')}
        </Button>
      </div>

      <SummaryCards
        counts={counts}
        dist={dist}
        loading={globalSummary.isLoading}
        fleetTotal={fleetTotal}
        byStatus={byStatus}
      />

      <BotToolbar
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
        nodes={nodes}
      />

      {/* key=groupBy：维度切换时重挂 BotGroupOverview，自然复位其展开/选择状态（避免 effect 内 setState） */}
      <BotGroupOverview
        key={groupBy}
        groupBy={groupBy}
        groups={groups}
        loading={activeSummary.isLoading}
        view={view}
        renderBatchBar={({ selectedGroups, onClear }) => (
          <BotBatchBar
            groupBy={groupBy}
            groups={selectedGroups}
            baseFilter={filter}
            onClear={onClear}
            onBatch={(body) => batch.mutateAsync(body)}
            batchPending={batch.isPending}
            dangerAllowed={dangerAllowed}
            onNotify={(level, message) => (level === 'success' ? toast.success(message) : toast.error(message))}
          />
        )}
        renderCard={({ group, checked, onCheck, expanded, onToggleExpand }) => (
          <BotWorktableCard
            groupBy={groupBy}
            group={group}
            checked={checked}
            onCheck={onCheck}
            expanded={expanded}
            onToggleExpand={onToggleExpand}
            actions={<GroupActions groupBy={groupBy} group={group} baseFilter={filter} />}
          >
            <GroupPeek params={groupFilter(groupBy, group, filter)} onOpenBot={setDetailBotId} />
          </BotWorktableCard>
        )}
        renderRow={({ group, checked, onCheck, expanded, onToggleExpand }) => (
          <GroupRow
            groupBy={groupBy}
            group={group}
            baseFilter={filter}
            checked={checked}
            onCheck={onCheck}
            expanded={expanded}
            onToggleExpand={onToggleExpand}
            onOpenBot={setDetailBotId}
          />
        )}
      />

      <CreateBotDialogContainer open={showCreate} onOpenChange={setShowCreate} />
      <BotStressSessionDialog
        open={showStress}
        onOpenChange={setShowStress}
        onCreate={(body) => createStressSession.mutateAsync(body)}
        submitting={createStressSession.isPending}
        renderInstancePicker={(args) => <InstancePicker {...args} />}
      />
      <BotDetailDialog botId={detailBotId} onOpenChange={(open) => { if (!open) setDetailBotId(null) }} />
    </div>
  )
}

/** 页顶概览卡片：总计/在线/连接中/异常 + 分布（X 实例·Y 节点）+ 舰队健康条（多段）。 */



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
  const openInConsole = () => navigate(`/instances/${group.key}`)
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
  onOpenBot,
}: {
  groupBy: GroupByDim
  group: BotSummaryGroup
  baseFilter: OverviewFilter
  checked: boolean
  onCheck: () => void
  expanded: boolean
  onToggleExpand: () => void
  onOpenBot: (id: number) => void
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
            <GroupPeek params={groupFilter(groupBy, group, baseFilter)} onOpenBot={onOpenBot} />
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

/** 行为参数化配置对话框（FR-147）：跟随=目标玩家名，巡逻=路径点（逗号分隔坐标）。 */

/** 展开窥视：仅拉该组首页 Bot（分页，绝不全量），用于核对组内成员。 */
function GroupPeek({ params, onOpenBot }: { params: BotListParams; onOpenBot: (id: number) => void }) {
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
          <PeekRow key={bot.id} bot={bot} onOpen={() => onOpenBot(bot.id)} />
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




/** 新建 Bot 弹窗的取数接线层（ADR-097）：视图已入包，此处注入实例选择与创建 mutation。 */
function CreateBotDialogContainer({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const { t } = useTranslation()
  const create = useCreateBot()
  const [instanceId, setInstanceId] = useState<number | null>(null)
  const [suggestedServer, setSuggestedServer] = useState('')
  const [suggestedPort, setSuggestedPort] = useState(25565)

  return (
    <CreateBotDialogView
      open={open}
      onOpenChange={onOpenChange}
      suggestedServer={suggestedServer}
      suggestedPort={suggestedPort}
      creating={create.isPending}
      instanceId={instanceId}
      instanceSelected={instanceId !== null}
      onInstancePick={(id, inst) => {
        setInstanceId(id)
        if (inst) {
          setSuggestedServer('127.0.0.1')
          setSuggestedPort(inst.serverPort && inst.serverPort > 0 ? inst.serverPort : 25565)
        }
      }}
      renderInstancePicker={(args) => (
        <InstancePicker {...args} enabled={open} placeholder={t('bots.selectInstance')} />
      )}
      onCreate={async (payload) => {
        try {
          await create.mutateAsync({
            instanceId: instanceId ?? 0,
            name: payload.name,
            config: { server: payload.server, port: payload.port, auth: payload.auth },
            behavior: payload.behavior,
          })
          return { ok: true }
        } catch (err) {
          const msg = (err as { response?: { data?: { message?: string } } })?.response?.data?.message
          return { ok: false, error: msg || t('bots.createFailed') }
        }
      }}
    />
  )
}
