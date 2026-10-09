// 分组总览的展示件已回迁应用侧（原 ADR-097 迁包已撤销）：分组操作区、分组行与成员窥视在包内；
// 本层保留取数（概览 summary / 窥视 bots / 批量 mutation）、路由跳转与提示。
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { useBots, useBot, useBotEvents, useBotSummary, useBotBatch, useCreateBot, useCreateBotStressSession, useSendBotCommand, type BotBatchAction, type BotListParams, type BotSummaryGroup } from '@/api/bots'
import { useNodes } from '@/api/nodes'
import { useDebounced } from '@/lib/use-debounced'
import { useTabParam } from '@/lib/use-tab-param'
import { BOTS_TABS } from '@/lib/bot-load-url-state'
import type { BotsTab } from '@/lib/bot-load-url-state'
import BotLoadSessionsTab from '@/pages/bots/BotLoadSessionsTab'
import BotLoadTemplatesTab from '@/pages/bots/BotLoadTemplatesTab'
import BotLoadWizard from '@/components/bot-load/BotLoadWizard'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@jianmanager/ui/components/tabs'
import { statusCounts, toListParams, groupFilter, distribution } from '@/lib/bots-overview'
import type { GroupByDim, OverviewFilter } from '@/lib/bots-overview'
import { SummaryCards } from '@/components/views/bots/BotListParts'
import { BotToolbar } from '@/components/views/bots/BotToolbar'
import { BotBatchBar } from '@/components/views/bots/BotBatchBar'
import { BotGroupOverview } from '@/components/views/bots/BotGroupOverview'
import { BOT_PEEK_PAGE_SIZE, BotGroupActions, BotGroupPeek, BotGroupRow } from '@/components/views/bots/BotGroupPartsView'
import { BotStressSessionDialog } from '@/components/views/bots/BotStressSessionDialog'
import { BotDetailDialog as BotDetailDialogView } from '@/components/views/bots/BotDetailDialog'
import { useDangerPermission } from '@/lib/danger'
import { BotWorktableCard } from '@/components/views/console/ConsoleLeafParts'
import type { ViewMode } from '@jianmanager/ui/components/view-toggle'
import { Plus } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { InstancePicker } from '@/components/instances/InstancePicker'
import CreateBotDialogView from '@/components/views/instances/CreateBotDialog'

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

/**
 * 分组窥视的取数接线层（ADR-097）：展示件已入包，此处只做参数展开、页码状态与成员取数。
 *
 * 页码会触发重新取数，故由本层持有（与列表页码同规则）；窥视随分组展开态挂载/卸载——
 * 收起即卸载、换组即重挂（行与卡都按 `group.key` 成键），页码自然复位为 1，与原页
 * 「各窥视件自持页码」的行为一致。
 */
function BotGroupPeekContainer({ params, onOpenBot }: { params: BotListParams; onOpenBot: (id: number) => void }) {
  const [page, setPage] = useState(1)
  const { data, isLoading } = useBots({ ...params, page, pageSize: BOT_PEEK_PAGE_SIZE })
  return (
    <BotGroupPeek
      items={data?.items}
      total={data?.total}
      isLoading={isLoading}
      page={page}
      onPage={setPage}
      onOpenBot={onOpenBot}
    />
  )
}

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
  // 分组操作区的「在控制台打开」要跳实例深链（原在 GroupActions 内取 navigate，视图入包后上提到容器）。
  const navigate = useNavigate()
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

  /**
   * 单组批量下发（FR-147）：目标 = 该组筛选，结果经 toast 回执（mutation 与提示都留在应用侧）。
   * 与原页一致：成功回执带成功/失败计数，失败只报动作失败。
   */
  const runGroupBatch = (group: BotSummaryGroup, action: BotBatchAction, behavior?: string) =>
    batch.mutate(
      { action, filter: groupFilter(groupBy, group, filter), behavior },
      {
        onSuccess: (res) =>
          toast.success(t('bots.batchDone', { succeeded: res.succeeded, failed: res.failed })),
        onError: () => toast.error(t('bots.batchFailed')),
      },
    )

  /** 分组操作区（卡片与行共用）：在控制台打开（仅实例维度）+ 单组批量菜单。 */
  const renderGroupActions = (group: BotSummaryGroup) => (
    <BotGroupActions
      groupBy={groupBy}
      onOpenInConsole={() => navigate(`/instances/${group.key}`)}
      pending={batch.isPending}
      onRunBatch={(action, behavior) => runGroupBatch(group, action, behavior)}
    />
  )

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
            actions={renderGroupActions(group)}
          >
            <BotGroupPeekContainer params={groupFilter(groupBy, group, filter)} onOpenBot={setDetailBotId} />
          </BotWorktableCard>
        )}
        renderRow={({ group, checked, onCheck, expanded, onToggleExpand }) => (
          <BotGroupRow
            group={group}
            checked={checked}
            onCheck={onCheck}
            expanded={expanded}
            onToggleExpand={onToggleExpand}
            actions={renderGroupActions(group)}
            // 窥视经函数传入：折叠行不建窥视元素，保持原页「仅展开才取数」的行为。
            renderPeek={() => (
              <BotGroupPeekContainer params={groupFilter(groupBy, group, filter)} onOpenBot={setDetailBotId} />
            )}
          />
        )}
      />

      <CreateBotDialogContainer open={showCreate} onOpenChange={setShowCreate} />
      <BotStressSessionDialog
        open={showStress}
        onOpenChange={setShowStress}
        onCreate={async (body) => {
          await createStressSession.mutateAsync(body)
        }}
        submitting={createStressSession.isPending}
        renderInstancePicker={(args) => <InstancePicker {...args} />}
      />
      <BotDetailDialog botId={detailBotId} onOpenChange={(open) => { if (!open) setDetailBotId(null) }} />
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
