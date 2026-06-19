import { NavLink } from 'react-router'
import { useTranslation } from 'react-i18next'
import { useConsoleStore } from '@/stores/console'

/** 侧栏导航项（功能导航 / 系统平台导航共用）。 */
export interface NavEntry {
  to: string
  labelKey: string
}

/** 侧栏单个导航链接，激活态高亮；`/` 用 end 精确匹配。 */
export default function SidebarNavLink({ to, labelKey }: NavEntry) {
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
        `block rounded-md px-3 py-2 text-sm ${
          isActive ? 'bg-accent font-medium' : 'hover:bg-accent/50'
        }`
      }
    >
      {t(labelKey)}
    </NavLink>
  )
}
