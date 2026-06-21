import { NavLink } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { LucideIcon } from 'lucide-react'
import { useConsoleStore } from '@/stores/console'
import { cn } from '@/lib/utils'

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
  const closeInstance = useConsoleStore((s) => s.closeInstance)
  return (
    <NavLink
      to={to}
      end={to === '/'}
      // 点击导航即关闭实例工作区：同路由（如控制台从实例页打开后再点「实例」）路由不变，
      // 仅靠 Workspace 的 pathname 监听不会关闭，这里显式关闭兜底。
      onClick={() => closeInstance()}
      className={({ isActive }) =>
        cn(
          'flex items-center gap-2 rounded-md px-2.5 py-1.5 text-[13px] transition-colors',
          nested ? 'pl-8 text-xs' : '',
          isActive
            ? 'bg-primary/15 font-medium text-primary'
            : 'text-foreground/80 hover:bg-accent/60 hover:text-foreground',
        )
      }
    >
      {Icon && <Icon className="size-4 shrink-0" />}
      <span className="truncate">{t(labelKey)}</span>
    </NavLink>
  )
}
