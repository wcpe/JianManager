import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { PanelLeftClose, RotateCw, Search } from 'lucide-react'

import { useAuthStore } from '@/stores/auth'
import { useConsoleStore } from '@/stores/console'
import { useInstanceAggregate, useSearchInstances } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useInstanceMetrics, useMetricOverview } from '@/api/metrics'
import { useTasks } from '@/api/tasks'
import { useNotificationFeed, useFeedUnreadCount } from '@/api/notification-feed'
import { cn } from '@jianmanager/ui'
import { TopNav } from '@jianmanager/ui/components/shell'
import { TasksMenu } from '@jianmanager/ui/components/views/console/TasksMenu'
import { NotificationBell } from '@jianmanager/ui/components/views/console/NotificationBell'
import { ClusterBadges, STAT_POPOVER_MAX_ROWS, type ClusterSlot } from '@jianmanager/ui/components/views/console/ClusterBadges'
import { AccountMenu } from '@jianmanager/ui/components/views/console/AccountMenu'
import { logoToggleLabelKey } from './sidebar-logo'
import { searchBoxClass } from './header-layout'
import { useWorkspaceNavigation } from './use-workspace-navigation'

/**
 * 行级在线人数取数：把实例 metrics 查询适配成 `ClusterBadges` 需要的形状。
 * 模块级定义（不是每次渲染新建的内联函数），作为 hook 引用注入给包内视图按行顶层调用。
 */
function useInstancePlayers(instanceId: number, enabled: boolean) {
  const { data } = useInstanceMetrics(instanceId, enabled)
  return data ? { available: data.playersAvailable, online: data.onlinePlayers } : undefined
}


/**
 * 全局顶栏（FR-496 阶段 6 补丁：**全局唯一一条**；承接 ADR-071 / FR-162 / FR-179、通知合并 FR-216）。
 *
 * 【为什么删掉面包屑】原型《资源优先工作区》第 1 节：「全局顶栏只做两件事」——切四个工作区、
 * 承载全局查找/任务/通知/账户，并明确「这里没有页面大标题」。此前顶栏左端还挂着
 * `PageBreadcrumb`（FR-134），于是同一页的页名在顶栏（面包屑末级）与内容页（`PageHeader` 大标题）
 * 各出现一次，顶栏实际只做了一件事（导航）却占了两行。补丁把面包屑整段移除：
 * **页名只由内容页承担**（`packages/ui` 的 `PageHeader`，阶段 3 起的标准做法），
 * 顶栏回归「工作区切换 + 全局工具」两件事。
 * 对象详情页要的「紧凑面包屑 → 对象名」是对象头（object head）内部的事，属阶段 7，
 * 因此 `PageBreadcrumb` 组件与 `lib/breadcrumb` 保留在仓库里不删，等对象头接住它。
 *
 * 【为什么只剩一行】阶段 6 把工作区切换接成了独立成行的 `ConsoleWorkspaceBar`，是为了
 * 「不动 ConsoleHeader」的渐进替换约定；代价是顶部出现两条横栏（48px 顶栏 + 53px 工作区行），
 * 与原型「一条 53px 通栏顶栏」不符。本补丁既然已经要动顶栏，就顺手把工作区切换并回顶栏
 * （原型 `.topbar` 内即含 `.space-nav`），顶栏高度对齐原型实测 53px，其下才是
 * 「侧栏 + 内容」的 `app-body`。工作区按钮复用 ui 包 `TopNav`（`.space-nav` 下划线强调样式
 * 的唯一真源），本文件不复制它的 class。
 *
 * 槽位顺序：品牌区 → 工作区切换 → 靠右操作区（搜索 / 集群概览 / 任务 / 统一通知铃铛 FR-216 /
 * 账户菜单）。原节点作用域下拉（FR-268）已下线（ADR-071）：其作用域仅少数页面消费、
 * 全部服务器页自带节点筛选。槽位顺序 / 响应式可见性逻辑仍下沉纯函数 `header-layout.ts`。
 */
export default function ConsoleHeader() {
  const { t } = useTranslation()
  const { workspaces, activeKey, goToWorkspace } = useWorkspaceNavigation()

  return (
    <TopNav
      // `data-slot` 覆盖 ui 包 TopNav 自己的 `top-nav`：外壳的 CSS 与测试都按 `console-header` 取它。
      data-slot="console-header"
      // `flex-none` 覆盖 TopNav 的 `flex-1`（tailwind-merge 后者胜）：外壳是纵向 flex，
      // 顶栏按内容高度（53px）定高，不能与下方 `console-body` 争抢剩余高度。
      // 去掉 `backdrop-blur-xl`：顶栏是 `relative`（并非 sticky/fixed），下方没有任何内容
      // 会滚到它底下，模糊永远作用在同一块静止背景上——纯粹的合成层开销，零视觉收益。
      className="jm-console-header relative z-30 h-[53px] flex-none text-[13px]"
      brand={<BrandSegment />}
      workspaces={workspaces.map((workspace) => ({
        key: workspace.key,
        label: t(workspace.labelKey),
        active: workspace.key === activeKey,
      }))}
      onWorkspaceChange={goToWorkspace}
      actions={<HeaderTools />}
    />
  )
}

/**
 * 顶栏靠右操作区（原型 `.global-tools`）：搜索入口 + 刷新 + 集群徽标 + 任务 + 通知 + 账户。
 * 容器（右对齐 / 14px 内边距 / 5px 间距）由 ui 包 `TopNav` 的 actions 槽位提供，本组件只排内容。
 */
function HeaderTools() {
  const navigate = useNavigate()
  // 常驻弹层的数据在这里取（弹层无 enabled 开关，与改前挂载即取一致）；视图侧只渲染与上报意图。
  const { data: taskPage } = useTasks()
  const { data: unread = 0 } = useFeedUnreadCount()
  const { data: feed } = useNotificationFeed({ pageSize: 8 })

  // 集群三个浮窗：视图内各自持有 open 态并上报，这里只记「当前开着哪个」，
  // 把对应查询的 enabled 绑到它——数据仍只在浮窗打开时拉取（FR-294 原语义）。
  const [clusterSlot, setClusterSlot] = useState<ClusterSlot | null>(null)
  const { data: nodes } = useNodes()
  const { data: runningAgg } = useInstanceAggregate({ status: 'RUNNING' }, clusterSlot === 'nodes')
  const { data: runningPage } = useSearchInstances(
    { status: 'RUNNING', pageSize: STAT_POPOVER_MAX_ROWS },
    clusterSlot === 'running',
  )
  const { data: crashedPage } = useSearchInstances(
    { status: 'CRASHED', pageSize: STAT_POPOVER_MAX_ROWS },
    clusterSlot === 'crashed',
  )
  const { data: overview } = useMetricOverview('24h')
  const { data: aggregate } = useInstanceAggregate()

  const runningByNode = new Map((runningAgg?.byNode ?? []).map((n) => [n.nodeId, n.count]))
  const nodeNames = new Map((nodes ?? []).map((n) => [n.id, n.name]))
  const runningItems = runningPage?.items ?? []
  const crashedItems = crashedPage?.items ?? []
  const username = useAuthStore((s) => s.username)
  const role = useAuthStore((s) => s.role)
  const logout = useAuthStore((s) => s.logout)

  return (
    <>
      <SearchBox />
      <div className="flex items-center gap-0.5 sm:gap-1">
        <RefreshButton />
        <ClusterBadges
          online={overview?.totals.onlineNodeCount ?? 0}
          running={overview?.totals.runningInstances ?? 0}
          crashed={aggregate?.byStatus.CRASHED ?? 0}
          nodeRows={(nodes ?? []).map((n) => ({
            id: n.id,
            name: n.name,
            status: n.status,
            runningCount: runningByNode.get(n.id) ?? 0,
          }))}
          runningRows={runningItems.map((i) => ({ id: i.id, name: i.name, nodeId: i.nodeId, nodeName: nodeNames.get(i.nodeId) }))}
          crashedRows={crashedItems.map((i) => ({
            id: i.id,
            name: i.name,
            nodeId: i.nodeId,
            nodeName: nodeNames.get(i.nodeId),
            statusReason: i.statusReason,
          }))}
          remaining={{
            nodes: (nodes?.length ?? 0) - STAT_POPOVER_MAX_ROWS,
            running: Math.max(0, (runningPage?.total ?? 0) - runningItems.length),
            crashed: Math.max(0, (crashedPage?.total ?? 0) - crashedItems.length),
          }}
          useInstancePlayers={useInstancePlayers}
          onSlotToggle={(slot, open) => setClusterSlot(open ? slot : null)}
          onOpenNode={(nodeId) => navigate(`/nodes?node=${nodeId}`)}
          onOpenInstance={(instanceId) => navigate(`/instances/${instanceId}`)}
          onViewAll={(slot) => {
            if (slot === 'nodes') navigate('/nodes')
            else if (slot === 'running') navigate('/instances?status=RUNNING')
            else navigate('/instances?status=CRASHED')
          }}
        />
        <TasksMenu tasks={taskPage?.items} onOpenTask={(taskId) => navigate(taskId ? `/tasks?task=${taskId}` : '/tasks')} />
        <NotificationBell
          unread={unread}
          items={feed?.items}
          onOpenItem={(item) => {
            // 快捷跳转（FR-226）：任务类站内信→任务中心定位该任务；告警→告警页；其余→通知中心。
            if (item.source === 'message' && item.taskId) navigate(`/tasks?task=${item.taskId}`)
            else if (item.source === 'alert') navigate('/alerts')
            else navigate('/notifications')
          }}
          onViewAll={() => navigate('/notifications')}
        />
        <AccountMenu username={username} role={role} onLogout={logout} />
      </div>
    </>
  )
}

/**
 * 顶栏品牌区（方案 C，见 ADR-071）：Logo + 折叠开关，整体作为折叠触发器复用 `toggleSidebar`
 * （展开态点击=收起、折叠态=展开，接管原侧栏 logo 的 FR-181 行为）。
 *
 * 本组件只排两个按钮，**列宽/内边距/右缘描边由 ui 包 `TopNav` 的品牌槽位与外壳 CSS 提供**：
 * 宽度经 CSS 绑定 `--sidebar-expanded-width`/`--sidebar-collapsed-width` 随侧栏同步收放
 * （ADR-071 的「左列一条连续竖线」），因此这里不能再自己写宽度类，否则会与同步规则打架。
 * 窄屏（<sm）侧栏隐藏，品牌列随之隐藏，顶栏回落为「工作区切换 + 操作区」满宽。
 */
function BrandSegment() {
  const { t } = useTranslation()
  const collapsed = useConsoleStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useConsoleStore((s) => s.toggleSidebar)

  return (
    <>
      <button
        type="button"
        onClick={toggleSidebar}
        aria-label={t(logoToggleLabelKey(collapsed))}
        title={t(logoToggleLabelKey(collapsed))}
        className={cn(
          // `py-1` 必须属于**两种状态共有**：先前它只写在展开分支里，折叠时随 `flex-1 gap-2` 一起消失，
          // 按钮高度随即从 36px 瞬跳到 28px（`transition-colors` 只过渡颜色、不过渡尺寸），
          // 于是点 logo 时图标周边空间会「抽」一下——这正是被反复报的「logo 点击缩放」。
          // 提到公共段后高度恒为 36px（28px 图标 + 上下各 4px），图标在两种状态下位置不动。
          'flex min-w-0 items-center rounded-md py-1 transition-colors hover:bg-accent/60',
          collapsed ? 'justify-center' : 'flex-1 gap-2',
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
          // `ml-auto` 照原型 `.brand .iconbtn{margin-left:auto}`：收起按钮贴品牌列右缘。
          className="ml-auto grid size-7 shrink-0 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
        >
          <PanelLeftClose className="size-4" />
        </button>
      )}
    </>
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
    // `mr-[6px]` 照原型 `.global-search{margin-right:6px}`：操作区槽位间距是 5px，
    // 搜索入口与图标组之间要更松一档，否则两者会贴成一个整块。
    // `data-slot` 供外壳 CSS 在窄视口压缩本槽位（顶栏挤不下时先牺牲它，见 index.css）。
    <div data-slot="console-header-search" className={cn(searchBoxClass(), 'mr-[6px]')}>
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

