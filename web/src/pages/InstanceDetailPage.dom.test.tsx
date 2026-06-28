import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Routes, Route } from 'react-router'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { useConsoleStore } from '@/stores/console'
import { useInstance, useStartInstance } from '@/api/instances'
import InstanceDetailPage from './InstanceDetailPage'

/**
 * 实例详情深链路由强断言（FR-201 / FR-166）。
 * InstanceDetailPage 本身只把 `:id` 打开进 console store（画布由 Workspace 接管，此处不挂）。
 * 用 Probe 读 store 打开的 id 再经 useInstance 拉详情，验证「路由参数→打开→详情可取（种子）」整链，
 * 并验状态联动与错误注入。需 :id 路由匹配，故包一层 Routes。
 */
function Probe() {
  const openInstanceId = useConsoleStore((s) => s.openInstanceId)
  const { data, isError } = useInstance(openInstanceId ?? 0)
  const start = useStartInstance()
  return (
    <div>
      <span data-testid="opened">{openInstanceId ?? 'none'}</span>
      {isError && <span data-testid="detail-error">详情加载失败</span>}
      {data && (
        <>
          <span data-testid="detail-name">{data.name}</span>
          <span data-testid="detail-status">{data.status}</span>
          <button onClick={() => start.mutate(data.id)}>启动</button>
        </>
      )}
    </div>
  )
}

function renderDetail(route: string) {
  return renderWithProviders(
    <>
      <Routes>
        <Route path="/instances/:id" element={<InstanceDetailPage />} />
      </Routes>
      <Probe />
    </>,
    { route },
  )
}

beforeEach(() => {
  loginMockUser()
})

describe('InstanceDetailPage（mock 深链）', () => {
  it('路由 /instances/1 → 打开种子实例并取回其详情', async () => {
    renderDetail('/instances/1')
    // 路由参数经 openInstance 写入 store。
    await waitFor(() => expect(screen.getByTestId('opened')).toHaveTextContent('1'))
    // 该 id 对应种子实例详情可取（GET /instances/1 命中假后端）。
    expect(await screen.findByTestId('detail-name')).toHaveTextContent('survival-1')
    expect(screen.getByTestId('detail-status')).toHaveTextContent('RUNNING')
  })

  it('对停止实例深链后启动 → 详情状态联动为 RUNNING', async () => {
    const user = userEvent.setup()
    renderDetail('/instances/2') // lobby-proxy 种子 STOPPED
    expect(await screen.findByTestId('detail-name')).toHaveTextContent('lobby-proxy')
    expect(screen.getByTestId('detail-status')).toHaveTextContent('STOPPED')

    await user.click(await screen.findByRole('button', { name: '启动' }))

    await waitFor(() => expect(screen.getByTestId('detail-status')).toHaveTextContent('RUNNING'))
  })

  it('注入 500 → 详情查询报错（错误态非崩溃）', async () => {
    mockInject('get', '/instances/:id', { kind: 'status', status: 500 })
    renderDetail('/instances/1')
    // 路由仍打开 id=1，但详情查询失败 → 错误态展示，页面不崩。
    await waitFor(() => expect(screen.getByTestId('opened')).toHaveTextContent('1'))
    expect(await screen.findByTestId('detail-error')).toBeInTheDocument()
    expect(screen.queryByTestId('detail-name')).not.toBeInTheDocument()
  })
})
