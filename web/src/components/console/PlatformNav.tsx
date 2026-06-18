import { useTranslation } from 'react-i18next'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { changeLanguage } from '@/i18n'
import SidebarNavLink, { type NavEntry } from './SidebarNavLink'

/**
 * 左栏下段：系统平台导航（平台管理）。
 * 用户 / 用户组 / 审计（ADR-009）。
 * 注：「设置」暂无对应页面/路由，待其落地后补入，避免死链。
 */
const platformNav: NavEntry[] = [
  { to: '/users', labelKey: 'nav.users' },
  { to: '/groups', labelKey: 'nav.groups' },
  { to: '/audit', labelKey: 'nav.audit' },
]

export default function PlatformNav() {
  const { t, i18n } = useTranslation()
  const logout = useAuthStore((s) => s.logout)
  const { theme, setTheme } = useThemeStore()

  const currentLang = i18n.language as 'zh' | 'en'

  const cycleTheme = () => {
    const order: Array<'light' | 'dark' | 'system'> = ['light', 'dark', 'system']
    const idx = order.indexOf(theme)
    setTheme(order[(idx + 1) % order.length])
  }

  const themeIcon = theme === 'dark' ? '🌙' : theme === 'light' ? '☀️' : '💻'

  return (
    <div className="border-t">
      <nav className="space-y-0.5 p-2">
        {platformNav.map((item) => (
          <SidebarNavLink key={item.to} {...item} />
        ))}
      </nav>
      <div className="space-y-1 border-t p-2">
        <div className="flex items-center gap-1 px-3 py-1">
          <button
            onClick={cycleTheme}
            className="flex-1 rounded px-2 py-1 text-left text-xs text-muted-foreground hover:bg-accent/50"
            title={t('theme.toggle')}
          >
            {themeIcon} {t(`theme.${theme}`)}
          </button>
          <button
            onClick={() => changeLanguage(currentLang === 'zh' ? 'en' : 'zh')}
            className="rounded px-2 py-1 text-xs text-muted-foreground hover:bg-accent/50"
          >
            {currentLang === 'zh' ? 'EN' : '中'}
          </button>
        </div>
        <button
          onClick={logout}
          className="w-full rounded-md px-3 py-2 text-left text-sm text-muted-foreground hover:bg-accent/50"
        >
          {t('common.logout')}
        </button>
        <p className="px-3 py-1 text-xs text-muted-foreground/70">v{__APP_VERSION__}</p>
      </div>
    </div>
  )
}
