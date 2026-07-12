import { NavLink } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { LucideIcon } from 'lucide-react'
import { cn } from '@jianmanager/ui'

/** 侧栏导航项（多级侧栏共用）。 */
export interface NavEntry {
  to: string
  labelKey: string
  icon?: LucideIcon
}

/** 侧栏单个导航链接（FR-061 高密度 + MC 绿激活态）；`/` 用 end 精确匹配。 */
export default function SidebarNavLink({
  to,
  labelKey,
  icon: Icon,
  nested = false,
}: NavEntry & { nested?: boolean }) {
  const { t } = useTranslation()
  const exact = to === '/' || to === '/networks'
  return (
    <NavLink
      to={to}
      end={exact}
      className={({ isActive }) =>
        cn(
          'jm-nav-link group relative flex items-center gap-2 rounded-md px-2.5 py-1.5 text-[13px]',
          nested ? 'pl-8 text-xs' : '',
          isActive
            ? 'bg-accent font-semibold text-primary'
            : 'text-foreground/80 hover:bg-accent/55 hover:text-foreground',
        )
      }
    >
      {Icon && <Icon className="jm-nav-link-icon size-4 shrink-0" />}
      <span className="jm-nav-link-label truncate">{t(labelKey)}</span>
    </NavLink>
  )
}
