import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Routes, Route } from 'react-router'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { useAuthStore } from '@/stores/auth'
import InstanceDetailPage from './InstanceDetailPage'

/** 
 * 实例详情深链路由强断言。
 * `/instances/:id` 直接渲染固定分区的服务器统一控制台，不再把路由参数写入 openInstance store。
 */
function renderDetail(route: string) {
  return renderWithProviders(
    <Routes>
      <Route path="/instances/:id" element={<InstanceDetailPage />} />
    </Routes>,
    { route },
  )
}

beforeEach(() => {
  loginMockUser()
})

describe('InstanceDetailPage（mock 深链）', () => {
  it('路由 /instances/1 → 直接渲染种子实例控制台', async () => {
    renderDetail('/instances/1')

    // 顶栏标题只留实例名（瘦身，「服务器控制台 /」前缀已删）。
    expect(await screen.findByRole('heading', { name: 'survival-1' })).toBeInTheDocument()
    expect(screen.getByText('运行', { selector: '[data-slot="status-badge"]' })).toBeInTheDocument()
  })

  it('对停止实例深链后启动 → 控制台状态联动为 RUNNING', async () => {
    const user = userEvent.setup()
    // 启停二次确认的角色门禁读 authStore.role；mock token 解不出 role（null 一律拒），显式给平台管理员。
    useAuthStore.setState({ role: 10 })
    renderDetail('/instances/2')

    expect(await screen.findByRole('heading', { name: 'lobby-proxy' })).toBeInTheDocument()
    expect(screen.getByText('停止', { selector: '[data-slot="status-badge"]' })).toBeInTheDocument()

    await user.click(await screen.findByRole('button', { name: /启动/ }))
    // 启停现在要过 DangerConfirm（与列表页同一安全语义）：确认后才发请求。
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /启动/ }))

    await waitFor(() => expect(screen.getByText('运行', { selector: '[data-slot="status-badge"]' })).toBeInTheDocument())
  })

  it('注入 500 → 控制台显示错误态（不是「不存在」）且页面不崩溃', async () => {
    mockInject('get', '/instances/:id', { kind: 'status', status: 500 })
    renderDetail('/instances/1')

    /*
      【错误态与空态必须分开】原先 500 与「加载中 / 不存在」共用一句文案：
      运维看到「服务器不存在」会去查实例是不是被删了，而事实只是这次没拉到。
      现在错误态有独立节点与重试入口。
    */
    expect(await screen.findByTestId('instance-load-error')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重试' })).toBeInTheDocument()
    expect(screen.queryByText('服务器不存在或正在加载')).not.toBeInTheDocument()
    expect(screen.queryByText(/服务器控制台 \/ survival-1/)).not.toBeInTheDocument()
  })
})
