import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { AlertTriangle, ArrowRight, ClipboardList, Sparkles } from 'lucide-react'
import { Badge, Button, Panel } from '@jianmanager/ui'
import { EmptyState } from '@jianmanager/ui/components/empty-state'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { useAlertEvents } from '@/api/alerts'
import { useTasks } from '@/api/tasks'
import { AssistantChatPanel } from '@/components/views/assistant/AssistantChatPanel'
import { useAssistantChat } from '@/components/views/assistant/useAssistantChat'

/**
 * AI 首页（FR-520）。
 *
 * 与经典首页（`OverviewPage`）是**两棵互不嵌套的树**，由路由层按 `?home=classic` 二选一
 * （spec §9.1：页内条件渲染会让两套查询同时存活，违背「只挂载当前模式的数据查询」）。
 *
 * 数据取用刻意有界（spec §9.1）：只取「未解决告警」与「最近任务」这两类摘要，
 * **不**挂完整实例表、资源趋势与健康墙——首页是入口，不是监控大盘。
 */
export default function AssistantHomePage() {
  const { t } = useTranslation()
  const chat = useAssistantChat(true)

  // 有界摘要：未解决告警取前 5 条；任务取前 5 条。
  // 两者都是既有 hook，不新写取数逻辑（避免与告警页/任务页口径分叉）。
  const alertsQuery = useAlertEvents({ resolved: false, page: 1, pageSize: 5 })
  const tasksQuery = useTasks({ limit: 5 })

  const alerts = alertsQuery.data?.items ?? []
  const tasks = tasksQuery.data?.items ?? []

  return (
    <PageShell data-page="assistant-home">
      <PageHeader
        title={t('assistant.homeTitle', 'AI 助手')}
        description={t('assistant.homeSubtitle', '用自然语言查运维状态、排查问题。')}
        actions={
          // 显式切到经典首页（spec §9：双向可切换，且切换保留无关合法查询）。
          <Button variant="outline" size="sm" asChild>
            <Link to="/?home=classic">
              {t('assistant.homeSwitchClassic', '经典首页')}
              <ArrowRight className="ml-1 size-3.5" />
            </Link>
          </Button>
        }
      />

      <div className="grid min-h-0 flex-1 grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_20rem]">
        {/* 主区：对话。高度由外壳给（内部自行滚动）。 */}
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
                ? t('assistant.chatNoModelHint', '尚未配置可用模型。请让平台管理员在「AI 助手」设置中添加供应商与模型。')
                : null
            }
            onSend={chat.send}
            onStop={chat.stop}
          />
        </Panel>

        {/* 右栏：需要关注 + 近期任务。窄屏降为单列（grid-cols-1）。 */}
        <div className="flex min-h-0 flex-col gap-4 overflow-y-auto">
          <Panel
            title={
              <span className="flex items-center gap-1.5">
                <AlertTriangle className="size-4 text-status-warning" />
                {t('assistant.homeAlerts', '需要关注')}
              </span>
            }
            actions={
              <Button variant="ghost" size="sm" asChild>
                <Link to="/alerts">{t('assistant.homeViewAll', '全部')}</Link>
              </Button>
            }
          >
            {alertsQuery.isLoading ? (
              <p className="text-xs text-muted-foreground">{t('common.loading')}</p>
            ) : alerts.length === 0 ? (
              <p className="text-xs text-muted-foreground">
                {t('assistant.homeAlertsEmpty', '当前没有未解决的告警。')}
              </p>
            ) : (
              <ul className="space-y-1.5">
                {alerts.map((a) => (
                  <li key={a.id} className="flex items-start gap-1.5 text-xs">
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
            {tasksQuery.isLoading ? (
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

          {/* 建议问题：点了直接发送，降低「不知道能问什么」的门槛。 */}
          <Panel title={t('assistant.homeSuggestions', '可以这样问')}>
            <div className="flex flex-col gap-1.5">
              {[
                t('assistant.suggest1', '哪些实例最近崩溃过？'),
                t('assistant.suggest2', '帮我看看有没有未解决的告警'),
                t('assistant.suggest3', '最近有哪些任务失败了？'),
              ].map((q) => (
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
      </div>
    </PageShell>
  )
}
