import { QueryClient } from '@tanstack/react-query'

// 全站共享同一个查询客户端，身份切换时由 auth store 清空跨账号缓存。
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      retry: 1,
    },
  },
})
