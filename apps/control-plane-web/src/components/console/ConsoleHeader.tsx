import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useLocation, useNavigate } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Bell, Boxes, ListChecks, Loader2, LogOut, PanelLeftClose, RotateCw, Search, Server, UserRound, Users } from 'lucide-react'

import { useAuthStore } from '@/stores/auth'
import { useConsoleStore } from '@/stores/console'
import { useInstance, useInstanceAggregate, useSearchInstances, type InstanceInfo } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useInstanceMetrics, useMetricOverview } from '@/api/metrics'
import { useTasks, isTerminalTask, TASK_KIND_LABEL_KEYS, type Task } from '@/api/tasks'
import { useNotificationFeed, useFeedUnreadCount, type FeedItem } from '@/api/notification-feed'
import { cn } from '@jianmanager/ui'
import { Badge } from '@jianmanager/ui/components/badge'
import PageBreadcrumb from './PageBreadcrumb'
import { logoToggleLabelKey } from './sidebar-logo'
import { searchBoxClass, slotVisibility, visibilityClass } from './header-layout'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'

/** 角色值 → i18n key（复用 users.* 角色文案，避免重复维护）。 */
const ROLE_LABEL_KEY: Record<number, string> = {
  0: 'users.member',
  1: 'users.groupAdmin',
  10: 'users.platformAdmin',
}

/**
 * 全局顶栏（方案 C 品牌顶栏贯通，见 ADR-071；承接 FR-162/FR-179、通知合并 FR-216）：
 * 横跨整宽的常驻顶栏——左端「品牌区」宽度随其下侧栏同步收放，右缘与侧栏右缘连成一条竖线，
 * 使左列「品牌 + 侧栏」合为一体，消除原「侧栏 logo 区」与「内容页眉」两条错位分割线的交界台阶。
 * 品牌区之右依次为当前页面包屑（占据剩余宽度）+ **靠右对齐操作区**（搜索 / 集群概览 / 任务 /
 * 统一通知铃铛 FR-216 / 账户菜单）。原节点作用域下拉（FR-268）已下线：其作用域仅少数页面消费、
 * 全部服务器页自带节点筛选，页眉入口去重。槽位顺序 / 响应式可见性逻辑仍下沉纯函数 `header-layout.ts`。
 */
export default function ConsoleHeader() {
  return (
    <header
      data-slot="console-header"
      className="jm-console-header relative z-30 flex h-12 shrink-0 items-center border-b text-[13px] backdrop-blur-xl"
    >
      <BrandSegment />
      <div className="flex min-w-0 flex-1 items-center gap-2 px-3 sm:gap-3 sm:px-4">
        <TitleArea />
        {/* 右侧操作区：搜索、集群状态、任务、通知与账户。 */}
        <div className="ml-auto flex items-center gap-2 sm:gap-3">
          <SearchBox />
          <div className="flex items-center gap-0.5 sm:gap-1">
            <RefreshButton />
            <ClusterBadges />
            <TasksMenu />
            <NotificationBell />
            <AccountMenu />
          </div>
        </div>
      </div>
    </header>
  )
}

/**
 * 顶栏品牌区（方案 C，见 ADR-071）：Logo + 折叠开关，整体作为折叠触发器复用 `toggleSidebar`
 * （展开态点击=收起、折叠态=展开，接管原侧栏 logo 的 FR-181 行为）。宽度经 CSS 与侧栏同步收放，
 * 右缘描边与侧栏右缘对齐连线。窄屏（<sm）侧栏隐藏，品牌区随之隐藏，顶栏回落为「面包屑 + 操作区」满宽。
 */
function BrandSegment() {
  const { t } = useTranslation()
  const collapsed = useConsoleStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useConsoleStore((s) => s.toggleSidebar)

  return (
    <div className={cn('jm-brand-segment hidden h-full shrink-0 items-center sm:flex', collapsed ? 'justify-center px-2' : 'gap-2 px-3.5')}>
      <button
        type="button"
        onClick={toggleSidebar}
        aria-label={t(logoToggleLabelKey(collapsed))}
        title={t(logoToggleLabelKey(collapsed))}
        className={cn(
          'flex min-w-0 items-center rounded-md transition-colors hover:bg-accent/60',
          collapsed ? 'justify-center' : '-mx-1.5 flex-1 gap-2 px-1.5 py-1',
        )}
      >
        <span className="grid size-7 shrink-0 place-items-center rounded-md border border-primary/15 bg-card shadow-soft">
          <img src="/brand/jianmanager-mark.svg" alt="" aria-hidden="true" className="size-6" />
        </span>
        {!collapsed && (
          <h2 className="min-w-0 flex-1 truncate text-left text-sm font-bold tracking-tight text-foreground">JianManager</h2>
        )}
      </button>
      {!collapsed && (
        <button
          type="button"
          onClick={toggleSidebar}
          aria-label={t('nav.collapseSidebar')}
          title={t('nav.collapseSidebar')}
          className="grid size-7 shrink-0 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
        >
          <PanelLeftClose className="size-4" />
        </button>
      )}
    </div>
  )
}

/**
 * 左侧面包屑（FR-134 + FR-162）：打开实例工作区时末级补实例名（域›实例›名称），
 * 否则按路由渲染「域 › 页面」轨迹。统一页头组件 `PageBreadcrumb` 承载。
 */
function TitleArea() {
  const location = useLocation()
  const routeInstanceId = Number(location.pathname.match(/^\/instances\/(\d+)/)?.[1] ?? 0)
  const activeInstanceId = routeInstanceId > 0 ? routeInstanceId : null
  const { data: openInst } = useInstance(activeInstanceId ?? 0)
  // min-w-0 让面包屑可截断，避免长轨迹把右侧操作区挤出页眉（窄屏防翻屏）。
  return (
    <div className="min-w-0 flex-1">
      <PageBreadcrumb leaf={openInst?.name} />
    </div>
  )
}

/**
 * 靠右常驻搜索入口（FR-179 重排 + FR-241）：点击或 Ctrl/⌘+K 打开全局命令面板（`CommandPalette`），
 * 检索实例/节点/页面/操作并跳转。本身不再是输入框，仅作开面板的按钮（Ctrl+K 由面板全局监听）。
 * 由 FR-162 的居中铺满改为靠右固定上限宽度（`header-layout.searchBoxClass`），紧贴右侧操作图标；
 * 窄屏（<md）隐藏不挤垮工作区。
 */
function SearchBox() {
  const { t } = useTranslation()
  const openPalette = useConsoleStore((s) => s.setCommandPaletteOpen)

  return (
    <div className={searchBoxClass()}>
      <button
        type="button"
        onClick={() => openPalette(true)}
        aria-label={t('header.searchPlaceholder')}
        className="flex h-8 w-full items-center gap-2 rounded-md border bg-card/90 pl-2.5 pr-2 text-xs text-muted-foreground shadow-soft transition-colors hover:bg-muted/55"
      >
        <Search className="size-4 shrink-0" />
        <span className="min-w-0 flex-1 truncate text-left">{t('header.searchPlaceholder')}</span>
        <kbd className="hidden shrink-0 rounded border bg-background px-1.5 py-0.5 text-[10px] font-medium xl:inline-block">
          Ctrl K
        </kbd>
      </button>
    </div>
  )
}

/**
 * 全局刷新（FR-232）：重拉当前页所有活跃查询（invalidateQueries），转动图标给反馈。
 * 解决「页面无刷新入口」——不整页 reload，仅失效并重取数据。
 */
function RefreshButton() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [spinning, setSpinning] = useState(false)
  const refresh = () => {
    setSpinning(true)
    void queryClient.invalidateQueries().finally(() => {
      setTimeout(() => setSpinning(false), 500)
    })
  }
  return (
    <button
      type="button"
      onClick={refresh}
      disabled={spinning}
      aria-label={t('header.refresh')}
      title={t('header.refresh')}
      className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground disabled:pointer-events-none disabled:opacity-60"
    >
      <RotateCw className={cn('size-4', spinning && 'animate-spin')} />
    </button>
  )
}

/** 集群统计浮窗行数上限（FR-294）：超出在底部提示「还有 N 个，查看全部」。 */
const STAT_POPOVER_MAX_ROWS = 8

/**
 * 集群概览徽标 + 缩略浮窗外壳（FR-294，复用 FR-216 铃铛的 DropdownMenu 范式、同款视觉）：
 * 徽标点击由「直接跳筛选页」改为弹缩略浮窗；受控 open 态经 render prop 传给内容，
 * 让浮窗数据查询 enabled 绑定 open——数据仅浮窗打开时拉取。danger 时计数着红。
 */
function ClusterStatPopover({
  icon: Icon,
  value,
  label,
  danger,
  children,
}: {
  icon: typeof Server
  value: number
  label: string
  danger?: boolean
  /** 浮窗内容，接收当前 open 态用于绑定查询 enabled。 */
  children: (open: boolean) => ReactNode
}) {
  const [open, setOpen] = useState(false)
  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          title={`${label}: ${value}`}
          aria-label={`${label}: ${value}`}
          className="flex items-center gap-1 rounded-md px-1.5 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
        >
          <Icon className={cn('size-3.5', danger && 'text-status-danger')} />
          <span className={cn('tabular-nums', danger && 'font-medium text-status-danger')}>{value}</span>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-72">
        <div className="px-2 py-1.5 text-xs font-medium">{label}</div>
        <DropdownMenuSeparator />
        {children(open)}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** 浮窗底部「查看全部」项（FR-294）：行数超上限时改为提示剩余数。 */
function StatPopoverFooter({ remaining, label, onClick }: { remaining: number; label: string; onClick: () => void }) {
  const { t } = useTranslation()
  return (
    <>
      <DropdownMenuSeparator />
      <DropdownMenuItem onClick={onClick} className="justify-center text-xs text-muted-foreground">
        {remaining > 0 ? t('header.moreCount', { count: remaining }) : label}
      </DropdownMenuItem>
    </>
  )
}

/**
 * 在线节点浮窗内容（FR-294）：行 = 节点名 + 在线状态点 + 该节点运行实例数。
 * 节点列表复用页眉 NodeScopeSelector 已缓存的 ['nodes'] 查询（不额外发请求）；
 * 每节点运行数复用 FR-247 聚合（status=RUNNING 的 byNode），enabled 绑定浮窗 open 态。
 * 行点击 → /nodes?node=<id> 定位该节点（FR-128 深链）。
 */
function NodeStatRows({ open }: { open: boolean }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data: nodes } = useNodes()
  const { data: runningAgg } = useInstanceAggregate({ status: 'RUNNING' }, open)
  const runningByNode = new Map((runningAgg?.byNode ?? []).map((n) => [n.nodeId, n.count]))
  const visible = (nodes ?? []).slice(0, STAT_POPOVER_MAX_ROWS)

  return (
    <>
      {nodes && visible.length === 0 ? (
        <div className="px-2 py-6 text-center text-xs text-muted-foreground">{t('header.noNodes')}</div>
      ) : (
        visible.map((node) => (
          <DropdownMenuItem key={node.id} onClick={() => navigate(`/nodes?node=${node.id}`)} className="text-xs">
            <span
              className={cn('size-1.5 shrink-0 rounded-full', node.status === 1 ? 'bg-status-success' : 'bg-muted-foreground/50')}
            />
            <span className="min-w-0 flex-1 truncate">{node.name}</span>
            <span className="shrink-0 tabular-nums text-muted-foreground">
              {t('header.runningOnNode', { count: runningByNode.get(node.id) ?? 0 })}
            </span>
          </DropdownMenuItem>
        ))
      )}
      <StatPopoverFooter
        remaining={(nodes?.length ?? 0) - visible.length}
        label={t('header.viewAllNodes')}
        onClick={() => navigate('/nodes')}
      />
    </>
  )
}

/**
 * 运行中服务器浮窗内容（FR-294）：行 = 实例名 + 节点名 + 在线人数（有数据时）。
 * 复用 FR-247 分页搜索（status=RUNNING，pageSize=行数上限），enabled 绑定浮窗 open 态；
 * 行点击 → /instances/:id 该服控制台，底部「查看全部」→ /instances?status=RUNNING。
 */
function RunningInstanceRows({ open }: { open: boolean }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data: nodes } = useNodes()
  const { data } = useSearchInstances({ status: 'RUNNING', pageSize: STAT_POPOVER_MAX_ROWS }, open)
  const items = data?.items ?? []
  const nodeNames = new Map((nodes ?? []).map((n) => [n.id, n.name]))

  return (
    <>
      {data && items.length === 0 ? (
        <div className="px-2 py-6 text-center text-xs text-muted-foreground">{t('header.noRunningInstances')}</div>
      ) : (
        items.map((inst) => (
          <RunningInstanceRow key={inst.id} instance={inst} nodeName={nodeNames.get(inst.nodeId)} open={open} />
        ))
      )}
      <StatPopoverFooter
        remaining={Math.max(0, (data?.total ?? 0) - items.length)}
        label={t('header.viewAll')}
        onClick={() => navigate('/instances?status=RUNNING')}
      />
    </>
  )
}

/**
 * 运行中服务器浮窗单行（FR-294）：在线人数复用既有实例 metrics 查询（FR-060），
 * enabled 绑定浮窗 open 态（行仅在浮窗打开时挂载），无数据时不显示人数。
 */
function RunningInstanceRow({ instance, nodeName, open }: { instance: InstanceInfo; nodeName?: string; open: boolean }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data: metrics } = useInstanceMetrics(instance.id, open)
  return (
    <DropdownMenuItem onClick={() => navigate(`/instances/${instance.id}`)} className="text-xs">
      <span className="size-1.5 shrink-0 rounded-full bg-status-success" />
      <span className="min-w-0 flex-1 truncate">{instance.name}</span>
      {nodeName && <span className="shrink-0 text-muted-foreground">{nodeName}</span>}
      {metrics && (
        <span className="flex shrink-0 items-center gap-1 tabular-nums text-muted-foreground">
          <Users className="size-3" />
          {t('header.onlinePlayersCount', { count: metrics.onlinePlayers })}
        </span>
      )}
    </DropdownMenuItem>
  )
}

/**
 * 崩溃服务器浮窗内容（FR-294）：行 = 实例名 + 节点名 + 崩溃原因（statusReason，有则显）；
 * 空态显示友好文案。复用 FR-247 分页搜索（status=CRASHED），enabled 绑定浮窗 open 态；
 * 行点击 → /instances/:id，底部「查看全部」→ /instances?status=CRASHED。
 */
function CrashedInstanceRows({ open }: { open: boolean }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data: nodes } = useNodes()
  const { data } = useSearchInstances({ status: 'CRASHED', pageSize: STAT_POPOVER_MAX_ROWS }, open)
  const items = data?.items ?? []
  const nodeNames = new Map((nodes ?? []).map((n) => [n.id, n.name]))

  return (
    <>
      {data && items.length === 0 ? (
        <div className="px-2 py-6 text-center text-xs text-muted-foreground">{t('header.noCrashedInstances')}</div>
      ) : (
        items.map((inst) => (
          <DropdownMenuItem key={inst.id} onClick={() => navigate(`/instances/${inst.id}`)} className="items-start text-xs">
            <span className="mt-1 size-1.5 shrink-0 rounded-full bg-status-danger" />
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-1.5">
                <span className="min-w-0 flex-1 truncate">{inst.name}</span>
                {nodeNames.get(inst.nodeId) && (
                  <span className="shrink-0 text-muted-foreground">{nodeNames.get(inst.nodeId)}</span>
                )}
              </div>
              {inst.statusReason && <p className="mt-0.5 truncate text-[11px] text-muted-foreground">{inst.statusReason}</p>}
            </div>
          </DropdownMenuItem>
        ))
      )}
      <StatPopoverFooter
        remaining={Math.max(0, (data?.total ?? 0) - items.length)}
        label={t('header.viewAll')}
        onClick={() => navigate('/instances?status=CRASHED')}
      />
    </>
  )
}

/**
 * 集群概览徽标组（FR-162；FR-294 点击改弹缩略浮窗）：在线节点 / 运行实例 / 崩溃数。
 * 徽标计数数据源不变（总览页同款聚合 + FR-247 聚合，避免页眉为计数拉全量实例）；
 * 原「点击跳筛选页」保留为浮窗底部「查看全部」。窄屏隐藏逻辑（header-layout）不变。
 */
function ClusterBadges() {
  const { t } = useTranslation()
  const { data: overview } = useMetricOverview('24h')
  const { data: aggregate } = useInstanceAggregate()
  const online = overview?.totals.onlineNodeCount ?? 0
  const running = overview?.totals.runningInstances ?? 0
  const crashed = aggregate?.byStatus.CRASHED ?? 0

  return (
    <div className={cn('items-center', visibilityClass(slotVisibility('clusterBadges')))}>
      <ClusterStatPopover icon={Server} value={online} label={t('header.onlineNodes')}>
        {(open) => <NodeStatRows open={open} />}
      </ClusterStatPopover>
      <ClusterStatPopover icon={Boxes} value={running} label={t('header.runningInstances')}>
        {(open) => <RunningInstanceRows open={open} />}
      </ClusterStatPopover>
      <ClusterStatPopover icon={AlertTriangle} value={crashed} label={t('header.crashedInstances')} danger={crashed > 0}>
        {(open) => <CrashedInstanceRows open={open} />}
      </ClusterStatPopover>
    </div>
  )
}

/** 页眉任务下拉的行数上限（FR-327）：最近 N 条，看全量进任务中心页。 */
const TASKS_MENU_MAX_ROWS = 8

/** 终态任务 → 徽章变体与文案键（tasks.state.*，与任务中心页同款语义）。 */
const TASK_BADGE_META: Record<string, { variant: 'secondary' | 'destructive' | 'outline'; key: string }> = {
  succeeded: { variant: 'secondary', key: 'tasks.state.succeeded' },
  failed: { variant: 'destructive', key: 'tasks.state.failed' },
  canceled: { variant: 'outline', key: 'tasks.state.canceled' },
}

/**
 * 页眉任务中心入口 + 下拉面板（FR-327，下拉化 FR-226 的「点击直跳任务中心」）：
 * 入口常驻——有在跑任务（pending/running）时显示数量 + 平均进度（转圈反馈），空闲时静态图标；
 * 点击弹下拉面板列最近 N 条任务（kind 徽标/名称/stage 进度/终态徽章），点条目跳任务中心定位该任务
 * （FR-226 `?task=` 深链），底部「进入任务中心」看全量。面板外点击关闭为 DropdownMenu 缺省行为。
 * 数据/轮询复用 useTasks（FR-329：活跃 2s 短轮询、空闲停），下拉不额外发请求。
 */
function TasksMenu() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  // FR-337 分页信封：消费 items（首窗 ≤100，active 计数/最近 N 条口径与改前一致）。
  const { data } = useTasks()
  const tasks = data?.items ?? []
  const active = tasks.filter((tk) => !isTerminalTask(tk))
  const recent = tasks.slice(0, TASKS_MENU_MAX_ROWS)
  const avg = active.length > 0 ? Math.round(active.reduce((s, tk) => s + tk.progress, 0) / active.length) : 0

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          title={active.length > 0 ? t('header.tasksRunning', { count: active.length, progress: avg }) : t('header.tasks')}
          aria-label={t('header.tasks')}
          className={cn(
            'flex items-center gap-1.5 rounded-md px-1.5 py-1 text-xs transition-colors hover:bg-accent/60',
            active.length > 0 ? 'text-primary' : 'text-muted-foreground hover:text-foreground',
          )}
        >
          {active.length > 0 ? (
            <>
              <Loader2 className="size-3.5 animate-spin" />
              <span className="font-medium tabular-nums">{active.length}</span>
              <span className="tabular-nums text-muted-foreground">{avg}%</span>
            </>
          ) : (
            <ListChecks className="size-4" />
          )}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80">
        <div className="flex items-center justify-between px-2 py-1.5 text-xs font-medium">
          <span>{t('header.tasks')}</span>
          {active.length > 0 && (
            <span className="text-muted-foreground">{t('header.tasksActiveCount', { count: active.length })}</span>
          )}
        </div>
        <DropdownMenuSeparator />
        {recent.length === 0 ? (
          <div className="px-2 py-6 text-center text-xs text-muted-foreground">{t('tasks.empty')}</div>
        ) : (
          // 内容自适应 + 超高内部滚动（ui-modals 纪律：禁固定尺寸溢出）。
          <div className="max-h-72 overflow-y-auto">
            {recent.map((task) => (
              <TaskMenuRow key={task.taskId} task={task} />
            ))}
          </div>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={() => navigate('/tasks')} className="justify-center text-xs text-muted-foreground">
          {t('header.viewAllTasks')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/**
 * 页眉任务下拉单行（FR-327）：kind 徽标 + 标题 + 进行中进度条（% 数值）/终态徽章 + stage 详情。
 * 点击跳任务中心并深链定位该任务（`/tasks?task=<taskId>`，进页自动展开，FR-226）。
 */
function TaskMenuRow({ task }: { task: Task }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const kindKey = TASK_KIND_LABEL_KEYS[task.kind]
  // 取消中（已请求停止但 Worker 未确认）优先于状态徽章，语义与任务中心页一致（FR-227）。
  const canceling = task.state === 'running' && task.cancelRequested
  const badge = TASK_BADGE_META[task.state]
  const pct = Math.max(0, Math.min(100, task.progress))
  return (
    <DropdownMenuItem onClick={() => navigate(`/tasks?task=${task.taskId}`)} className="items-start text-xs">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5">
          <span className="shrink-0 rounded bg-primary/10 px-1 py-px text-[10px] font-medium text-primary">
            {kindKey ? t(kindKey) : task.kind}
          </span>
          <span className="min-w-0 flex-1 truncate text-foreground">{task.title || task.kind}</span>
          {canceling ? (
            <Badge variant="outline">{t('tasks.state.canceling', '取消中')}</Badge>
          ) : (
            badge && <Badge variant={badge.variant}>{t(badge.key)}</Badge>
          )}
        </div>
        {!isTerminalTask(task) && !canceling && (
          <div className="mt-1 flex items-center gap-2">
            <div className="h-1 min-w-0 flex-1 overflow-hidden rounded-full bg-muted">
              <div className="h-full rounded-full bg-primary transition-all" style={{ width: `${pct}%` }} />
            </div>
            <span className="shrink-0 tabular-nums text-[10px] text-muted-foreground">{pct}%</span>
          </div>
        )}
        {task.detail && <p className="mt-0.5 truncate text-[11px] text-muted-foreground">{task.detail}</p>}
      </div>
    </DropdownMenuItem>
  )
}

/** 统一通知级别 → 圆点配色类（站内信四档；告警三档已在后端就近映射到此）。 */
function feedLevelDotClass(level: string): string {
  if (level === 'error') return 'bg-status-danger'
  if (level === 'warning') return 'bg-status-warning'
  if (level === 'success') return 'bg-status-success'
  return 'bg-status-info'
}

/**
 * 统一通知铃铛（FR-216，见 ADR-048）：合并原「站内信收件箱」+「告警铃铛」为单一入口。
 * 未读计数（统一：本人站内信 + 全局告警，30s 轮询）+ 下拉只读最近通知（消息/告警混合，各带来源标识与级别色点）；
 * 点「查看全部」进通知中心页。处置（确认/认领）仍在告警页，本下拉只读预览。
 */
function NotificationBell() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data: unread = 0 } = useFeedUnreadCount()
  const { data: feed } = useNotificationFeed({ pageSize: 8 })
  const recent = feed?.items ?? []

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={t('header.notifications')}
          className="relative rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
        >
          <Bell className="size-4" />
          {unread > 0 && (
            <span className="absolute -right-0.5 -top-0.5 grid min-w-4 place-items-center rounded-full bg-status-danger px-1 text-[10px] font-semibold leading-4 text-white">
              {unread > 99 ? '99+' : unread}
            </span>
          )}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80">
        <div className="flex items-center justify-between px-2 py-1.5 text-xs font-medium">
          <span>{t('header.notifications')}</span>
          {unread > 0 && <span className="text-muted-foreground">{t('header.unreadCount', { count: unread })}</span>}
        </div>
        <DropdownMenuSeparator />
        {recent.length === 0 ? (
          <div className="px-2 py-6 text-center text-xs text-muted-foreground">{t('notificationCenter.empty')}</div>
        ) : (
          <div className="max-h-72 overflow-y-auto">
            {recent.map((it) => (
              <NotificationPreviewRow key={`${it.source}-${it.id}`} item={it} />
            ))}
          </div>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={() => navigate('/notifications')}>
          {t('header.viewAllNotifications')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** 下拉内单条通知预览：级别色点 + 来源徽标 + 标题/正文 + 时间 + 未读点。 */
function NotificationPreviewRow({ item }: { item: FeedItem }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const sourceLabel = item.source === 'alert' ? t('notificationCenter.badgeAlert') : t('notificationCenter.badgeMessage')
  // 快捷跳转（FR-226）：任务类站内信→任务中心定位该任务；告警→告警页；其余→通知中心。
  const jump = () => {
    if (item.source === 'message' && item.taskId) navigate(`/tasks?task=${item.taskId}`)
    else if (item.source === 'alert') navigate('/alerts')
    else navigate('/notifications')
  }
  return (
    <button type="button" onClick={jump} className="flex w-full items-start gap-2 px-2 py-1.5 text-left text-xs transition-colors hover:bg-accent/60">
      <span className={cn('mt-1 size-1.5 shrink-0 rounded-full', feedLevelDotClass(item.level))} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5">
          <span
            className={cn(
              'shrink-0 rounded px-1 py-px text-[10px] font-medium',
              item.source === 'alert'
                ? 'bg-status-warning/15 text-status-warning'
                : 'bg-primary/10 text-primary',
            )}
          >
            {sourceLabel}
          </span>
          <p className="truncate text-foreground">{item.title}</p>
        </div>
        {item.body && <p className="mt-0.5 truncate text-[11px] text-muted-foreground">{item.body}</p>}
        <p className="text-[11px] text-muted-foreground">{new Date(item.createdAt).toLocaleString()}</p>
      </div>
      {!item.read && <span className="mt-1 size-1.5 shrink-0 rounded-full bg-primary" />}
    </button>
  )
}

/** 账户菜单（FR-162）：显示用户名 / 角色 + 退出登录（接管 FR-132 的退出图标化）。 */
function AccountMenu() {
  const { t } = useTranslation()
  const username = useAuthStore((s) => s.username)
  const role = useAuthStore((s) => s.role)
  const logout = useAuthStore((s) => s.logout)
  const roleKey = role != null ? ROLE_LABEL_KEY[role] : undefined

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={t('header.account')}
          className="flex items-center gap-1.5 rounded-md px-1.5 py-1 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
        >
          <span className="grid size-6 shrink-0 place-items-center rounded-full bg-primary/15 text-primary">
            <UserRound className="size-3.5" />
          </span>
          <span className="hidden max-w-32 truncate text-xs font-medium text-foreground sm:block">
            {username ?? t('header.account')}
          </span>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        <div className="px-2 py-1.5">
          <p className="truncate text-sm font-medium">{username ?? '—'}</p>
          {roleKey && <p className="text-xs text-muted-foreground">{t(roleKey)}</p>}
        </div>
        <DropdownMenuSeparator />
        <DropdownMenuItem variant="destructive" onClick={logout}>
          <LogOut className="size-4" />
          {t('common.logout')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
