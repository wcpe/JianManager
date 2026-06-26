import { Routes, Route, Navigate } from 'react-router'
import { Suspense, lazy, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { Toaster } from 'sonner'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'

const LoginPage = lazy(() => import('./pages/LoginPage'))
const SetupPage = lazy(() => import('./pages/SetupPage'))
const DashboardPage = lazy(() => import('./pages/DashboardPage'))

/** 认证守卫：未登录时重定向到 /login。 */
function AuthGuard({ children }: { children: React.ReactNode }) {
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated)
  if (!isAuthenticated) {
    return <Navigate to="/login" replace />
  }
  return <>{children}</>
}

function App() {
  const loadFromStorage = useAuthStore((s) => s.loadFromStorage)
  const loadTheme = useThemeStore((s) => s.loadFromStorage)
  const { t } = useTranslation()

  useEffect(() => {
    loadFromStorage()
    // 主题 DOM 已由入口 initThemeFromStorage 套好；此处回填 store 状态 + 注册系统主题监听
    // （覆盖登录页：监听不依赖 console shell 挂载）。
    loadTheme()
  }, [loadFromStorage, loadTheme])

  return (
    <>
      <Toaster position="top-right" richColors closeButton />
      <Suspense fallback={<div className="flex items-center justify-center h-screen">{t('common.loading')}</div>}>
        <Routes>
          <Route path="/setup" element={<SetupPage />} />
          <Route path="/login" element={<LoginPage />} />
          <Route
            path="/*"
            element={
              <AuthGuard>
                <DashboardPage />
              </AuthGuard>
            }
          />
        </Routes>
      </Suspense>
    </>
  )
}

export default App
