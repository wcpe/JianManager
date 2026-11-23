import { beforeEach, describe, expect, it } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { useConsoleStore } from '@/stores/console'
import InstancesPage from './InstancesPage'

beforeEach(() => {
  loginMockUser()
  useConsoleStore.setState({ selectedNodeId: null })
  server.use(
    http.get(API('/nodes'), () => HttpResponse.json([{ id: 1, name: 'node-a', host: '192.0.2.102' }])),
    http.get(API('/networks'), () => HttpResponse.json([])),
  )
})

describe('服务器卡片默认视图', () => {
  it('未指定视图时展示卡片和节点 IP，而显式列表深链仍可用', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?groupBy=none' })

    expect(await screen.findByTestId('instances-card-virtual')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '卡片视图' })).toHaveAttribute('aria-pressed', 'true')
    const card = await screen.findByTestId('instances-card-virtual')
    expect(await within(card).findAllByText(/192\.0\.2\.102/)).not.toHaveLength(0)
    expect(screen.getByRole('region', { name: '服务器运行概览' })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '列表视图' }))
    expect(new URLSearchParams(window.location.search).get('view')).toBe('list')
    expect(screen.getByRole('button', { name: '列表视图' })).toHaveAttribute('aria-pressed', 'true')

    await user.click(screen.getByRole('button', { name: '卡片视图' }))
    expect(new URLSearchParams(window.location.search).get('view')).toBeNull()
    expect(screen.getByTestId('instances-card-virtual')).toBeInTheDocument()
  })
})
