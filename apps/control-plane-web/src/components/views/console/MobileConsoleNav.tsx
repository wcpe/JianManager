import { Fragment, useEffect, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown } from 'lucide-react'

import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import { Card } from '@jianmanager/ui/components/card'
import type { SidebarLinkArgs } from '@/components/views/console/sidebar-link'
import type { NavGroup, NavSection, WorkspaceDef } from '@/lib/console/workspace-navigation'

/** 收集一个分组下所有可导航路由，用于移动端主域高亮。 */
function groupRoutes(group: NavGroup): string[] {
  if (group.to) return [group.to]
  const fromChildren = group.children?.map((c) => c.to) ?? []
  const fromSections = group.sections?.flatMap((s) => s.children.map((c) => c.to)) ?? []
  return [...fromChildren, ...fromSections]
}

/** 一个工作区下所有可导航路由（含其全部分组），用于底部格子高亮。 */
function workspaceRoutes(workspace: WorkspaceDef): string[] {
  return workspace.groups.flatMap(groupRoutes)
}

/** 面板内链接的统一样式（组链接与子项共用一份，避免两处漂移）。 */
function panelLinkClass(isActive: boolean): string {
  return cn(
    'flex items-center gap-2 rounded-md px-3 py-2 text-sm transition-[background-color,color,transform] duration-150 ease-ios active:scale-[0.98]',
    isActive
      ? 'bg-accent font-semibold text-primary shadow-[inset_3px_0_0_var(--primary)]'
      : 'text-foreground/85 hover:bg-accent/60 hover:text-foreground',
  )
}

function isRouteActive(pathname: string, to: string): boolean {
  if (to === '/') return pathname === '/'
  if (to === '/networks') return pathname === '/networks'
  return pathname === to || pathname.startsWith(`${to}/`)
}

export interface MobileConsoleNavProps {
  /** 权限裁剪后的工作区列表（应用侧计算）。底部格子一格一个工作区。 */
  workspaces: WorkspaceDef[]
  /** 当前路径（主域高亮判定）。 */
  pathname: string
  /** 链接渲染（应用侧接路由；`className` 支持激活态回调）。 */
  renderLink: (args: SidebarLinkArgs) => ReactNode
}

/**
 * 手机端底部导航：侧栏在小屏隐藏时的等价入口。数据与路由由 props 注入（ADR-103）。
 *
 * 【为何以「工作区」为底部格子（FR-507 阶段 6）】
 * 旧实现把六域顶层分组直接铺成 5 格，于是只有前 5 个域可见——观测、客户端分发、
 * 平台设置在手机上**永远点不到**，是个静默的功能缺失。四工作区结构下如果照搮平铺，
 * 会变成 13 个分组只显示 5 个，缺失更严重。
 * 以工作区为格子后：格子数固定为 4（≤5，不会截断），而工作区内部的分组与侧栏同源，
 * 于是「四工作区导航在三处一致可达」（验收①）在手机上真正成立。
 */
export function MobileConsoleNav({ workspaces, pathname, renderLink }: MobileConsoleNavProps) {
  const { t } = useTranslation()
  const [openKey, setOpenKey] = useState<string | null>(null)
  const [panelWorkspace, setPanelWorkspace] = useState<WorkspaceDef | null>(null)
  const [panelState, setPanelState] = useState<'open' | 'closed'>('closed')
  const closeTimer = useRef<number | null>(null)

  function clearCloseTimer() {
    if (closeTimer.current === null) return
    window.clearTimeout(closeTimer.current)
    closeTimer.current = null
  }

  function closePanel() {
    clearCloseTimer()
    const closingKey = panelWorkspace?.key
    setOpenKey(null)
    setPanelState('closed')
    if (!closingKey) return

    closeTimer.current = window.setTimeout(() => {
      setPanelWorkspace((current) => (current?.key === closingKey ? null : current))
      closeTimer.current = null
    }, 180)
  }

  function togglePanel(workspace: WorkspaceDef) {
    if (openKey === workspace.key) {
      closePanel()
      return
    }

    clearCloseTimer()
    setPanelWorkspace(workspace)
    setOpenKey(workspace.key)
    setPanelState('open')
  }

  useEffect(
    () => () => {
      if (closeTimer.current !== null) window.clearTimeout(closeTimer.current)
    },
    [],
  )

  return (
    <nav
      data-slot="mobile-console-nav"
      aria-label="移动导航"
      className="fixed inset-x-0 bottom-0 z-40 border-t bg-card/96 px-2 pb-[max(env(safe-area-inset-bottom),0.5rem)] pt-2 shadow-[0_-16px_32px_-28px_rgb(var(--shadow-color)/0.45)] backdrop-blur-xl sm:hidden"
    >
      {panelWorkspace && (
        // 卡片视觉交给 Card；data-slot / data-state 是测试与 CSS 动画契约，必须透传。
        <Card
          data-slot="mobile-nav-panel"
          data-state={panelState}
          className="jm-mobile-nav-panel absolute inset-x-2 bottom-[calc(100%+0.5rem)] max-h-[60vh] overflow-y-auto p-2 shadow-lift"
        >
          <div className="mb-1 flex items-center justify-between px-2 py-1 text-xs font-medium text-muted-foreground">
            <span>{t(panelWorkspace.labelKey)}</span>
            <Button
              type="button"
              variant="invisible"
              size="auto"
              onClick={closePanel}
              className="rounded p-1 text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
              aria-label="收起移动导航"
            >
              <ChevronDown className="size-4" />
            </Button>
          </div>
          {/* 工作区内的分组：与侧栏同一份数据，组标题就当作面板里的小节标题。 */}
          <div className="grid gap-1">
            {panelWorkspace.groups.map((group) => (
              <MobileGroup key={group.key} group={group} onNavigate={closePanel} renderLink={renderLink} />
            ))}
          </div>
        </Card>
      )}

      <div className="grid gap-1" style={{ gridTemplateColumns: `repeat(${Math.min(workspaces.length, 5)}, minmax(0, 1fr))` }}>
        {workspaces.slice(0, 5).map((workspace) => {
          const Icon = workspace.icon
          const active = workspaceRoutes(workspace).some((to) => isRouteActive(pathname, to))

          return (
            /* 底部格子是「图标 + 文字」的整格可点块，撑满网格列，故保原布局不套按钮盒 */
            <Button
              key={workspace.key}
              type="button"
              variant="invisible"
              size="auto"
              aria-expanded={openKey === workspace.key}
              onClick={() => togglePanel(workspace)}
              className={cn(
                'relative flex min-w-0 flex-col items-center gap-1 rounded-md px-1.5 py-1.5 text-[11px] font-normal transition-[background-color,color,transform] duration-150 ease-ios active:scale-[0.97]',
                active || openKey === workspace.key
                  ? 'bg-accent text-primary'
                  : 'text-muted-foreground hover:bg-accent/50 hover:text-foreground',
              )}
            >
              <Icon className="size-4" />
              <span className="w-full truncate text-center">{t(workspace.labelKey)}</span>
            </Button>
          )
        })}
      </div>
    </nav>
  )
}

/**
 * 面板里的一个分组：组标题 + 其下条目。
 *
 * 处理三种形态（与侧栏同构，数据同源）：
 * - `to`：分组自身就是一个链接（如「资源」下的首页）；
 * - `children`：普通子项；
 * - `sections`：带二级小标题（如「身份与访问」）。
 */
function MobileGroup({
  group,
  onNavigate,
  renderLink,
}: {
  group: NavGroup
  onNavigate: () => void
  renderLink: (args: SidebarLinkArgs) => ReactNode
}) {
  const { t } = useTranslation()
  // 单子项的分组不渲染组标题：等于重复一遍链接文字，纯噪声。
  const childCount = (group.children?.length ?? 0) + (group.sections?.length ?? 0)
  const showHeading = group.to === undefined && childCount > 1
  return (
    <div className="space-y-1">
      {showHeading && (
        <div className="px-3 pt-2 text-[11px] font-medium text-muted-foreground/70">{t(group.labelKey)}</div>
      )}
      {group.to !== undefined &&
        renderLink({
          to: group.to,
          end: group.to === '/' || group.to === '/networks',
          onClick: onNavigate,
          className: panelLinkClass,
          children: (
            <>
              <group.icon className="size-4 shrink-0" />
              <span>{t(group.labelKey)}</span>
            </>
          ),
        })}
      {group.children?.map((item) => (
        <Fragment key={item.to}>
          {renderLink({
            to: item.to,
            end: item.to === '/' || item.to === '/networks',
            onClick: onNavigate,
            className: panelLinkClass,
            children: (
              <>
                {item.icon && <item.icon className="size-4 shrink-0" />}
                <span>{t(item.labelKey)}</span>
              </>
            ),
          })}
        </Fragment>
      ))}
      {group.sections?.map((section) => (
        <MobileSection key={section.labelKey} section={section} onNavigate={onNavigate} renderLink={renderLink} />
      ))}
    </div>
  )
}

function MobileSection({
  section,
  onNavigate,
  renderLink,
}: {
  section: NavSection
  onNavigate: () => void
  renderLink: (args: SidebarLinkArgs) => ReactNode
}) {
  const { t } = useTranslation()
  return (
    <div className="space-y-1">
      <div className="px-3 pt-2 text-[11px] font-medium text-muted-foreground/70">{t(section.labelKey)}</div>
      {section.children.map((item) => (
        <Fragment key={item.to}>
          {renderLink({
            to: item.to,
            end: item.to === '/' || item.to === '/networks',
            onClick: onNavigate,
            className: panelLinkClass,
            children: (
              <>
                {item.icon && <item.icon className="size-4 shrink-0" />}
                <span>{t(item.labelKey)}</span>
              </>
            ),
          })}
        </Fragment>
      ))}
    </div>
  )
}
