import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import './i18n'
import './index.css'
import { initThemeFromStorage } from '@/lib/theme'
import App from './App'

// 首屏无闪 + 登录/初始化页也套主题（FR-164）：在 React 挂载前先把
// 主题色（data-theme）与明暗（class）套到 <html>，早于任何组件渲染。
initThemeFromStorage()

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      retry: 1,
    },
  },
})

/**
 * VITE_MOCK=1 时启 MSW Service Worker，整站打到内存假后端（FR-196，`npm run dev:mock`）。
 * 动态 import 确保 mock 代码不进生产包。否则直连真后端，行为不变。
 */
async function enableMocking() {
  if (!import.meta.env.VITE_MOCK) return
  const [{ worker }, dbModule] = await Promise.all([import('@/mocks/browser'), import('@/mocks/db')])
  dbModule.resetDb()
  // 内存假后端刷新即重置，但 localStorage 仍存 token——为其补一个会话，使 mock 模式刷新后保持登录
  // （否则刷新→会话丢→首个 API 401→跳登录，整站不可持续点）。仅 VITE_MOCK 下运行，不入生产。
  const token = localStorage.getItem('accessToken')
  if (token) {
    const { db } = dbModule
    type Sess = { id: number; accessToken: string; refreshToken: string; userId: number }
    db<Sess>('sessions').insert({
      accessToken: token,
      refreshToken: localStorage.getItem('refreshToken') ?? `mock-refresh-${token}`,
      userId: 1,
    })
  }
  await worker.start({ onUnhandledRequest: 'bypass' })
}

void enableMocking().then(() => {
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <BrowserRouter>
        <QueryClientProvider client={queryClient}>
          <App />
        </QueryClientProvider>
      </BrowserRouter>
    </StrictMode>,
  )
})
