import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'

import { useAuthStore } from '@/stores/auth'
import { useConsoleStore } from '@/stores/console'
import { useInstanceAggregate, useSearchInstances } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useInstanceMetrics, useMetricOverview } from '@/api/metrics'
import { useTasks } from '@/api/tasks'
import { useNotificationFeed, useFeedUnreadCount } from '@/api/notification-feed'
import { TopNav } from '@jianmanager/ui/components/shell'
import { TasksMenu } from '@jianmanager/ui/components/views/console/TasksMenu'
import { NotificationBell } from '@jianmanager/ui/components/views/console/NotificationBell'
import { ClusterBadges, STAT_POPOVER_MAX_ROWS, type ClusterSlot } from '@jianmanager/ui/components/views/console/ClusterBadges'
import { AccountMenu } from '@jianmanager/ui/components/views/console/AccountMenu'
import {
  ConsoleBrandSegment,
  ConsoleRefreshButton,
  ConsoleSearchBox,
} from '@jianmanager/ui/components/views/console/console-header-parts'
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

/** 顶栏品牌区（方案 C，ADR-071）：折叠状态与切换动作注入包内展示件，此处只读 console store。 */
function BrandSegment() {
  const collapsed = useConsoleStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useConsoleStore((s) => s.toggleSidebar)
  return <ConsoleBrandSegment collapsed={collapsed} onToggleSidebar={toggleSidebar} />
}

/** 靠右常驻搜索入口（FR-179 / FR-241）：开面板动作注入包内展示件，此处只读 console store。 */
function SearchBox() {
  const openPalette = useConsoleStore((s) => s.setCommandPaletteOpen)
  return <ConsoleSearchBox onOpenPalette={() => openPalette(true)} />
}

/** 全局刷新（FR-232）：重取动作注入包内展示件，此处只提供 queryClient。 */
function RefreshButton() {
  const queryClient = useQueryClient()
  return <ConsoleRefreshButton onRefresh={() => queryClient.invalidateQueries()} />
}

