import { useTranslation } from 'react-i18next'
import { cn } from '@jianmanager/ui'
import type { NavEntry } from '@/lib/nav-config'
import type { SidebarLinkArgs, SidebarLinkRenderer } from '@/components/views/console/sidebar-link'

/** 渲染入参：与侧栏其它链接共用同一份（见 {@link SidebarLinkArgs}）；此处保留旧名以免已引用它的代码改动。 */
export type NavLinkRenderArgs = SidebarLinkArgs

/** 外壳注入的路由渲染函数：接 react-router 的 NavLink 与悬停预取处理器。 */
export type NavLinkRenderer = SidebarLinkRenderer

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