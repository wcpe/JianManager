import { PanelLeftIcon, SearchIcon } from 'lucide-react'
import * as React from 'react'

import { cn } from '../../lib/utils'

/** 顶栏工作区项（原型 `WORKSPACES`：服务器运维 / 平台管理 / 身份与访问 …）。 */
export type TopNavWorkspace = {
  key: string
  label: string
  /** 当前所在工作区；由调用方按路由决定，顶栏不自持状态。 */
  active?: boolean
}

/**
 * 顶栏（FR-496 阶段 4）：品牌 + 四工作区切换 + 右端工具区，全局唯一一条。
 *
 * 依据《资源优先工作区》原型的两条设计意图：
 *   1. **不重复页名**——顶栏切的是「工作区」（导航层级），页名只在 `PageHeader` 出现一次；
 *   2. 工作区是**切换**不是**菜单**，所以用下划线强调的平铺按钮（原型 `.space-nav`），
 *      而不是下拉；下划线用伪元素贴在 53px 行的底边，因此按钮必须拉伸到整条高度。
 *
 * 右端顺序照原型：全局查找入口在最左（它承担 ⌘K 快捷键提示），
 * 其右依次是调用方传入的 `actions`（全部功能九宫格 / 任务托盘 / 通知 / 明暗 / 账户）。
 */
export function TopNav({
  brand,
  workspaces,
  onWorkspaceChange,
  onSearch,
  searchLabel = '查找实例、节点、功能',
  searchShortcut = '⌘ K',
  actions,
  sidebarCollapsed,
  onToggleSidebar,
  className,
  ...props
}: React.ComponentProps<'header'> & {
  /** 品牌区内容（logo + 产品名，通常是 `<a>`）。 */
  brand?: React.ReactNode
  /** 工作区列表。 */
  workspaces?: TopNavWorkspace[]
  /** 切换工作区回调（参数为 `key`）。 */
  onWorkspaceChange?: (key: string) => void
  /** 传入时显示全局查找入口。 */
  onSearch?: () => void
  /** 查找入口占位文案。 */
  searchLabel?: string
  /** 查找入口的快捷键提示。 */
  searchShortcut?: string
  /** 右端工具区（图标按钮组）。 */
  actions?: React.ReactNode
  /** 侧栏是否折叠，仅用于折叠按钮的无障碍状态。 */
  sidebarCollapsed?: boolean
  /** 传入时在品牌区右端渲染侧栏折叠按钮（原型把该按钮放在品牌列的右端）。 */
  onToggleSidebar?: () => void
}) {
  return (
    <header
      data-slot="top-nav"
      className={cn('flex min-w-0 flex-1 items-center border-b border-border bg-card', className)}
      {...props}
    >
      {brand && (
        <div
          data-slot="top-nav-brand"
          // 宽度对齐侧栏（原型 .brand{width:var(--sidebar)}，基准值 246px）：
          // 折叠侧栏不改变品牌列宽，原型同样只改 .sidebar 的宽度而不动 --sidebar。
          className="flex h-full w-[246px] min-w-0 shrink-0 items-center gap-[10px] px-4"
        >
          {brand}
          {onToggleSidebar && (
            <button
              type="button"
              data-slot="top-nav-sidebar-toggle"
              aria-label="收起 / 展开资源导航"
              aria-pressed={sidebarCollapsed}
              onClick={onToggleSidebar}
              className={cn(
                'ml-auto inline-flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground',
                'transition-colors duration-[var(--motion-duration-normal)] ease-ios hover:bg-muted hover:text-foreground',
              )}
            >
              <PanelLeftIcon className="size-[17px]" />
            </button>
          )}
        </div>
      )}

      {workspaces && workspaces.length > 0 && (
        <nav
          data-slot="top-nav-workspaces"
          aria-label="工作区"
          className="flex items-stretch gap-[18px] self-stretch px-[10px]"
        >
          {workspaces.map((workspace) => (
            <TopNavWorkspaceButton
              key={workspace.key}
              workspace={workspace}
              onClick={onWorkspaceChange}
            />
          ))}
        </nav>
      )}

      <div
        data-slot="top-nav-actions"
        className="ml-auto flex min-w-0 shrink-0 items-center gap-[5px] px-[14px]"
      >
        {onSearch && (
          <button
            type="button"
            data-slot="top-nav-search"
            aria-label="全局搜索"
            onClick={onSearch}
            className={cn(
              'mr-[6px] flex h-[31px] w-[210px] items-center gap-2 rounded-md border border-border bg-muted px-[9px]',
              'text-[11px] text-muted-foreground transition-colors duration-[var(--motion-duration-normal)] ease-ios hover:text-foreground',
            )}
          >
            <SearchIcon className="size-[13px] shrink-0" aria-hidden />
            <span className="truncate">{searchLabel}</span>
            <kbd
              data-slot="top-nav-shortcut"
              className="ml-auto shrink-0 rounded-[4px] border border-border bg-card px-[5px] font-mono text-[10px] leading-[18px] text-muted-foreground"
            >
              {searchShortcut}
            </kbd>
          </button>
        )}
        {actions}
      </div>
    </header>
  )
}

/** 单个工作区按钮。`active` 时以主色下划线标记当前工作区（原型 `.space-nav button.active:after`）。 */
function TopNavWorkspaceButton({
  workspace,
  onClick,
}: {
  workspace: TopNavWorkspace
  onClick?: (key: string) => void
}) {
  const { key, label, active } = workspace
  return (
    <button
      type="button"
      data-slot="top-nav-workspace"
      data-active={active || undefined}
      aria-current={active ? 'page' : undefined}
      onClick={() => onClick?.(key)}
      className={cn(
        'relative isolate inline-flex items-center self-stretch whitespace-nowrap px-1 text-xs text-muted-foreground',
        // 悬停底用 `before` 伪元素而非按钮自身背景：按钮必须 `self-stretch`，
        // 激活下划线才能像原型 `.space-nav button.active:after` 那样贴到 53px 行的底边；
        // 而自身背景会跟着撑满整条高度、变成一根顶天立地的色柱。伪元素上下各内缩 8px
        // 后，才是与侧栏 nav-row 同量级的「药丸」底。
        // `isolate` 把 `-z-10` 约束在本按钮自己的层叠上下文内：按 CSS 层叠顺序，
        // 负 z-index 子层位于「元素自身背景之上、行内内容之下」，所以底色既不会盖住
        // 文字，也不会溢出按钮去遮顶栏。`pointer-events-none` 让伪元素不吃点击。
        "before:pointer-events-none before:absolute before:inset-x-0 before:inset-y-2 before:-z-10 before:rounded-md before:content-[''] before:transition-colors before:duration-[var(--motion-duration-fast)] before:ease-ios",
        'transition-colors duration-[var(--motion-duration-normal)] ease-ios hover:text-foreground hover:before:bg-accent/60',
        active &&
          // 下划线用伪元素而非额外节点：它属于按钮自身状态，且必须跨整条 53px 高度贴底。
          "font-[650] text-accent-foreground after:absolute after:inset-x-[3px] after:bottom-0 after:h-[3px] after:rounded-t-[3px] after:bg-primary after:content-['']",
      )}
    >
      {label}
    </button>
  )
}
