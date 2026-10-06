import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@jianmanager/ui'
import type { NavEntry } from '@jianmanager/ui/lib/nav-config'

/** 外壳注入的路由渲染入参（ADR-097：包内不依赖 react-router）。 */
export interface NavLinkRenderArgs {
  to: string
  /** 精确匹配标记（`/` 与 `/networks` 用 end）。 */
  end: boolean
  /** 由激活态决定 class —— 样式规则归视图，激活态只有路由知道。 */
  className: (isActive: boolean) => string
  children: ReactNode
}

/** 外壳注入的路由渲染函数：接 react-router 的 NavLink 与悬停预取处理器。 */
export type NavLinkRenderer = (args: NavLinkRenderArgs) => ReactNode

export interface SidebarNavLinkProps extends NavEntry {
  nested?: boolean
  /** 路由渲染函数（应用侧接线层注入）。 */
  renderLink: NavLinkRenderer
}

/** 侧栏单个导航链接（FR-061 高密度 + MC 绿激活态）；`/` 用 end 精确匹配。 */
export function SidebarNavLink({ to, labelKey, icon: Icon, nested = false, renderLink }: SidebarNavLinkProps) {
  const { t } = useTranslation()
  const exact = to === '/' || to === '/networks'
  return (
    <>
      {renderLink({
        to,
        end: exact,
        className: (isActive) =>
          cn(
            'jm-nav-link group relative flex items-center gap-2 rounded-md px-2.5 py-1.5 text-[13px]',
            nested ? 'pl-8 text-xs' : '',
            isActive
              ? 'bg-accent font-semibold text-primary'
              : 'text-foreground/80 hover:bg-accent/55 hover:text-foreground',
          ),
        children: (
          <>
            {Icon && <Icon className="jm-nav-link-icon size-4 shrink-0" />}
            <span className="jm-nav-link-label truncate">{t(labelKey)}</span>
          </>
        ),
      })}
    </>
  )
}