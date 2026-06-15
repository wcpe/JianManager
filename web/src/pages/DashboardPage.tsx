import { NavLink, Routes, Route } from 'react-router'
import { Suspense, lazy } from 'react'
import { useAuthStore } from '@/stores/auth'

const OverviewPage = lazy(() => import('./OverviewPage'))
const NodesPage = lazy(() => import('./NodesPage'))
const InstancesPage = lazy(() => import('./InstancesPage'))

interface NavItem {
  to?: string
  label?: string
  divider?: boolean
}

const navItems: NavItem[] = [
  { to: '/', label: '仪表盘' },
  { to: '/nodes', label: '节点' },
  { to: '/instances', label: '实例' },
  { to: '/bots', label: 'Bot' },
  { divider: true },
  { to: '/users', label: '用户' },
  { to: '/groups', label: '用户组' },
  { divider: true },
  { to: '/schedules', label: '定时任务' },
  { to: '/backups', label: '备份' },
  { to: '/audit', label: '审计日志' },
]

export default function DashboardPage() {
  const logout = useAuthStore((s) => s.logout)

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
                {item.label}
              </NavLink>
            )
          })}
        </nav>
        <div className="p-2 border-t">
          <button
            onClick={logout}
            className="w-full px-3 py-2 text-sm text-left text-muted-foreground hover:bg-accent/50 rounded-md"
          >
            退出登录
          </button>
        </div>
      </aside>
      <main className="flex-1 p-6 overflow-auto">
        <Suspense fallback={<div className="text-muted-foreground">加载中...</div>}>
          <Routes>
            <Route index element={<OverviewPage />} />
            <Route path="nodes" element={<NodesPage />} />
            <Route path="instances" element={<InstancesPage />} />
            <Route path="*" element={<div className="text-muted-foreground">页面开发中...</div>} />
          </Routes>
        </Suspense>
      </main>
    </div>
  )
}
