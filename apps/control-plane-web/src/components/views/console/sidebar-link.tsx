import type { ReactNode } from 'react'

/**
 * 外壳注入的侧栏链接渲染入参（ADR-097：包内不依赖 react-router）。
 *
 * 侧栏各视图（工作区侧栏 / 控制台侧栏）都用它把「跳转 + 可访问性语义」交给外壳，
 * 由外壳接 react-router 的 `<Link>`。`dataActive` 供折叠图标轨这类自带激活标记的样式钩子。
 */
export interface SidebarLinkArgs {
  to: string
  /** 精确匹配标记（根路径 `/` 等用 end）。 */
  end?: boolean
  /**
   * 类名：可直接给字符串；需要激活态样式时给回调（外壳的 `<NavLink>` 两种都支持）。
   * 激活态只有路由知道，所以样式规则留在视图、由外壳把 isActive 传进来。
   */
  className?: string | ((isActive: boolean) => string)
  /** 激活态语义（落在 `<a>` 上才生效）。 */
  ariaCurrent?: 'page'
  /** 折叠态只剩图标时的无障碍名。 */
  ariaLabel?: string
  title?: string
  /** 激活标记（`data-active`，供样式钩子）。 */
  dataActive?: string
  /** 点击回调（如移动端面板项点击后收起面板）。 */
  onClick?: () => void
  children: ReactNode
}

/** 链接渲染函数。 */
export type SidebarLinkRenderer = (args: SidebarLinkArgs) => ReactNode
