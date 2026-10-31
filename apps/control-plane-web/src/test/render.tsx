import type { ReactElement } from 'react'
import { render, type RenderOptions } from '@testing-library/react'
import { BrowserRouter } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { TooltipProvider } from '@jianmanager/ui/components/tooltip'
import '@/i18n'

/** 
 * 渲染组件并套上与 main.tsx 一致的 Provider（Router / TanStack Query / Tooltip / i18n）。
 * route 设初始路径；queries/mutations 关重试，让错误即时可断言。
 *
 * 【TooltipProvider 必须在这里】组件库的 Tooltip 缺 Provider 会直接抛
 * 「`Tooltip` must be used within `TooltipProvider`」，而用例普遍只渲染单个组件
 * （不经过 App 根）。这层包裹与 main.tsx 的 <App/> 内那层等价，保持测试与真实结构一致。
 */
export function renderWithProviders(
  ui: ReactElement,
  { route = '/', ...options }: { route?: string } & RenderOptions = {},
) {
  window.history.pushState({}, '', route)
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <BrowserRouter>
      <QueryClientProvider client={queryClient}>
        <TooltipProvider delayDuration={300}>{ui}</TooltipProvider>
      </QueryClientProvider>
    </BrowserRouter>,
    options,
  )
}
