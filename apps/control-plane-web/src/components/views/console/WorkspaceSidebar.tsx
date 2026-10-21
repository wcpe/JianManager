import { type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, Languages, PanelLeftClose, PanelLeftOpen, Scale } from 'lucide-react'

import type { NodeInfo } from '@/lib/nodes/node-types'
import { cn } from '@jianmanager/ui'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import {
  SideNav,
  SideNavBottom,
  SideNavResource,
  SideNavRow,
  SideNavShortcuts,
} from '@jianmanager/ui/components/shell'
import { ThemeSwitcher } from '@/components/views/console/ThemeSwitcher'
import type { ThemeSwitcherProps } from '@/components/views/console/ThemeSwitcher'
import type { SidebarLinkArgs } from '@/components/views/console/sidebar-link'
import { SidebarPinnedTools, SidebarToolRail } from '@/components/views/console/SidebarToolRail'
import type { NavEntry } from '@/lib/shared/nav-config'
import { logoToggleLabelKey } from '@/lib/shared/sidebar-logo'
import { resolveWorkspacePath } from '@/lib/console/workspace-navigation'
import type { WorkspaceDef } from '@/lib/console/workspace-navigation'
import { isDeepLink } from '@/lib/hooks/use-workspace-navigation'

/**
 * 工作区外壳侧栏（FR-502 阶段 6）：把「资源优先工作区」原型（`.tmp/设计/`）的侧栏接进主控台。
 *
 * 【与 `ConsoleSidebar` 的关系：新增，不替换文件】
 * 本文件是 `ConsoleSidebar.tsx` 的**接替者**，但旧文件保留不删——渐进替换才有回退路径，
 * 旧侧栏的六域 IA 也仍是 `workspace-navigation.test.ts` 的对照物。差异只在「侧栏装什么」：
 * 旧侧栏把六域 37 条路由铺成可展开长列表（原型要修的正是这一点），新侧栏把四个工作区
 * 各装**自己的**入口（`workspace-navigation.ts` 的真实数据），且只做两件事：
 * 定位资源（节点 / 群组 / 收藏）+ 少数几个高频目的地。其余入口走顶栏搜索（Ctrl+K 命令面板）。
 *
 * 【四段结构（重构件后的现状）】
 *   `SideNavShortcuts` 固定入口（服务器运维：平台首页 / 全部实例 / 节点，紧凑高度）——
 *                      工作区名标题行与分组小标题**已移除**（「侧栏不留标题」是本次要求）；
 *   常用区            置回的工具（`SidebarPinnedTools`，偏好见 lib/console/sidebar-tools）；
 *   `SideNavResource`  资源区（唯一可伸缩段）：服务器运维=资源面板（搜索 + 筛选 + 分组方式 +
 *                      服务器列表，见 SidebarResourcePanel），其他工作区=扁平入口列表；
 *   `SideNavBottom`    底部常驻：工具图标轨（网络拓扑/分组管理/超级工作台/导播台 + 「更多」
 *                      收纳菜单，见 SidebarToolRail）与偏好块。
 *
 * 【能力对照：替换时哪些旧能力必须原样留下】
 * - 权限裁剪：与旧侧栏同语义同入口（`workspacesForPermissions` / `workspacesForRole`，FR-431）；
 * - 折叠态：仍由 `useConsoleStore.sidebarCollapsed` 驱动，宽度动画仍走 `jm-*` 那套 CSS
 *   （FR-502 阶段 6 补丁后，宽度过渡只作用于侧栏自身，见 {@link WorkspaceSidebar}）；
 * - 收藏 / 最近打开（FR-293/240）：并入资源面板（置顶=收藏），存储层未改；
 * - 移动端：`MobileConsoleNav` 不动，本组件在 `<sm` 依旧整条隐藏（`hidden sm:flex`）。
 *
 * 【原缺口已闭合（FR-502 阶段 6 补丁）】
 * - 导航行的链接语义：`SideNavRow` 曾渲染 `<button>` + `navigate()`，拿不到 `<a href>` 的
 *   中键 / 新标签页语义。根因是 `<a>` 不允许嵌 `<button>`——只要行组件自己渲染 button，
 *   调用方就永远包不出真链接。现已把行组件改为渲染 `<div>`（只负责外观与激活态样式），
 *   由本文件的 `WorkspaceNavRow` 在外层包 `<Link>`，得到真 `<a href>`。
 *   行样式本身仍只有 ui 包一处真源，本文件不复制它的 class。
 * - 非服务器运维工作区在折叠态下只剩图标轨（原型该态显示分组图标行）：本阶段用
 *   「折叠态渲染全部入口图标」实现（见 {@link WorkspaceIconRail}）。
 */

/** 一个分组里可直接导航的目的地（登记顺序不变）。 */
function navigableEntries(group: { children?: NavEntry[] }): NavEntry[] {
  return (group.children ?? []).filter((entry) => !isDeepLink(entry.to))
}

/**
 * 控制台侧栏（FR-502 阶段 6）：`ConsoleSidebar` 的接替者。默认导出，供 `DashboardPage` 直接挂载。
 *
 * 【折叠动画：宽度过渡只由侧栏自己承担（FR-502 阶段 6 补丁）】
 * 旧外壳把宽度过渡放在内层 `jm-sidebar-drawer`、再给内容区加 `clip-path` + `translate3d` 补偿，
 * 靠「JS 320ms 定时器 + CSS 320ms 过渡」两个时钟对齐来假装内容没动。补丁改为原型 `.sidebar` 的做法：
 * **外层 `jm-console-sidebar` 自己过渡宽度**（`index.css`），内层抽屉 `width: 100%` 跟随，
 * 内容区是同一 flex 行的兄弟节点，宽度变化时自然回流、不再被平移。
 * 因此这里只需保留抽屉容器（它仍带来明暗两套表面样式），不再需要「瞬跳 / 补偿」那套配对动画。
 */
/** 侧栏资源数据源（ADR-102：应用侧取数后注入；字段为组件所需投影）。 */
export interface WorkspaceSidebarData {
  /** 实例聚合权威总数（`/instances` 计数，与页眉同源）。 */
  instanceTotal?: number
  /** 节点列表。 */
  nodes?: NodeInfo[]
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
  /** 应用侧接线层组件：资源面板（搜索 + 筛选 + 分组方式 + 服务器列表）。 */
  renderResourcePanel: () => ReactNode
  /** 版本号（Vite 注入常量由外壳提供）。 */
  appVersion: string
  /** 语言切换（与主题/「更多」并排在同一行）。 */
  language: { current: string; onChange: (lng: string) => void }
  /**
   * 折叠/展开侧栏。
   *
   * 【为什么底部也要一个】顶栏那个按钮在折叠态是 `{!collapsed && …}`（DOM 卸载），
   * 折叠后**整个页面再也找不到展开入口**——用户实测「缩回去就无法展开」。
   * 侧栏底部这个按钮两种状态都常驻，图标与文案随状态切换（收起 ↔ 展开）。
   */
  onToggleSidebar: () => void
  /** 底部主题切换注入面（透传给受控的 {@link ThemeSwitcher}）。 */
  themeSwitcher: Pick<ThemeSwitcherProps, 'colorTheme' | 'theme' | 'onColorThemeChange' | 'onThemeChange'>
}

/**
 * 控制台侧栏（FR-502 阶段 6）：`ConsoleSidebar` 的接替者。
 *
 * 数据与路由一律由 props 注入（ADR-102）：本视图不触达 api / store / 路由。
 */
export function WorkspaceSidebar({
  collapsed,
  activeWorkspace,
  data,
  permissions,
  pathname,
  renderLink,
  onNavigate,
  renderResourcePanel,
  appVersion,
  themeSwitcher,
  language,
  onToggleSidebar,
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
        {/*
          抽屉是 grid（CSS 指定，`grid-template-rows: minmax(0,1fr)`），只能有一个自动放置的
          子项；真正的四段列布局放在这一层。

          【必须有 `h-full` + `min-h-0`】grid 子项默认 `min-height:auto`，本层又是 flex 列容器：
          资源区（`flex-1`）内容再多也会把本层撑到内容高度，底部常驻段随之下移到视口之外——
          这正是「底部的『显示在侧边栏』显示不出来」的成因。`h-full` 把高度钉在抽屉行高上，
          `min-h-0` 允许它收缩，`overflow-hidden` 保证超出部分由资源区自己滚。
        */}
        <div data-slot="console-sidebar-surface" className="flex h-full min-h-0 flex-col overflow-hidden">
          {activeWorkspace && (
            <WorkspaceSections
              workspace={activeWorkspace}
              collapsed={collapsed}
              data={data}
              permissions={permissions}
              pathname={pathname}
              renderLink={renderLink}
              onNavigate={onNavigate}
              renderResourcePanel={renderResourcePanel}
              appVersion={appVersion}
              themeSwitcher={themeSwitcher}
              language={language}
              onToggleSidebar={onToggleSidebar}
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
  renderResourcePanel: () => ReactNode
  appVersion: string
  themeSwitcher: Pick<ThemeSwitcherProps, 'colorTheme' | 'theme' | 'onColorThemeChange' | 'onThemeChange'>
  /** 语言切换（`/settings` 也有等价入口，但侧栏底部需要与主题/更多并排的一处快捷入口）。 */
  language: { current: string; onChange: (lng: string) => void }
  /** 折叠/展开侧栏（底部常驻，折叠态仍可展开）。 */
  onToggleSidebar: () => void
}

/**
 * 工作区切换（FR-502 阶段 6 补丁：已并回顶栏 `ConsoleHeader`，不再独立成行）。
 *
 * 阶段 6 曾在此处导出 `ConsoleWorkspaceBar`，以「不动 ConsoleHeader」为代价换渐进替换；
 * 本补丁把工作区切换并进 ui 包 `TopNav` 的 `workspaces` 槽位后，顶栏恢复成原型那样
 * **唯一一条 53px 通栏**（其下才是「侧栏 + 内容」），本模块不再导出顶栏部件，
 * 只保留侧栏自身与它消费的 {@link useWorkspaceNavigation} 数据源。
 */

/** 四段内容（按工作区分派：服务器运维有工具轨/资源面板，其他工作区给扁平入口列表）。 */
function WorkspaceSections({
  workspace,
  collapsed,
  ...injected
}: { workspace: WorkspaceDef; collapsed: boolean } & SectionsInjected) {
  const isOps = workspace.key === 'ops'
  // 工具集合（网络拓扑/分组管理/超级工作台/导播台）的真源：ops「群组与工作台」组。
  const toolEntries = isOps
    ? navigableEntries(workspace.groups.find((group) => group.key === 'opsGroupsWorkbench') ?? {})
    : []

  return (
    <>
      {isOps ? (
        <>
          <OpsShortcuts workspace={workspace} collapsed={collapsed} {...injected} />
          <SideNavResource>{injected.renderResourcePanel()}</SideNavResource>
        </>
      ) : collapsed ? (
        <WorkspaceIconRail workspace={workspace} {...injected} />
      ) : (
        <WorkspaceGroupSections workspace={workspace} {...injected} />
      )}

      <SidebarBottom workspace={workspace} collapsed={collapsed} toolEntries={toolEntries} {...injected} />
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
    // 紧凑高度（重构件要求）：行高 36→30、字号 11px；标题行移除后顶部补回一点留白。
    <SideNavShortcuts className="pb-[10px] pt-[10px] [&_[data-slot=side-nav-row]]:min-h-[30px] [&_[data-slot=side-nav-row]]:text-[11px]">
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
 /**
  * 侧栏底部常驻段（重构件后）：工具图标轨 + 偏好块。
  *
  * 1. 工具图标轨（`SidebarToolRail`）：网络拓扑 / 分组管理 / 超级工作台 / 导播台 收纳为纯图标，
  *    末尾「更多」菜单可把工具置回侧栏主体（偏好见 lib/console/sidebar-tools）；
  *    图标语义落在真 `<a href>` 上，中键 / 新标签页可用。
  * 2. 偏好块（`SidebarPreferences`）：主题色 / 明暗、版本号、开源许可。
  *
  * 底部段对**所有工作区**都存在（第二件事与工作区无关），因此主题切换在任何工作区都够得着；
  * 工具轨只在服务器运维（其工具集合真源所在）渲染。
  */
 function SidebarBottom({
   collapsed,
   toolEntries,
   ...injected
 }: { workspace: WorkspaceDef; collapsed: boolean; toolEntries: NavEntry[] } & SectionsInjected) {
   // 底部段竖直内边距按本侧栏的实际内容收紧（展开态 10px → 6px）。
   // 写在 className 上而不是改 ui 包默认值：`SideNavBottom` 是通用容器，
   // 折叠态 padding 还参与过渡（`p-[8px_6px]`），改默认值会影响其他页面。
   return (
     <SideNavBottom className="py-[6px]">
       {/*
         两段：
         1. **显示在侧边栏**的工具行（用户从「更多」里拖回来的，`SidebarPinnedTools`）——
            这里是它们唯一的落脚点。此前把工具整体收进菜单时漏了这一半，
            导致「拖进『显示在侧边栏』之后哪儿都不出现」（本次修复）。
            一个都没放时整段不渲染，底部分隔线也随之消失。
         2. 偏好块：主题 / 明暗 / 语言 / 更多 同一行 + 版本号与开源协议。
       */}
       <SidebarPinnedTools
         entries={toolEntries}
         pathname={injected.pathname}
         renderLink={injected.renderLink}
         collapsed={collapsed}
       />
       <SidebarPreferences
         collapsed={collapsed}
         appVersion={injected.appVersion}
         renderLink={injected.renderLink}
         themeSwitcher={injected.themeSwitcher}
         language={injected.language}
         onToggleSidebar={injected.onToggleSidebar}
         moreMenu={toolEntries.length > 0
           ? <SidebarToolRail entries={toolEntries} pathname={injected.pathname} renderLink={injected.renderLink} />
           : undefined}
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
  language,
  moreMenu,
  onToggleSidebar,
}: {
  collapsed: boolean
  appVersion: string
  renderLink: (args: WorkspaceLinkArgs) => ReactNode
  themeSwitcher: Pick<ThemeSwitcherProps, 'colorTheme' | 'theme' | 'onColorThemeChange' | 'onThemeChange'>
  language: { current: string; onChange: (lng: string) => void }
  /** 三个点「更多」入口：与主题/语言并排在同一行（用户指定）。 */
  moreMenu?: ReactNode
  /**
   * 上方是否有「显示在侧边栏」的工具行。
   *
   * 决定本块顶部那条分隔线要不要画：**一个工具都没置回时，上面的工具段整段不渲染，
   * 这条线下面也没有任何工具**，留着就只是凭空一条横线（用户明确指出要去掉）。
   */
  onToggleSidebar: () => void
}) {
  const { t } = useTranslation()

  return (
    <div
      data-slot="console-sidebar-preferences"
      className={cn(
        /*
          【间距收敛】此前是 `mt-[6px] pt-[7px]`，叠加 `SideNavBottom` 自身的 `p-[10px]`
          与版本行 `pt-1.5`，在没有工具行时底部区高达 85px（其中 33px 全是留白）——
          用户反馈「没有放入按钮的时候过于高」。这里把外边距与上内边距压到 2/4，
          总留白 20px → 低于一行按钮高度，视觉上紧贴边界线。
        */
        'mt-[2px] shrink-0 pt-[4px]',
      )}
    >
      {/*
        一行放齐：主题色 / 明暗（`ThemeSwitcher` 自带两个按钮）+ 语言 + 三个点「更多」。
        【为什么折成一行】此前它们是两行（主题一行、更多与版本一行），在 246px 宽的侧栏里
        白白吃掉两行视觉重量；用户明确要求并排。
        折叠态只隐藏右侧的语言/更多（宽度不够），主题按钮居中——与既有折叠行为一致。
      */}
      <div className={cn('flex items-center gap-1.5', collapsed && 'justify-center')}>
        {/*
          【折叠态整块不渲染】用户指定：缩放（折叠）后底部只保留折叠按钮——主题色/明暗/语言
          全部撤走，否则 54px 宽里塞一个配色按钮既挤又容易误触。
          折叠后这些入口并非消失：展开一次即可，且明暗/语言在 `/settings` 的「外观」也有等价入口。
          用条件渲染而非 `hidden`：折叠态不需要它们的 DOM（也不该被 Tab 走到）。
        */}
        {!collapsed && <ThemeSwitcher compact={collapsed} {...themeSwitcher} />}
        {!collapsed && <LanguageButton language={language} />}
        {/* 「更多」与折叠开关靠右；折叠态只剩折叠开关（唯一出口，必须常驻）。 */}
        <div className={cn('ms-auto flex items-center gap-1', collapsed && 'ms-0')}>
          {!collapsed && moreMenu}
          <button
            type="button"
            onClick={onToggleSidebar}
            /* 标签描述「将发生的动作」而非当前态：展开时=收起、折叠时=展开。
               复用 `logoToggleLabelKey` 而不是在这里写三元——那条规则原先服务顶栏 logo，
               现在顶栏按钮已删、只剩这一处开关，但「按折叠态取标签」的规则是同一份。 */
            aria-label={t(logoToggleLabelKey(collapsed))}
            title={t(logoToggleLabelKey(collapsed))}
            data-testid="sidebar-collapse-toggle"
            className="flex size-[22px] shrink-0 items-center justify-center rounded-md text-muted-foreground/70 transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            {collapsed ? <PanelLeftOpen className="size-[15px]" /> : <PanelLeftClose className="size-[15px]" />}
          </button>
        </div>
      </div>

      {/* 版本号 + 开源协议：仍在最下，与上面一行之间不留额外间距（视觉上是一块）。 */}
      <div
        inert={collapsed ? true : undefined}
        className={cn(
          'grid transition-[grid-template-rows,opacity] duration-[var(--motion-duration-slow)] ease-ios',
          collapsed ? 'grid-rows-[0fr] opacity-0' : 'grid-rows-[1fr] opacity-100',
        )}
      >
        <div className="overflow-hidden">
          {/* 版本行与上面一行按钮之间只留 2px：两行本是同一块，多余留白只会把底部区撑高。 */}
          <div className="flex items-center justify-between gap-2 px-1 pt-[2px]">
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
 * 语言切换（中/英两档，与 `/settings` 的「外观」同源）。
 *
 * 为什么用下拉而不是一个「切换」按钮：当前语言要能一眼看出来；两档下拉的点击成本
 * 与切换按钮相同，但不会在误触后需要再点一次才能回去。
 */
function LanguageButton({ language }: { language: { current: string; onChange: (lng: string) => void } }) {
  const { t } = useTranslation()
  const current = language.current.startsWith('en') ? 'en' : 'zh'
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={t('settings.language')}
          title={t('settings.language')}
          data-testid="sidebar-language"
          className="flex size-7 shrink-0 items-center justify-center gap-0.5 rounded-md text-foreground/70 transition-colors hover:bg-accent/60 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          <Languages className="size-4" />
          <span className="text-[9px] font-medium uppercase leading-none">{current}</span>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" side="top" className="w-28">
        {(['zh', 'en'] as const).map((lng) => (
          <DropdownMenuItem key={lng} className="text-xs" onSelect={() => language.onChange(lng)}>
            {lng === 'zh' ? '简体中文' : 'English'}
            {current === lng && <Check className="ml-auto size-3.5" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/**
 * 非服务器运维工作区的展开态：列出该工作区的全部入口（**扁平，不渲染分组小标题**）。
 * 资源区是侧栏唯一可滚动段，工作区入口多的（平台 14 条）也不会把底栏顶出屏幕。
 */
function WorkspaceGroupSections({ workspace, renderLink, pathname }: { workspace: WorkspaceDef } & SectionsInjected) {
  return (
    <SideNavResource>
      <div data-slot="console-sidebar-groups" className="min-h-0 flex-1 overflow-y-auto scrollbar-none px-[9px] pb-[14px] pt-[6px]">
        {workspace.groups.map((group) => {
          const entries = navigableEntries(group)
          if (entries.length === 0) return null
          return (
            <div key={group.key} data-slot="console-sidebar-group" className="pt-[2px]">
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
 * 一行工作区导航（FR-502 阶段 6）：统一 `SideNavRow` 的调用口径。
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

