import { useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Scale } from 'lucide-react'

import type { InstanceInfo } from '@/lib/instances/instance-types'
import type { NodeInfo } from '@/lib/nodes/node-types'
import { cn } from '@jianmanager/ui'
import {
  ResourceTree,
  SideNav,
  SideNavBottom,
  SideNavHead,
  SideNavResource,
  SideNavRow,
  SideNavShortcuts,
  type ResourceNode,
  type ResourceStatus,
} from '@jianmanager/ui/components/shell'
import { ThemeSwitcher } from '@/components/views/console/ThemeSwitcher'
import type { ThemeSwitcherProps } from '@/components/views/console/ThemeSwitcher'
import type { SidebarLinkArgs } from '@/components/views/console/sidebar-link'
import { statusDotKind } from '@/lib/instances/instance-tree'
import type { NavEntry } from '@/lib/shared/nav-config'
import { resolveWorkspacePath } from '@/lib/console/workspace-navigation'
import type { WorkspaceDef } from '@/lib/console/workspace-navigation'
import { isDeepLink, landingPathOf } from '@/lib/hooks/use-workspace-navigation'
import type { MemberStatusCounts } from '@/lib/networks/topology'

/**
 * 工作区外壳侧栏（FR-496 阶段 6）：把「资源优先工作区」原型（`.tmp/设计/`）的侧栏接进主控台。
 *
 * 【与 `ConsoleSidebar` 的关系：新增，不替换文件】
 * 本文件是 `ConsoleSidebar.tsx` 的**接替者**，但旧文件保留不删——渐进替换才有回退路径，
 * 旧侧栏的六域 IA 也仍是 `workspace-navigation.test.ts` 的对照物。差异只在「侧栏装什么」：
 * 旧侧栏把六域 37 条路由铺成可展开长列表（原型要修的正是这一点），新侧栏把四个工作区
 * 各装**自己的**入口（`workspace-navigation.ts` 的真实数据），且只做两件事：
 * 定位资源（节点 / 群组 / 收藏）+ 少数几个高频目的地。其余入口走顶栏搜索（Ctrl+K 命令面板）。
 *
 * 【四段结构（原型 `.sidebar`）】
 *   `SideNavHead`      工作区名 + pill，回答「我在哪个工作区」
 *   `SideNavShortcuts` 固定入口（服务器运维：平台首页 / 全部实例 / 节点，带计数）
 *   `SideNavResource`  资源区（唯一可伸缩段）：服务器运维=资源树，其他工作区=分组入口
 *   `SideNavBottom`    底部常驻（服务器运维：群组与拓扑 / 工作台等跨设施入口）
 *
 * 【能力对照：替换时哪些旧能力必须原样留下】
 * - 权限裁剪：与旧侧栏同语义同入口（`workspacesForPermissions` / `workspacesForRole`，FR-431）；
 * - 折叠态：仍由 `useConsoleStore.sidebarCollapsed` 驱动，宽度动画仍走 `jm-*` 那套 CSS
 *   （FR-496 阶段 6 补丁后，宽度过渡只作用于侧栏自身，见 {@link WorkspaceSidebar}）；
 * - 收藏 / 最近打开（FR-293）：`ServerSelector` + `SidebarServerList` 整体搬进「收藏」段，逻辑未改；
 * - 移动端：`MobileConsoleNav` 不动，本组件在 `<sm` 依旧整条隐藏（`hidden sm:flex`）。
 *
 * 【原缺口已闭合（FR-496 阶段 6 补丁）】
 * - 导航行的链接语义：`SideNavRow` 曾渲染 `<button>` + `navigate()`，拿不到 `<a href>` 的
 *   中键 / 新标签页语义。根因是 `<a>` 不允许嵌 `<button>`——只要行组件自己渲染 button，
 *   调用方就永远包不出真链接。现已把行组件改为渲染 `<div>`（只负责外观与激活态样式），
 *   由本文件的 `WorkspaceNavRow` 在外层包 `<Link>`，得到真 `<a href>`。
 *   行样式本身仍只有 ui 包一处真源，本文件不复制它的 class。
 * - 非服务器运维工作区在折叠态下只剩图标轨（原型该态显示分组图标行）：本阶段用
 *   「折叠态渲染全部入口图标」实现（见 {@link WorkspaceIconRail}）。
 */

/** 资源导航三段（原型 `.segments` 的三个切换项）。 */
type ResourceMode = 'nodes' | 'groups' | 'favorites'

/** 三段的文案键：复用既有 i18n（分组维度 / 常驻服务器列），不为侧栏另造同义键。 */
const RESOURCE_MODE_LABEL_KEY: Record<ResourceMode, string> = {
  nodes: 'grouping.dim_node',
  groups: 'grouping.dim_network',
  favorites: 'sidebarServers.favorites',
}

/** 一个分组里可直接导航的目的地（登记顺序不变）。 */
function navigableEntries(group: { children?: NavEntry[] }): NavEntry[] {
  return (group.children ?? []).filter((entry) => !isDeepLink(entry.to))
}

/**
 * 控制台侧栏（FR-496 阶段 6）：`ConsoleSidebar` 的接替者。默认导出，供 `DashboardPage` 直接挂载。
 *
 * 【折叠动画：宽度过渡只由侧栏自己承担（FR-496 阶段 6 补丁）】
 * 旧外壳把宽度过渡放在内层 `jm-sidebar-drawer`、再给内容区加 `clip-path` + `translate3d` 补偿，
 * 靠「JS 320ms 定时器 + CSS 320ms 过渡」两个时钟对齐来假装内容没动。补丁改为原型 `.sidebar` 的做法：
 * **外层 `jm-console-sidebar` 自己过渡宽度**（`index.css`），内层抽屉 `width: 100%` 跟随，
 * 内容区是同一 flex 行的兄弟节点，宽度变化时自然回流、不再被平移。
 * 因此这里只需保留抽屉容器（它仍带来明暗两套表面样式），不再需要「瞬跳 / 补偿」那套配对动画。
 */
/** 侧栏资源数据源（ADR-097：应用侧取数后注入；字段为组件所需投影）。 */
export interface WorkspaceSidebarData {
  /** 实例聚合权威总数（`/instances` 计数，与页眉同源）。 */
  instanceTotal?: number
  /** 节点列表。 */
  nodes?: NodeInfo[]
  /** 按节点的实例计数（聚合权威值，非已加载条数）。 */
  byNode?: { nodeId: number; count: number }[]
  /** 资源树首屏实例（前 N 条）。 */
  instances?: InstanceInfo[]
  /** 群组列表行。 */
  networks?: { id: number; name: string; memberCount: number; memberStatus: MemberStatusCounts }[]
}

/** 权限快照（应用侧注入；`loaded=false` 时不做裁剪，宁可短暂多给也不闪空白）。 */
export interface WorkspaceSidebarPermissions {
  nodes: Set<string>
  isPlatformAdmin: boolean
  loaded: boolean
}

/**
 * 链接渲染入参：与旧控制台侧栏共用同一份（见 {@link SidebarLinkArgs}），
 * 此处保留旧名以免已引用它的代码改动。
 */
export type WorkspaceLinkArgs = SidebarLinkArgs

export interface WorkspaceSidebarProps {
  /** 折叠态（应用侧 store 驱动）。 */
  collapsed: boolean
  /** 当前工作区（应用侧按权限与路径解析）。 */
  activeWorkspace: WorkspaceDef | null
  data?: WorkspaceSidebarData
  permissions: WorkspaceSidebarPermissions
  /** 当前路径（激活判定用；与工作区归属同一套优先级）。 */
  pathname: string
  /** 链接渲染（应用侧接 react-router 的 Link）。 */
  renderLink: (args: WorkspaceLinkArgs) => ReactNode
  /** 命令式跳转（资源树 / 群组行）。 */
  onNavigate: (to: string) => void
  /** 应用侧接线层组件：服务器选择器（收藏 / 最近）。 */
  renderServerSelector: () => ReactNode
  /** 应用侧接线层组件：常驻服务器列。 */
  renderSidebarServerList: () => ReactNode
  /** 版本号（Vite 注入常量由外壳提供）。 */
  appVersion: string
  /** 底部主题切换注入面（透传给受控的 {@link ThemeSwitcher}）。 */
  themeSwitcher: Pick<ThemeSwitcherProps, 'colorTheme' | 'theme' | 'onColorThemeChange' | 'onThemeChange'>
}

/**
 * 控制台侧栏（FR-496 阶段 6）：`ConsoleSidebar` 的接替者。
 *
 * 数据与路由一律由 props 注入（ADR-097）：本视图不触达 api / store / 路由。
 */
export function WorkspaceSidebar({
  collapsed,
  activeWorkspace,
  data,
  permissions,
  pathname,
  renderLink,
  onNavigate,
  renderServerSelector,
  renderSidebarServerList,
  appVersion,
  themeSwitcher,
}: WorkspaceSidebarProps) {
  const { t } = useTranslation()

  return (
    <SideNav
      collapsed={collapsed}
      data-slot="console-sidebar"
      data-state={collapsed ? 'collapsed' : 'expanded'}
      aria-label={activeWorkspace ? `${t(activeWorkspace.labelKey)}导航` : undefined}
      className="jm-console-sidebar hidden h-full min-h-0 shrink-0 sm:flex"
    >
      <div
        data-slot="sidebar-drawer"
        data-state={collapsed ? 'collapsed' : 'expanded'}
        className="jm-sidebar-drawer flex h-full min-h-0 flex-col"
      >
        {/* 抽屉是 grid（CSS 指定），只能有一个自动放置的子项；真正的四段列布局放在这一层。 */}
        <div data-slot="console-sidebar-surface" className="flex min-h-0 flex-col">
          {activeWorkspace && (
            <WorkspaceSections
              workspace={activeWorkspace}
              collapsed={collapsed}
              data={data}
              permissions={permissions}
              pathname={pathname}
              renderLink={renderLink}
              onNavigate={onNavigate}
              renderServerSelector={renderServerSelector}
              renderSidebarServerList={renderSidebarServerList}
              appVersion={appVersion}
              themeSwitcher={themeSwitcher}
            />
          )}
        </div>
      </div>
    </SideNav>
  )
}

/** 各段共用的注入面（避免逐层重复声明）。 */
interface SectionsInjected {
  data?: WorkspaceSidebarData
  permissions: WorkspaceSidebarPermissions
  pathname: string
  renderLink: (args: WorkspaceLinkArgs) => ReactNode
  onNavigate: (to: string) => void
  renderServerSelector: () => ReactNode
  renderSidebarServerList: () => ReactNode
  appVersion: string
  themeSwitcher: Pick<ThemeSwitcherProps, 'colorTheme' | 'theme' | 'onColorThemeChange' | 'onThemeChange'>
}

/**
 * 工作区切换（FR-496 阶段 6 补丁：已并回顶栏 `ConsoleHeader`，不再独立成行）。
 *
 * 阶段 6 曾在此处导出 `ConsoleWorkspaceBar`，以「不动 ConsoleHeader」为代价换渐进替换；
 * 本补丁把工作区切换并进 ui 包 `TopNav` 的 `workspaces` 槽位后，顶栏恢复成原型那样
 * **唯一一条 53px 通栏**（其下才是「侧栏 + 内容」），本模块不再导出顶栏部件，
 * 只保留侧栏自身与它消费的 {@link useWorkspaceNavigation} 数据源。
 */

/** 四段内容（按工作区分派：服务器运维有资源树与底部常驻，其他工作区给分组入口）。 */
function WorkspaceSections({
  workspace,
  collapsed,
  ...injected
}: { workspace: WorkspaceDef; collapsed: boolean } & SectionsInjected) {
  const { t } = useTranslation()
  const isOps = workspace.key === 'ops'
  // 工作区没有自己的图标字段：取首个分组的图标（服务器运维=总览、观测=活动、运营=玩家、平台=身份），
  // 与原型 `.workspace-label` 里那个「该工作区通用图标」同义。
  const HeadIcon = workspace.groups[0]?.icon
  // 标题点击回到该工作区落地页。直接复用 use-workspace-navigation 的 landingPathOf：
  // 它本就是「切工作区进该区第一页」的同一套规则（服务器运维 → 平台首页，观测 → 监控总览…），
  // 复用它才能保证「点标题」与「切工作区」落在同一个页面上，不会各自漂移。
  const landingTo = landingPathOf(workspace)

  // 标题此前是纯标签，但它位于侧栏顶部、由「图标 + 文字 + 徽章」构成，形状与位置都在说
  // 「这是导航元素」——用户会反复去点而无反应。现在点击回到本工作区落地页
  //（「标题回首页」是跨产品惯例）。
  // 可点态由外壳注入的链接承担，SideNavHead 只收到 `interactive`
  // 用来补悬停反馈——这样拿到的是真实 `<a href>`：可聚焦、支持中键与新标签页。
  const head = (
    <SideNavHead
      interactive={Boolean(landingTo)}
      icon={HeadIcon ? <HeadIcon /> : undefined}
      // pill 只给服务器运维：它确实是资源视图；其他工作区是功能导航，原型的 pill 也只在运维侧出现。
      pill={isOps ? t('resourceCard.viewGroup') : undefined}
    >
      {t(workspace.labelKey)}
    </SideNavHead>
  )

  return (
    <>
      {landingTo
        ? injected.renderLink({ to: landingTo, className: 'group block', children: head })
        : head}

      {isOps ? (
        <>
          <OpsShortcuts workspace={workspace} collapsed={collapsed} {...injected} />
          <OpsResourceSection {...injected} />
        </>
      ) : collapsed ? (
        <WorkspaceIconRail workspace={workspace} {...injected} />
      ) : (
        <WorkspaceGroupSections workspace={workspace} {...injected} />
      )}

      <SidebarBottom workspace={workspace} collapsed={collapsed} {...injected} />
    </>
  )
}

/** 服务器运维的固定三段（原型 `.main-shortcuts`）：平台首页 / 全部服务器 / 节点，后两项带计数。 */
function OpsShortcuts({
  workspace,
  collapsed,
  data,
  renderLink,
  pathname,
}: { workspace: WorkspaceDef; collapsed: boolean } & SectionsInjected) {
  // 计数与页眉同一数据源：聚合查询键与 `ConsoleHeader` 一致，命中它的缓存、不额外发请求。
  const counts: Record<string, number | undefined> = {
    '/instances': data?.instanceTotal,
    '/nodes': data?.nodes?.length,
  }
  const resources = workspace.groups.find((group) => group.key === 'opsResources') ?? workspace.groups[0]
  const entries = resources ? navigableEntries(resources) : []
  if (entries.length === 0) return null

  return (
    <SideNavShortcuts>
      {entries.map((entry) => (
        <WorkspaceNavRow
          key={entry.to}
          entry={entry}
          collapsed={collapsed}
          count={counts[entry.to]}
          renderLink={renderLink}
          pathname={pathname}
        />
      ))}
    </SideNavShortcuts>
  )
}

/**
 * 侧栏底部常驻段（原型 `.sidebar-bottom`），两件事：
 *
 * 1. 服务器运维的跨设施入口（群组与拓扑 / 工作台）：取 `opsGroupsWorkbench` 分组
 *    （权限裁剪后的真实登记）而不是硬编码两条——原型的两条只是这四个目的地的概称，
 *    按登记渲染才不会丢「分组管理 / 导播台」。其他工作区的跨设施入口在各自的分组列表里。
 * 2. 偏好块（`SidebarPreferences`）：主题色 / 明暗、版本号、开源许可。
 *
 * 底部段对**所有工作区**都存在（第二件事与工作区无关），因此主题切换在任何工作区都够得着。
 */
function SidebarBottom({
  workspace,
  collapsed,
  ...injected
}: { workspace: WorkspaceDef; collapsed: boolean } & SectionsInjected) {
  const group = workspace.key === 'ops' ? workspace.groups.find((item) => item.key === 'opsGroupsWorkbench') : undefined
  const entries = group ? navigableEntries(group) : []

  return (
    <SideNavBottom>
      {entries.map((entry) => (
        <WorkspaceNavRow
          key={entry.to}
          entry={entry}
          collapsed={collapsed}
          renderLink={injected.renderLink}
          pathname={injected.pathname}
        />
      ))}
      <SidebarPreferences
        collapsed={collapsed}
        appVersion={injected.appVersion}
        renderLink={injected.renderLink}
        themeSwitcher={injected.themeSwitcher}
      />
    </SideNavBottom>
  )
}

/**
 * 底部偏好块（FR-132 版本号与许可 / FR-164 主题色与明暗）。
 *
 * 为什么在「照原型换侧栏」时还留着它：这三项当前**只有侧栏这一个入口**——
 * `/settings` 的「外观」分组明说不重复主题色（见 `SettingsPage` 注释，FR-164），
 * 版本号也只在旧侧栏页脚渲染。原型把「外观」收进 `/settings`、把 `/licenses` 当普通页面，
 * 但那是**阶段 7 的迁移目标**：在 `/settings` 真正接住主题色与版本之前删掉本块，
 * 等于把已交付的入口抹掉。阶段 7 补齐后本块即可整段删除。
 * 语言切换不在此列：`/settings` 的「外观」已有等价入口，故不再重复一份。
 */
function SidebarPreferences({
  collapsed,
  appVersion,
  renderLink,
  themeSwitcher,
}: {
  collapsed: boolean
  appVersion: string
  renderLink: (args: WorkspaceLinkArgs) => ReactNode
  themeSwitcher: Pick<ThemeSwitcherProps, 'colorTheme' | 'theme' | 'onColorThemeChange' | 'onThemeChange'>
}) {
  const { t } = useTranslation()

  return (
    <div data-slot="console-sidebar-preferences" className="mt-[6px] border-t border-border/60 pt-[7px]">
      {/* 【为什么折叠态不再 `flex-col`】`flex-direction` **不参与过渡**（瞬跳）：折叠瞬间
          按钮从横排跳成纵排，紧接着侧栏宽度过渡又把它们挤一次——布局连算两遍，
          视觉上就是「抖两下」。折叠态这里只有 ThemeSwitcher 一个按钮且已是纯图标形态，
          54px 宽放得下横排，保持同一方向、只切换对齐即可，不必换布局方向。 */}
      <div className={cn('flex items-center gap-2', collapsed && 'justify-center')}>
        {/* `compact` 与整栏折叠态同步：折叠态本来就是纯图标。 */}
        <ThemeSwitcher compact={collapsed} {...themeSwitcher} />
      </div>

      {/* 版本号 + 开源许可行：原先用 `{!collapsed && …}` 条件渲染，折叠即卸载 →
          又一次布局跳动。改用 grid-rows 收起高度 + 淡出，与侧栏宽度同节拍。
          `inert` 补齐语义：视觉收起后元素仍在 DOM，不加它读屏与 Tab 仍会走到「开源许可」。 */}
      <div
        inert={collapsed ? true : undefined}
        className={cn(
          'grid transition-[grid-template-rows,opacity] duration-[var(--motion-duration-slow)] ease-ios',
          collapsed ? 'grid-rows-[0fr] opacity-0' : 'grid-rows-[1fr] opacity-100',
        )}
      >
        <div className="overflow-hidden">
          <div className="flex items-center justify-between gap-2 px-1 pt-1.5">
            <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground/80">
              <span className="size-1.5 rounded-full bg-status-success" />
              v{appVersion}
            </span>
            {renderLink({
              to: '/licenses',
              className:
                'flex items-center gap-1 rounded text-[11px] text-muted-foreground/70 transition-colors hover:text-foreground hover:underline',
              children: (
                <>
                  <Scale className="size-3 shrink-0" />
                  {t('licenses.entry')}
                </>
              ),
            })}
          </div>
        </div>
      </div>
    </div>
  )
}

/**
 * 非服务器运维工作区的展开态：按分组列出该工作区的全部入口（原型 `.section-nav` +
 * `.section-title`：平台管理「拥有独立且清楚的五组导航」）。
 * 资源区是侧栏唯一可滚动段，工作区入口多的（平台 14 条）也不会把底栏顶出屏幕。
 */
function WorkspaceGroupSections({ workspace, renderLink, pathname }: { workspace: WorkspaceDef } & SectionsInjected) {
  const { t } = useTranslation()

  return (
    <SideNavResource>
      <div data-slot="console-sidebar-groups" className="min-h-0 flex-1 overflow-y-auto scrollbar-none px-[9px] pb-[14px] pt-[3px]">
        {workspace.groups.map((group) => {
          const entries = navigableEntries(group)
          if (entries.length === 0) return null
          return (
            <div key={group.key} data-slot="console-sidebar-group">
              <div className="px-[10px] pb-[5px] pt-[12px] text-[10px] tracking-[.5px] text-muted-foreground/70">
                {t(group.labelKey)}
              </div>
              {entries.map((entry) => (
                <WorkspaceNavRow key={entry.to} entry={entry} collapsed={false} renderLink={renderLink} pathname={pathname} />
              ))}
            </div>
          )
        })}
      </div>
    </SideNavResource>
  )
}

/**
 * 非服务器运维工作区的折叠态：只剩 54px，装不下分组标题与文字，改为把该工作区的
 * 全部入口压成图标行（原型 `.sidebar.collapsed .section-nav` 就是这个形态）。
 * 图标行自带 title，鼠标悬停即知去向；行数多时该段自行滚动（折叠态 `SideNavShortcuts` 撑满余高）。
 */
function WorkspaceIconRail({ workspace, renderLink, pathname }: { workspace: WorkspaceDef } & SectionsInjected) {
  const entries = workspace.groups.flatMap((group) => navigableEntries(group))
  if (entries.length === 0) return null

  return (
    <SideNavShortcuts className="overflow-y-auto scrollbar-none">
      {entries.map((entry) => (
        <WorkspaceNavRow key={entry.to} entry={entry} collapsed renderLink={renderLink} pathname={pathname} />
      ))}
    </SideNavShortcuts>
  )
}

/**
 * 服务器运维的资源区（原型 `.resource-head` + `.resource-search` + `.resource-scroll`）。
 *
 * 三段分工：
 * - **按节点**：`ResourceTree`（节点 → 实例，每节点默认 7 行，其余折叠成跳实例列表的链接）；
 * - **按群组**：群组（Network 软标签）的成员数与健康点，点击进按群组过滤的实例列表。
 *   原型在群组下内联展开成员，本阶段改为跳列表——为每个群组各拉一次详情会让侧栏在
 *   大集群上发出一串请求，收益却只是少点一次；
 * - **收藏**：`ServerSelector` + 常驻服务器列（FR-293 收藏 / 最近打开）。这是旧侧栏的既有能力，
 *   替换时必须原样保留，所以整体搬进来而不是重写。
 *
 * 三段本身按权限收敛（没 node.read 就不该出现节点树），一个都不剩时整段不渲染。
 */
function OpsResourceSection({
  data,
  permissions,
  onNavigate,
  renderServerSelector,
  renderSidebarServerList,
}: SectionsInjected) {
  const { t } = useTranslation()
  const [mode, setMode] = useState<ResourceMode>('nodes')
  // 权限还没加载时不做隐藏（宁可短暂多给，也不要首帧闪出「资源导航」空白）。
  const can = (perm: string) =>
    !permissions.loaded || permissions.isPlatformAdmin || permissions.nodes.has(perm)
  const modes: ResourceMode[] = []
  if (can('node.read') && can('instance.read')) modes.push('nodes')
  if (can('network.read')) modes.push('groups')
  modes.push('favorites')

  // 权限裁剪让当前段失效时（例如登录后权限下落）自动落到第一个可见段，而不是留下空白。
  const activeMode = modes.includes(mode) ? mode : modes[0]
  if (!activeMode) return null

  return (
    <SideNavResource>
      <div data-slot="resource-nav-head" className="px-[14px] pb-[10px] pt-[15px]">
        <div className="mb-[10px] text-[10px] tracking-[.8px] text-muted-foreground/70">{t('nav.resources')}</div>
        <div
          data-slot="resource-nav-segments"
          className="flex w-full items-center gap-[2px] rounded-md border border-border bg-muted p-[3px]"
        >
          {modes.map((item) => (
            <button
              key={item}
              type="button"
              aria-pressed={item === activeMode}
              data-active={item === activeMode || undefined}
              onClick={() => setMode(item)}
              className={cn(
                'flex-1 whitespace-nowrap rounded-[4px] px-[5px] py-1 text-[11px] text-muted-foreground',
                'transition-colors duration-[var(--motion-duration-normal)] ease-ios hover:text-foreground',
                item === activeMode && 'bg-card font-[560] text-foreground shadow-soft',
              )}
            >
              {t(RESOURCE_MODE_LABEL_KEY[item])}
            </button>
          ))}
        </div>
      </div>

      {/* 「定位」入口常驻：三段都靠它做跨全量的服务端搜索，本地的树/列表只负责就近挑选。
          选择器本体是应用侧接线层（自带取数与路由），故由外壳注入渲染。 */}
      <div data-slot="resource-nav-selector" className="mx-[14px] mb-[9px]">
        {renderServerSelector()}
      </div>

      {activeMode === 'nodes' && (
        <NodeResourceList data={data} permissions={permissions} onNavigate={onNavigate} />
      )}
      {activeMode === 'groups' && <GroupResourceList data={data} onNavigate={onNavigate} />}
      {activeMode === 'favorites' && (
        <div data-slot="resource-nav-favorites" className="min-h-0 flex-1 overflow-y-auto scrollbar-none px-[9px] pb-[14px]">
          {renderSidebarServerList()}
        </div>
      )}
    </SideNavResource>
  )
}

/** 「按节点」段：节点 → 实例两级资源树（数据源与实例列表 / 聚合同源）。 */
function NodeResourceList({
  data,
  permissions,
  onNavigate,
}: Pick<SectionsInjected, 'data' | 'permissions' | 'onNavigate'>) {
  const [query, setQuery] = useState('')
  const canReadInstances =
    !permissions.loaded || permissions.isPlatformAdmin || permissions.nodes.has('instance.read')

  const tree = useMemo(
    () =>
      buildResourceNodes(
        data?.nodes ?? [],
        // 无 instance.read 时（理论上已被段可见性挡住）不摆实例，避免展示注定 403 的内容。
        canReadInstances ? (data?.instances ?? []) : [],
        data?.byNode ?? [],
      ),
    [data, canReadInstances],
  )

  return (
    <ResourceTree
      nodes={tree}
      query={query}
      onQueryChange={setQuery}
      onSelectNode={(node) => onNavigate(`/nodes?node=${node.id}`)}
      onSelectInstance={(instance) => onNavigate(`/instances/${instance.id}`)}
      onShowAll={(node) => onNavigate(`/instances?nodeId=${node.id}`)}
    />
  )
}

/**
 * 「按群组」段：群组（Network 软标签）成员数 + 健康点，点击进 `?networkId=` 过滤后的实例列表。
 * 请求按需发生：本组件只在选中该段时挂载，`useNetworks` 因此不会在三段之外被触发。
 */
function GroupResourceList({ data, onNavigate }: Pick<SectionsInjected, 'data' | 'onNavigate'>) {
  const { t } = useTranslation()
  const rows = data?.networks ?? []

  return (
    <div data-slot="resource-nav-groups" className="min-h-0 flex-1 overflow-y-auto scrollbar-none px-[9px] pb-[14px]">
      {rows.length === 0 ? (
        <p className="px-[10px] py-[10px] text-[11px] text-muted-foreground">{t('networks.empty')}</p>
      ) : (
        rows.map((network) => (
          <button
            key={network.id}
            type="button"
            title={network.name}
            onClick={() => onNavigate(`/instances?networkId=${network.id}`)}
            className="flex h-[33px] w-full items-center gap-[6px] rounded-[5px] px-[6px] text-left transition-colors duration-[var(--motion-duration-normal)] ease-ios hover:bg-muted"
          >
            <span className={cn('size-1.5 shrink-0 rounded-full', groupHealthClass(network.memberStatus))} aria-hidden />
            <span className="min-w-0 flex-1 truncate text-[11px] font-[570]">{network.name}</span>
            <span className="min-w-[24px] shrink-0 text-right font-mono text-[10px] text-muted-foreground/70">
              {network.memberCount}
            </span>
          </button>
        ))
      )}
    </div>
  )
}

/**
 * 一行工作区导航（FR-496 阶段 6）：统一 `SideNavRow` 的调用口径。
 * 行样式只有 ui 包一处真源，这里只负责「激活判定 / 折叠态可访问名 / 链接语义」三件事。
 *
 * 语义是**链接**，所以外层包 `<Link>` 拿真 `<a href>`：可聚焦、支持中键与 Ctrl+点击、
 * 能复制链接地址、读屏念作链接。`aria-current` / `aria-label` / `title` 都落在 `<a>` 上
 * （这些语义要有可聚焦元素承载才生效），`SideNavRow` 只负责行的外观与激活态样式。
 */
function WorkspaceNavRow({
  entry,
  collapsed,
  count,
  renderLink,
  pathname,
}: {
  entry: NavEntry
  collapsed: boolean
  count?: number
  renderLink: (args: WorkspaceLinkArgs) => ReactNode
  pathname: string
}) {
  const { t } = useTranslation()
  const label = t(entry.labelKey)
  const Icon = entry.icon
  const active = currentPatternOf(pathname) === entry.to

  return renderLink({
    to: entry.to,
    ariaCurrent: active ? 'page' : undefined,
    // 折叠态只剩图标：文字节点被 `hidden` 隐藏，无障碍名必须由 aria-label 兜住。
    ariaLabel: collapsed ? label : undefined,
    title: label,
    className: 'block',
    children: (
      <SideNavRow icon={Icon ? <Icon /> : undefined} count={count} active={active}>
        {label}
      </SideNavRow>
    ),
  })
}

/**
 * 当前路径命中的登记项（`workspace-navigation.ts` 的匹配结果）。
 * 激活判定统一走它而不是各写一遍 `startsWith`：这样 `/instances/new` 归向导、
 * `/instances/:id` 归实例控制台，与工作区归属判定同一套优先级。
 */
function currentPatternOf(pathname: string): string | null {
  return resolveWorkspacePath(pathname)?.pattern ?? null
}

/** 节点 + 已加载实例 → 资源树的 `ResourceNode[]`。 */
function buildResourceNodes(
  nodes: NodeInfo[],
  instances: InstanceInfo[],
  byNode: { nodeId: number; count: number }[],
): ResourceNode[] {
  const instanceCounts = new Map(byNode.map((item) => [item.nodeId, item.count]))
  const grouped = new Map<number, InstanceInfo[]>()
  for (const instance of instances) {
    const list = grouped.get(instance.nodeId)
    if (list) list.push(instance)
    else grouped.set(instance.nodeId, [instance])
  }

  return nodes.map((node) => {
    const members = grouped.get(node.id) ?? []
    return {
      id: String(node.id),
      name: node.name,
      // 节点 status：1=在线，其余（离线/启动中）统一按「非异常但不可用」的灰点处理。
      status: node.status === 1 ? 'normal' : 'muted',
      // 计数一律用聚合的权威总数：实例列表只加载了前 N 条，用已加载条数会少报，
      // 让「查看其余 N 个实例」变成假话。
      instanceCount: instanceCounts.get(node.id) ?? members.length,
      instances: members.map((instance) => ({
        id: String(instance.id),
        name: instance.name,
        status: instanceResourceStatus(instance.status),
      })),
    }
  })
}

/** 实例状态 → 资源树四态。复用仓库既有的状态分类（`statusDotKind`），避免与列表页的点色漂移。 */
function instanceResourceStatus(status: string): ResourceStatus {
  switch (statusDotKind(status)) {
    case 'running':
      return 'normal'
    case 'transitioning':
      return 'warn'
    case 'crashed':
      return 'bad'
    default:
      return 'muted'
  }
}

/** 群组健康点：有崩溃成员=红、有过渡中成员=琥珀、一个都没在跑=灰、否则绿。 */
function groupHealthClass(counts: MemberStatusCounts): string {
  if (counts.crashed > 0) return 'bg-status-danger'
  if (counts.starting > 0 || counts.stopping > 0) return 'bg-status-warning'
  if (counts.running === 0) return 'bg-muted-foreground/50'
  return 'bg-status-success'
}