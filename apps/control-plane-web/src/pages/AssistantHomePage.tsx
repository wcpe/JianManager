import { useCallback, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate } from 'react-router'
import { AlertTriangle, ArrowRight, ClipboardList, MessageSquare, MoreHorizontal, Server, Sparkles } from 'lucide-react'
import { toast } from 'sonner'
import { Badge, Button, Panel } from '@jianmanager/ui'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import { EmptyState } from '@jianmanager/ui/components/empty-state'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { useAlertEvents } from '@/api/alerts'
import type { TaskPage } from '@/lib/tasks/task-status'
import type { InstanceSearchResult } from '@/api/instances'
import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'
import type { MetricOverviewResponse } from '@/api/metrics'
import { useAssistantConversations } from '@/api/assistant-chat'
import { AssistantChatPanel } from '@/components/views/assistant/AssistantChatPanel'
import { useAssistantChatCtx } from '@/components/views/assistant/assistant-chat-context'
import { usePermissionsStore } from '@/stores/permissions'
import { useAuthStore } from '@/stores/auth'
import { saveHomeMode, resetHomeMode, homeModeHref } from '@/lib/shared/home-mode'

/** 
 * AI 首页。
 *
 * 与经典首页（`OverviewPage`）是**两棵互不嵌套的树**，由路由层按 `?home=classic` 二选一
 * （spec §9.1：页内条件渲染会让两套查询同时存活，违背「只挂载当前模式的数据查询」）。
 *
 * 摘要区取数一律**有界且不轮询**（spec §6）：
 *  - 每个区块独立挂载、独立失败——首页核心功能是「能直接提问」，一个摘要 500 不该拖垮输入器；
 *  - 不合并成一个大请求（多权限域混在一个响应里，服务端只能做并集，容易出错）；
 *  - 既有 hook 自带的轮询不能带进来：本页是常开页，10/30 秒一次的全平台查询会持续压 CP。
 *    注：**不能**靠给既有 hook 传 `refetchInterval: false` 来关——顶栏用同一个 `queryKey`
 *    在轮询，React Query 的缓存观察者是 key 级的，同 key 就会被一起刷新（真机实测过）。
 *    故摘要区一律用本页私有的 queryKey 直调端点（spec §6 规则 3 方案 a）。
 *
 * 告警摘要**仅平台管理员挂载**（spec §7）：`/alerts/events` 尚未按调用者可见范围收敛，
 * 普通用户既不该看到区块，也不该发出这个请求——所以用 `enabled` 从根上不发。
 */

/** 问候语时段（本地时区）。 */
type DayPart = 'morning' | 'afternoon' | 'evening'

function dayPartOf(hour: number): DayPart {
  // 边界按直觉取：5–11 早上、12–17 下午、其余算晚上。
  // 不做「凌晨」档：夜里来运维的人也确实该被问「晚上好」，多一档没有信息增益。
  if (hour >= 5 && hour < 12) return 'morning'
  if (hour >= 12 && hour < 18) return 'afternoon'
  return 'evening'
}

export default function AssistantHomePage() {
  const { t } = useTranslation()
  /* 
    与浮窗共用**同一份**会话状态（外壳级单例）。这里若自行调用 useAssistantChat，
    就会退回到「首页一份、浮窗一份」的分裂状态——流式途中最容易露馅。
  */
  const chat = useAssistantChatCtx()
  const navigate = useNavigate()

  /** 
   * 重置首页偏好（§4.4）。
   *
   * 两件事必须一起做，否则会出现「点了重置但页面没变」的假成功：
   * ① 清掉记忆；② 若当前 URL 带 `?home=`，把它去掉——因为显式参数的优先级高于记忆，
   * 只清记忆的话 URL 里的 classic 仍会继续压过默认值。
   */
  const handleResetHomeMode = useCallback(() => {
    resetHomeMode()
    const params = new URLSearchParams(window.location.search)
    if (params.has('home')) {
      params.delete('home')
      const qs = params.toString()
      navigate({ pathname: '/', search: qs ? `?${qs}` : '' }, { replace: true })
    }
    toast.success(t('assistant.homeResetDone', '已重置为默认首页'))
  }, [navigate, t])

  const isPlatformAdmin = usePermissionsStore((s) => s.isPlatformAdmin)
  const username = useAuthStore((s) => s.username)

  // ── 摘要取数（全部关掉轮询；区块内最多 5 条） ──────────────────────────
  // 告警：仅平台管理员。非管理员 `enabled: false` → 连请求都不发（验收要求「Network 里没有」）。
  const alertsQuery = useAlertEvents(
    { resolved: false, page: 1, pageSize: 5 },
    { enabled: isPlatformAdmin },
  )
  // 近期任务：**同样走私有 key 直调**，不复用 `useTasks`。
  //
  // `useTasks` 在存在非终态任务时会以 2 秒间隔重取（的 `tasksRefetchInterval`）。
  // 首页是常开页，只要有部署/备份等长期任务在跑，就会变成每 2 秒一次全平台任务查询——
  // 与本文件另两处摘要（异常实例、集群水位）的取舍理由完全相同，也违背 spec §6「摘要不轮询」。
  // 真机实测（devmock large 规模，30 秒窗口）：复用 hook 时该端点被请求 13～14 次。
  const tasksQuery = useQuery({
    queryKey: ['assistantHome', 'tasks'],
    queryFn: async () => {
      const { data } = await api.get<TaskPage>('/tasks', { params: { limit: 5 } })
      return data
    },
  })
  // 异常实例：复用服务端搜索端点（不拉全量），只取前 5 条。
  // 同样用独立 key：顶栏的 `useSearchInstances` 带 30 秒兜底轮询且 key 与本页相同，
  // 共用观察者会被一起刷新（与集群水位同一原因，见下方注释）。
  const crashedQuery = useQuery({
    queryKey: ['assistantHome', 'crashedInstances'],
    queryFn: async () => {
      const { data } = await api.get<InstanceSearchResult>('/instances/search', {
        params: { status: 'CRASHED', page: 1, pageSize: 5 },
      })
      return data
    },
  })
  // 集群水位：只要 totals 概览，不画趋势图（spec §5.2）；`range` 必填，固定 24h。
  //
  // **不能复用 `useMetricOverview`**：顶栏 `ConsoleHeader` 也在拉同一端点，且用的是同一个
  // `queryKey`（`['metricOverview','24h','auto']`）。React Query 的缓存观察者是 key 级的——
  // 只要顶栏还在 10 秒轮询，首页作为同 key 的观察者就会被一起刷新，把 `refetchInterval`
  // 传 `false` 也拦不住（**真机实测**：静置 36 秒内该端点被请求 4 次）。
  // 故这里按 spec §6 规则 3 方案 (a)：独立 key 直调同一端点，不设任何轮询，进入时取一次。
  const overviewQuery = useQuery({
    queryKey: ['assistantHome', 'metricOverview', '24h'],
    queryFn: async () => {
      const { data } = await api.get<MetricOverviewResponse>('/metrics/overview?range=24h')
      return data
    },
  })
  // 最近会话：会话列表前 5（服务端已按 updatedAt 倒序）。
  const conversationsQuery = useAssistantConversations(true)

  const alerts = isPlatformAdmin ? (alertsQuery.data?.items ?? []) : []
  const tasks = tasksQuery.data?.items ?? []
  const crashed = crashedQuery.data?.items ?? []
  const totals = overviewQuery.data?.totals
  const recentConversations = (conversationsQuery.data ?? []).slice(0, 5)

  const greeting = useMemo(() => {
    const part = dayPartOf(new Date().getHours())
    // 显式三分支而不是拼 key 再查表：拼 key 会丢掉 `t()` 的键类型约束，
    // 少写一个档位时运行时才暴露成裸键名。
    if (part === 'morning') return t('assistant.greet_morning', '早上好')
    if (part === 'afternoon') return t('assistant.greet_afternoon', '下午好')
    return t('assistant.greet_evening', '晚上好')
  }, [t])

  // 建议问题：静态文案 + 传参，不发模型请求（spec §5.1）。
  // 按权限过滤——普通用户不提他看不到的东西（告警）。
  const suggestions = useMemo(() => {
    const crashed = t('assistant.suggest1', '哪些实例最近崩溃过？')
    const failedTasks = t('assistant.suggest3', '最近有哪些任务失败了？')
    const alertSuggestion = t('assistant.suggest2', '帮我看看有没有未解决的告警')
    // 告警建议只给平台管理员：普通用户问出来只会得到「看不到」。
    return isPlatformAdmin ? [crashed, alertSuggestion, failedTasks] : [crashed, failedTasks]
  }, [isPlatformAdmin, t])

  return (
    <PageShell data-page="assistant-home">
      <PageHeader
        title={t('assistant.homeTitle', 'AI 助手')}
        description={t('assistant.homeSubtitle', '用自然语言查运维状态、排查问题。')}
        actions={
          // §4.4：主操作是切到经典首页；「重置首页偏好」作为**次级操作**收在旁边的
          // 小菜单里——它是低频纠错动作，不该与切换按钮同等视觉权重。
          <div className="flex items-center gap-1">
            <Button variant="outline" size="sm" asChild>
              {/* 目标 URL 保留现有查询参数（§8）：切模式不该顺手清掉用户的筛选条件 */}
              <Link to={homeModeHref('classic', window.location.search)} onClick={() => saveHomeMode('classic')}>
                {t('assistant.homeSwitchClassic', '经典首页')}
                <ArrowRight className="ml-1 size-3.5" />
              </Link>
            </Button>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon" aria-label={t('assistant.homePrefsAria', '首页偏好')}>
                  <MoreHorizontal className="size-4" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={handleResetHomeMode}>
                  {t('assistant.homeResetPref', '重置首页偏好')}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        }
      />

      <div className="grid min-h-0 flex-1 grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_20rem]">
        {/* 主区：问候 → 对话 → 建议问题 → 最近会话（窄屏即按此单栏顺序）。 */}
        <div className="flex min-h-0 flex-col gap-4">
          <div>
            <p className="text-sm font-medium">
              {greeting}
              {username ? `，${username}` : ''}
            </p>
            <p className="text-xs text-muted-foreground">
              {t('assistant.homeGreetHint', '想了解什么？直接问，或从下面的问题开始。')}
            </p>
          </div>

          <Panel
            title={t('assistant.homeChatTitle', '对话')}
            bodyClassName="flex min-h-0 flex-1 flex-col p-2"
            className="flex min-h-0 flex-col"
          >
            <AssistantChatPanel
              messages={chat.messages}
              running={chat.running}
              providerName={chat.providerName}
              modelName={chat.modelName}
              hint={
                chat.noModel
                  ? t(
                      'assistant.chatNoModelHint',
                      '尚未配置可用模型。请让平台管理员在「AI 助手」设置中添加供应商与模型。',
                    )
                  : null
              }
              onSend={chat.send}
              onStop={chat.stop}
              draft={chat.draft}
              onDraftChange={chat.setDraft}
              contextChips={chat.contextChips}
              runError={chat.runError}
              contextEntities={chat.contextEntities}
              onAddContextEntity={chat.addContextEntity}
              confirmationRunId={chat.runId}
              toolCalls={chat.toolCalls}
            />
          </Panel>

          {/* 建议问题：点了直接发送，降低「不知道能问什么」的门槛。 */}
          <Panel title={t('assistant.homeSuggestions', '可以这样问')}>
            <div className="flex flex-wrap gap-1.5">
              {suggestions.map((q) => (
                <Button
                  key={q}
                  variant="ghost"
                  size="sm"
                  className="h-auto justify-start whitespace-normal py-1.5 text-left text-xs font-normal"
                  disabled={chat.running}
                  onClick={() => void chat.send(q)}
                >
                  <Sparkles className="mr-1.5 size-3.5 shrink-0 text-muted-foreground" />
                  {q}
                </Button>
              ))}
            </div>
          </Panel>
        </div>

        {/* 右栏：摘要。窄屏降为单列（grid-cols-1）。 */}
        <div className="flex min-h-0 flex-col gap-4 overflow-y-auto">
          {/* 异常实例 + 告警同属「需要关注」。两者都空时显示一句合并空态。 */}
          <Panel
            title={
              <span className="flex items-center gap-1.5">
                <AlertTriangle className="size-4 text-status-warning" />
                {t('assistant.homeAlerts', '需要关注')}
              </span>
            }
            actions={
              isPlatformAdmin ? (
                <Button variant="ghost" size="sm" asChild>
                  <Link to="/alerts">{t('assistant.homeViewAll', '全部')}</Link>
                </Button>
              ) : null
            }
          >
            {crashedQuery.isError || (isPlatformAdmin && alertsQuery.isError) ? (
              <p className="text-xs text-destructive">{t('assistant.homeLoadFailed', '加载失败，稍后可刷新重试')}</p>
            ) : crashedQuery.isLoading || (isPlatformAdmin && alertsQuery.isLoading) ? (
              <p className="text-xs text-muted-foreground">{t('common.loading')}</p>
            ) : crashed.length === 0 && alerts.length === 0 ? (
              <p className="text-xs text-muted-foreground">
                {isPlatformAdmin
                  ? t('assistant.homeAllClear', '当前没有异常实例，也没有未解决的告警。')
                  : t('assistant.homeNoCrashed', '当前没有异常实例。')}
              </p>
            ) : (
              <ul className="space-y-1.5">
                {crashed.map((inst) => (
                  <li key={`i-${inst.id}`} className="flex items-start gap-1.5 text-xs">
                    <Badge variant="destructive" className="mt-0.5 shrink-0">
                      {inst.status}
                    </Badge>
                    <Link to={`/instances/${inst.id}`} className="min-w-0 flex-1 truncate hover:underline">
                      {inst.name}
                    </Link>
                  </li>
                ))}
                {alerts.map((a) => (
                  <li key={`a-${a.id}`} className="flex items-start gap-1.5 text-xs">
                    <Badge
                      variant={a.level === 'critical' || a.level === 'error' ? 'destructive' : 'secondary'}
                      className="mt-0.5 shrink-0"
                    >
                      {a.level}
                    </Badge>
                    <span className="min-w-0 flex-1 truncate" title={a.message}>
                      {a.message}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </Panel>

          <Panel
            title={
              <span className="flex items-center gap-1.5">
                <ClipboardList className="size-4 text-muted-foreground" />
                {t('assistant.homeTasks', '近期任务')}
              </span>
            }
            actions={
              <Button variant="ghost" size="sm" asChild>
                <Link to="/tasks">{t('assistant.homeViewAll', '全部')}</Link>
              </Button>
            }
            >
            {tasksQuery.isError ? (
              <p className="text-xs text-destructive">{t('assistant.homeLoadFailed', '加载失败，稍后可刷新重试')}</p>
            ) : tasksQuery.isLoading ? (
              <p className="text-xs text-muted-foreground">{t('common.loading')}</p>
            ) : tasks.length === 0 ? (
              <EmptyState
                title={t('assistant.homeTasksEmpty', '近期没有任务')}
                description={t('assistant.homeTasksEmptyHint', '部署、备份、批量操作会出现在这里。')}
              />
            ) : (
              <ul className="space-y-1.5">
                {tasks.map((task) => (
                  <li key={task.id} className="flex items-center justify-between gap-2 text-xs">
                    <span className="min-w-0 flex-1 truncate" title={task.title}>
                      {task.title}
                    </span>
                    <Badge variant="outline" className="shrink-0">
                      {task.state}
                    </Badge>
                  </li>
                ))}
              </ul>
            )}
          </Panel>

          {/* 集群水位：只取 totals 概览，不画趋势图（spec §5.2）。无权限/加载中不占位。 */}
          {/* 集群水位：只取 totals 概览，不画趋势图（spec §5.2）。
              失败时保留区块外壳并显示错误，而不是当「无权限」直接不渲染——
              两者语义不同：无权限是「本就不该看」，失败是「本该有但没拿到」。 */}
          {totals || overviewQuery.isError ? (
            <Panel
              title={
                <span className="flex items-center gap-1.5">
                  <Server className="size-4 text-muted-foreground" />
                  {t('assistant.homeCluster', '集群水位')}
                </span>
              }
            >
              {overviewQuery.isError ? (
                <p className="text-xs text-destructive">{t('assistant.homeLoadFailed', '加载失败，稍后可刷新重试')}</p>
              ) : totals ? (
              <dl className="grid grid-cols-2 gap-x-3 gap-y-1.5 text-xs">
                <div className="flex items-center justify-between gap-2">
                  <dt className="text-muted-foreground">{t('assistant.homeNodes', '节点')}</dt>
                  <dd className="tabular-nums">
                    {totals.onlineNodeCount}/{totals.nodeCount}
                  </dd>
                </div>
                <div className="flex items-center justify-between gap-2">
                  <dt className="text-muted-foreground">{t('assistant.homeInstances', '运行实例')}</dt>
                  <dd className="tabular-nums">{totals.runningInstances}</dd>
                </div>
                <div className="flex items-center justify-between gap-2">
                  <dt className="text-muted-foreground">{t('assistant.homePlayers', '在线玩家')}</dt>
                  <dd className="tabular-nums">{totals.onlinePlayers}</dd>
                </div>
                <div className="flex items-center justify-between gap-2">
                  <dt className="text-muted-foreground">{t('assistant.homeCpu', 'CPU')}</dt>
                  <dd className="tabular-nums">{totals.cpuPct.toFixed(1)}%</dd>
                </div>
              </dl>
              ) : null}
            </Panel>
          ) : null}

          {/* 最近会话：无会话时整块不渲染（spec §5.1 明确「不留空壳」）。
              放在右栏末尾而不是宽屏布局图所示的左栏：§8 的硬性验收是「窄屏单栏顺序」，
              而窄屏两栏是**上下堆叠**（左栏整体在前），若把它留在左栏，它会跑到
              「需要关注/近期任务/集群水位」之前，与 spec 要求的末端位置冲突。
              宽屏下它在右栏最下方，与示意图的差异不影响任何可测验收项。 */}
          {recentConversations.length > 0 || conversationsQuery.isError ? (
            <Panel
              title={
                <span className="flex items-center gap-1.5">
                  <MessageSquare className="size-4 text-muted-foreground" />
                  {t('assistant.homeRecent', '最近会话')}
                </span>
              }
            >
              {conversationsQuery.isError ? (
                <p className="text-xs text-destructive">{t('assistant.homeLoadFailed', '加载失败，稍后可刷新重试')}</p>
              ) : (
              <ul className="flex flex-col gap-0.5">
                {recentConversations.map((c) => (
                  <li key={c.id}>
                    <button
                      type="button"
                      onClick={() => chat.selectConversation(c.id)}
                      className={
                        c.id === chat.conversationId
                          ? 'w-full truncate rounded px-2 py-1 text-left text-xs font-medium text-primary hover:bg-accent/60'
                          : 'w-full truncate rounded px-2 py-1 text-left text-xs hover:bg-accent/60'
                      }
                    >
                      {c.title || t('assistant.dockUntitled', '未命名会话')}
                    </button>
                  </li>
                ))}
              </ul>
              )}
            </Panel>
          ) : null}
        </div>
      </div>
    </PageShell>
  )
}
