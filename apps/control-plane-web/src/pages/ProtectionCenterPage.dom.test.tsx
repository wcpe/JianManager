import { describe, expect, it } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { domainRoute } from '@jianmanager/devmock/inject'
import ProtectionCenterPage from './ProtectionCenterPage'

describe('ProtectionCenterPage', () => {
  it('展示客户端分发安全标题并渲染全量日志详情', async () => {
    loginMockUser('admin')
    server.use(
      domainRoute('get', '/client-dist/security/overview', () => HttpResponse.json({
        activeDownloads: 0,
        downloadBytesPerSecond: 0,
        abnormalRequests: 0,
        unauthorizedRequests: 0,
        forbiddenRequests: 0,
        rateLimitedRequests: 0,
        blockedIpCount: 0,
        throttledKeyCount: 0,
        protectedChannelCount: 0,
        topIps: [],
        topKeys: [],
        topChannels: [],
        topPlayers: [],
      })),
      domainRoute('get', '/client-dist/security/logs', () => HttpResponse.json({
        page: 1,
        pageSize: 100,
        total: 2,
        items: [
          {
            id: 'hello:1',
            type: 'hello',
            title: '安全画像上报',
            channelId: 'skyblock-s1',
            machineId: 'machine-1',
            playerName: 'Alex',
            ip: '127.0.0.1',
            status: 'accepted',
            createdAt: '2026-07-02T12:00:00Z',
            detail: { installId: 'install-1', keyPrefix: 'jm_live' },
          },
          {
            id: 'telemetry:2',
            type: 'telemetry',
            title: '更新遥测',
            channelId: 'skyblock-s1',
            machineId: 'machine-1',
            playerName: 'Alex',
            ip: '127.0.0.1',
            status: 'success',
            createdAt: '2026-07-02T12:01:00Z',
            detail: { fromVersion: 1, toVersion: 2 },
          },
        ],
      })),
    )

    const { container } = renderWithProviders(<ProtectionCenterPage />)

    expect(container.firstElementChild).toHaveAttribute('data-page', 'client-dist-security')
    expect(container.firstElementChild).toHaveClass('jm-page-stack')
    expect(screen.getByRole('heading', { name: '客户端分发安全' })).toBeInTheDocument()
    expect(screen.getByRole('tablist')).toHaveClass('jm-toolbar-surface')
    expect(screen.queryByRole('tab', { name: '遥测告知' })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('tab', { name: '日志详情' }))

    const table = await screen.findByRole('table')
    expect(within(table).getByText('Security Hello')).toBeInTheDocument()
    expect(within(table).getAllByText('更新遥测').length).toBeGreaterThan(0)
    expect(within(table).getAllByText(/Alex/).length).toBeGreaterThan(0)
    expect(within(table).getByText(/install-1/)).toBeInTheDocument()
  })
})
