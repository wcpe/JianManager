// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做初始化状态查询与创建请求接线。
import { Navigate } from 'react-router'
import { useSetupStatus, useSetup } from '@/api/setup'
import { SetupPageView } from '@/components/views/auth/SetupPageView'

/**
 * 首次使用引导（FR-157）容器：初始化状态查询、「无需初始化」重定向与创建 mutation
 * （token 落地 + 成功后跳首页由 `useSetup` 挂）留在此处，表单与密码强度展示交共享视图。
 * 保留同路径默认导出，路由表无需改动。
 */
export default function SetupPage() {
  const { data: status, isLoading } = useSetupStatus()
  const setup = useSetup()

  if (!isLoading && status && !status.setupRequired) {
    return <Navigate to="/login" replace />
  }

  return (
    <SetupPageView
      loading={isLoading}
      submitting={setup.isPending}
      onSubmit={async (values) => {
        await setup.mutateAsync(values)
      }}
    />
  )
}
