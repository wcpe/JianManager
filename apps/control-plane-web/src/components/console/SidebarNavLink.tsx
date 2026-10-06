import { NavLink } from 'react-router'
import { useTranslation } from 'react-i18next'
import { cn } from '@jianmanager/ui'
import type { NavEntry } from '@jianmanager/ui/lib/nav-config'
import { useRouteIntentPrefetch } from '@/lib/route-prefetch'

// 导航项类型已收敛到包内 nav-config（单一来源）；此处转出以兼容既有 import 点。
export type { NavEntry }

/** 侧栏单个导航链接（FR-061 高密度 + MC 绿激活态）；`/` 用 end 精确匹配。 */
export default function SidebarNavLink({
  to,
  labelKey,
  icon: Icon,
  nested = false,
}: NavEntry & { nested?: boolean }) {
  const { t } = useTranslation()
  // FR-496 阶段 6 补丁：悬停/聚焦即预取目标页 chunk，把下载提前到点击之前（幂等，见 route-prefetch）。
  const prefetchHandlers = useRouteIntentPrefetch(to)
  const exact = to === '/' || to === '/networks'
  return (
    <NavLink
      to={to}
      end={exact}
      {...prefetchHandlers}
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
