// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只解析 returnTo、判定重定向并执行登录请求。
import { useState } from 'react'
import { Navigate, useLocation } from 'react-router'
import { useLogin } from '@/api/auth'
import { getSafeReturnTo } from '@/api/client'
import { useSetupStatus } from '@/api/setup'
import { useAuthStore } from '@/stores/auth'
import { LoginPageView } from '@/components/views/auth/LoginPageView'

/**
 * 登录页（FR-157）容器：表单草稿与内联错误交共享视图，这里保留 returnTo 解析、
 * 「已登录 / 待初始化」重定向与登录 mutation（token 落地与成功跳转由 `useLogin` 挂）。
 * 保留同路径默认导出，路由表无需改动。
 */
export default function LoginPage() {
  const location = useLocation()
  const [returnTo] = useState(() => getSafeReturnTo(new URLSearchParams(location.search).get('returnTo')))

  const { data: setupStatus, isLoading } = useSetupStatus()
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated)
  const login = useLogin(returnTo)

  // 已登录用户不应停留在登录页，返回安全的原页面（BUG-006、DEF-FR157-1）。
  if (isAuthenticated) {
    return <Navigate to={returnTo} replace />
  }

  if (!isLoading && setupStatus?.setupRequired) {
    return <Navigate to="/setup" replace />
  }

  return (
    <LoginPageView
      loading={isLoading}
      submitting={login.isPending}
      onSubmit={async (values) => {
        await login.mutateAsync(values)
      }}
    />
  )
}
