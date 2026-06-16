import { NavLink, Routes, Route } from 'react-router'
import { Suspense, lazy } from 'react'
import { useTranslation } from 'react-i18next'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { changeLanguage } from '@/i18n'

const OverviewPage = lazy(() => import('./OverviewPage'))
const NodesPage = lazy(() => import('./NodesPage'))
const InstancesPage = lazy(() => import('./InstancesPage'))
const InstanceDetailPage = lazy(() => import('./InstanceDetailPage'))
const UsersPage = lazy(() => import('./UsersPage'))
const GroupsPage = lazy(() => import('./GroupsPage'))
const SchedulesPage = lazy(() => import('./SchedulesPage'))
const BackupsPage = lazy(() => import('./BackupsPage'))
const BotsPage = lazy(() => import('./BotsPage'))
const AuditPage = lazy(() => import('./AuditPage'))
const TemplatesPage = lazy(() => import('./TemplatesPage'))
const AlertsPage = lazy(() => import('./AlertsPage'))

interface NavItem {
  to?: string
  labelKey?: string
  divider?: boolean
}

const navItems: NavItem[] = [
  { to: '/', labelKey: 'nav.dashboard' },
  { to: '/nodes', labelKey: 'nav.nodes' },
  { to: '/instances', labelKey: 'nav.instances' },
  { to: '/bots', labelKey: 'nav.bots' },
  { to: '/alerts', labelKey: 'nav.alerts' },
  { divider: true },
  { to: '/users', labelKey: 'nav.users' },
  { to: '/groups', labelKey: 'nav.groups' },
  { to: '/templates', labelKey: 'nav.templates' },
  { divider: true },
  { to: '/schedules', labelKey: 'nav.schedules' },
  { to: '/backups', labelKey: 'nav.backups' },
  { to: '/audit', labelKey: 'nav.audit' },
]

export default function DashboardPage() {
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
    <div className="flex h-screen">
      <aside className="w-60 border-r flex flex-col">
        <div className="p-4 border-b">
          <h2 className="font-bold text-lg">JianManager</h2>
        </div>
        <nav className="flex-1 p-2 space-y-0.5 text-sm overflow-auto">
          {navItems.map((item, i) => {
            if (item.divider) {
              return <hr key={i} className="my-2" />
            }
            return (
              <NavLink
                key={item.to}
                to={item.to!}
                end={item.to === '/'}
                className={({ isActive }) =>
                  `block px-3 py-2 rounded-md ${isActive ? 'bg-accent font-medium' : 'hover:bg-accent/50'}`
                }
              >
                {t(item.labelKey!)}
              </NavLink>
            )
          })}
        </nav>
        <div className="p-2 border-t space-y-1">
          <div className="flex items-center gap-1 px-3 py-1">
            <button
              onClick={cycleTheme}
              className="flex-1 px-2 py-1 text-xs text-left text-muted-foreground hover:bg-accent/50 rounded"
              title={t('theme.toggle')}
            >
              {themeIcon} {t(`theme.${theme}`)}
            </button>
            <button
              onClick={() => changeLanguage(currentLang === 'zh' ? 'en' : 'zh')}
              className="px-2 py-1 text-xs text-muted-foreground hover:bg-accent/50 rounded"
            >
              {currentLang === 'zh' ? 'EN' : '中'}
            </button>
          </div>
          <button
            onClick={logout}
            className="w-full px-3 py-2 text-sm text-left text-muted-foreground hover:bg-accent/50 rounded-md"
          >
            {t('common.logout')}
          </button>
        </div>
      </aside>
      <main className="flex-1 p-6 overflow-auto">
        <Suspense fallback={<div className="text-muted-foreground">{t('common.loading')}</div>}>
          <Routes>
            <Route index element={<OverviewPage />} />
            <Route path="nodes" element={<NodesPage />} />
            <Route path="instances" element={<InstancesPage />} />
            <Route path="instances/:id" element={<InstanceDetailPage />} />
            <Route path="bots" element={<BotsPage />} />
            <Route path="alerts" element={<AlertsPage />} />
            <Route path="users" element={<UsersPage />} />
            <Route path="groups" element={<GroupsPage />} />
            <Route path="templates" element={<TemplatesPage />} />
            <Route path="schedules" element={<SchedulesPage />} />
            <Route path="backups" element={<BackupsPage />} />
            <Route path="audit" element={<AuditPage />} />
            <Route path="*" element={<div className="text-muted-foreground">{t('common.loading')}</div>} />
          </Routes>
        </Suspense>
      </main>
    </div>
  )
}
