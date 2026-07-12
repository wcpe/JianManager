import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Routes, Route } from 'react-router'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import InstanceDetailPage from './InstanceDetailPage'

/**
 * 实例详情深链路由强断言（FR-269）。
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

    expect(await screen.findByText(/服务器控制台 \/ survival-1/)).toBeInTheDocument()
    expect(screen.getByText('运行', { selector: '[data-slot="status-badge"]' })).toBeInTheDocument()
  })

  it('对停止实例深链后启动 → 控制台状态联动为 RUNNING', async () => {
    const user = userEvent.setup()
    renderDetail('/instances/2')

    expect(await screen.findByText(/服务器控制台 \/ lobby-proxy/)).toBeInTheDocument()
    expect(screen.getByText('停止', { selector: '[data-slot="status-badge"]' })).toBeInTheDocument()

    await user.click(await screen.findByRole('button', { name: /启动/ }))

    await waitFor(() => expect(screen.getByText('运行', { selector: '[data-slot="status-badge"]' })).toBeInTheDocument())
  })

  it('注入 500 → 控制台显示空态且页面不崩溃', async () => {
    mockInject('get', '/instances/:id', { kind: 'status', status: 500 })
    renderDetail('/instances/1')

    expect(await screen.findByText('服务器不存在或正在加载')).toBeInTheDocument()
    expect(screen.queryByText(/服务器控制台 \/ survival-1/)).not.toBeInTheDocument()
  })
})
