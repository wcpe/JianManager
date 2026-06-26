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

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <QueryClientProvider client={queryClient}>
        <App />
      </QueryClientProvider>
    </BrowserRouter>
  </StrictMode>,
)
