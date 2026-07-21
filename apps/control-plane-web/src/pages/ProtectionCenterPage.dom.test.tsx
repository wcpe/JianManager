import { describe, expect, it } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { domainRoute } from '@jianmanager/devmock/inject'
import { useAuthStore } from '@/stores/auth'
import ProtectionCenterPage from './ProtectionCenterPage'

function loginPlatformAdmin(): void {
  const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))
  const token = `mock.${payload}.sig`
  loginMockUser(token)
  useAuthStore.getState().login(token, 'test-refresh-token')
}

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

  it('事件行封禁 IP 必须经 DangerConfirm 确认后才写入', async () => {
    loginPlatformAdmin()
    let writes = 0
    server.use(
      domainRoute('get', '/client-dist/security/events', () => HttpResponse.json([{
        id: 1,
        subjectType: 'client',
        subjectValue: 'install-a',
        channelId: 'skyblock-s1',
        machineId: 'machine-abcdef',
        installId: 'install-abcdef',
        playerName: 'Alex',
        ip: '192.0.2.9',
        keyId: 7,
        ruleCode: 'RATE_SPIKE',
        severity: 'high',
        scoreDelta: 3,
        action: 'observe',
        reason: '请求突增',
        endpoint: '/artifact',
        status: 429,
        createdAt: '2026-07-02T12:00:00Z',
      }])),
      domainRoute('post', '/client-dist/security/ip-blocks', () => {
        writes += 1
        return HttpResponse.json({ id: 10 }, { status: 201 })
      }),
    )
    const user = userEvent.setup()
    renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-security?tab=events' })

    await user.click(await screen.findByRole('button', { name: '封禁 IP' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/临时封禁 IP/)).toBeInTheDocument()
    expect(writes).toBe(0)
    await user.click(within(dialog).getByRole('button', { name: '确认封禁' }))
    expect(writes).toBe(1)
  })

  it('画像详情展示脱敏字段、环境信息与风险时间线', async () => {
    loginMockUser('admin')
    const profile = {
      id: 1,
      channelId: 'skyblock-s1',
      machineId: 'machine-abcdef',
      installId: 'install-abcdef',
      playerName: 'Alex',
      keyId: 7,
      keyPrefix: 'jm_live',
      firstSeen: '2026-07-01T12:00:00Z',
      lastSeen: '2026-07-02T12:00:00Z',
      lastIp: '192.0.2.9',
      userAgent: 'JM-Updater/1.0',
      coreVersion: '2.1.0',
      wedgeVersion: '1.2.0',
      manifestVersion: 9,
      os: 'Windows',
      osVersion: '11',
      arch: 'amd64',
      javaVendor: 'Temurin',
      javaVersion: '21',
      javaArch: 'amd64',
      launcher: 'Prism',
      locale: 'zh-CN',
      timezone: 'Asia/Shanghai',
      memoryTier: '8-16g',
      riskScore: 3,
      riskLevel: 'high',
      protectionState: 'observe',
      labels: [],
      createdAt: '2026-07-01T12:00:00Z',
      updatedAt: '2026-07-02T12:00:00Z',
    }
    server.use(
      domainRoute('get', '/client-dist/security/profiles', () => HttpResponse.json([profile])),
      domainRoute('get', '/client-dist/security/profiles/:id', () => HttpResponse.json({
        ...profile,
        recentEvents: [{ id: 2, ruleCode: 'RATE_SPIKE', severity: 'high', reason: '请求突增', createdAt: '2026-07-02T11:59:00Z' }],
        protectionActions: [{ id: 3, targetType: 'key', targetValue: '7', action: 'key_state', status: 'active', reason: 'observe', createdAt: '2026-07-02T11:58:00Z' }],
      })),
    )
    const user = userEvent.setup()
    renderWithProviders(<ProtectionCenterPage />, { route: '/client-dist-security?tab=profiles' })

    await user.click(await screen.findByRole('button', { name: '查看详情' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Temurin')).toBeInTheDocument()
    expect(within(dialog).getByText('Asia/Shanghai')).toBeInTheDocument()
    expect(within(dialog).getAllByText('不可信').length).toBeGreaterThan(0)
    expect(within(dialog).getByText('RATE_SPIKE')).toBeInTheDocument()
    expect(within(dialog).getByText('key_state')).toBeInTheDocument()
    // maskMachineId 默认前 6 后 4：machine-abcdef → machin…cdef
    expect(within(dialog).getByText(/machin…cdef/)).toBeInTheDocument()
  })
})
