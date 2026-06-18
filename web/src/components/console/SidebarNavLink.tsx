import { NavLink } from 'react-router'
import { useTranslation } from 'react-i18next'

/** 侧栏导航项（功能导航 / 系统平台导航共用）。 */
export interface NavEntry {
  to: string
  labelKey: string
}

/** 侧栏单个导航链接，激活态高亮；`/` 用 end 精确匹配。 */
export default function SidebarNavLink({ to, labelKey }: NavEntry) {
  const { t } = useTranslation()
  return (
    <NavLink
      to={to}
      end={to === '/'}
      className={({ isActive }) =>
        `block rounded-md px-3 py-2 text-sm ${
          isActive ? 'bg-accent font-medium' : 'hover:bg-accent/50'
        }`
      }
    >
      {t(labelKey)}
    </NavLink>
  )
}
