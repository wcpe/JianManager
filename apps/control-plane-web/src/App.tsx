import { Routes, Route, Navigate } from 'react-router'
import { Suspense, lazy, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { Toaster } from 'sonner'
import { PageSkeleton } from '@jianmanager/ui/components/layout'
import { TooltipProvider } from '@jianmanager/ui/components/tooltip'
import { useAuthStore } from '@/stores/auth'
import { useThemeStore } from '@/stores/theme'
import { useLinkIntentPrefetch, useSidebarShortcutWarmup } from '@/lib/shared/route-prefetch'

const LoginPage = lazy(() => import('./pages/LoginPage'))
const SetupPage = lazy(() => import('./pages/SetupPage'))
const DashboardPage = lazy(() => import('./pages/DashboardPage'))
const InvitePage = lazy(() => import('./pages/InvitePage'))

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

  // 导航意图预取（阶段 6 补丁）：挂在应用根上一次性覆盖所有页面里的链接与侧栏。
  // 放在这里而不是各处导航组件：控制台里还有大量不在本次改动范围内的导航文件（如
  // WorkspaceSidebar）与各页面自带的 `<Link>`，代理监听能一并覆盖，且预取本身幂等。
  useLinkIntentPrefetch()
  useSidebarShortcutWarmup()

  useEffect(() => {
    loadFromStorage()
    // 主题 DOM 已由入口 initThemeFromStorage 套好；此处回填 store 状态 + 注册系统主题监听
    // （覆盖登录页：监听不依赖 console shell 挂载）。
    loadTheme()
  }, [loadFromStorage, loadTheme])

  return (
    <>
      <Toaster position="top-right" richColors closeButton />
      {/*
        Tooltip 的全局 Provider（组件库 tooltip.tsx 的约定：Radix 原语的上下文缺失会直接抛错，
        故每个 Tooltip 都必须有一个 Provider 祖先）。

        放在应用根而不是各页面：图标按钮散落在控制台各处（含 Radix Dialog/Portal 内的组件），
        每处各套一个只会让「全局 delayDuration / skipDelayDuration」变成 N 份不一致的配置。
        这里统一 delayDuration=300：组件库缺省 0 会让滑过一排图标按钮时提示连闪，300ms 是
        「停留才提示」的常用阈值，且小于 Radix 缺省的 700ms（本项目按钮大多带 aria-label，
        提示是辅助信息而非唯一信息源，不必等那么久）。
      */}
      <TooltipProvider delayDuration={300}>
        {/* 首屏 fallback（登录 / 初始化 / 控制台外壳的 chunk）：外壳此时本身还没挂载，
            套页面壳只会二次跳动，故用轻量版骨架（居中占位）。控制台内的切页骨架由 Workspace 承担。 */}
        <Suspense fallback={<PageSkeleton variant="minimal" aria-label={t('common.loading')} />}>
          <Routes>
            <Route path="/setup" element={<SetupPage />} />
            <Route path="/login" element={<LoginPage />} />
            <Route path="/invite" element={<InvitePage />} />
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
      </TooltipProvider>
    </>
  )
}

export default App
