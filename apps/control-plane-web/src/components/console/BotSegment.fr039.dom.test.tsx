import { beforeAll, describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import InstanceConsolePage from './InstanceConsolePage'
import BotSegment from './BotSegment'

beforeAll(() => {
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
  if (!Element.prototype.hasPointerCapture) Element.prototype.hasPointerCapture = () => false
  if (!Element.prototype.setPointerCapture) Element.prototype.setPointerCapture = () => {}
  if (!Element.prototype.releasePointerCapture) Element.prototype.releasePointerCapture = () => {}
  if (!('ResizeObserver' in globalThis)) {
    globalThis.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver
  }
})

describe('FR-039 控制台实例内 Bot 管理段', () => {
  it('统一服务器控制台可切到 Bot 分区并渲染实例内 Bot 段', async () => {
    loginMockUser()
    renderWithProviders(<InstanceConsolePage instanceId={1} />)

    await userEvent.click(await screen.findByRole('button', { name: 'Bot' }))

    expect(await screen.findByText('当前筛选 2 个 Bot')).toBeInTheDocument()
    expect(screen.getByText('GuardBot')).toBeInTheDocument()
    expect(screen.getByText('FollowBot')).toBeInTheDocument()
    expect(screen.getByText('共 2 个')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '+ 创建 Bot' })).toBeInTheDocument()
  })

  it('Bot 段支持勾选、批量设行为与聚合计数联动', async () => {
    loginMockUser()
    renderWithProviders(<BotSegment instanceId={1} />)

    expect(await screen.findByText('当前筛选 2 个 Bot')).toBeInTheDocument()
    expect(screen.getByText('GuardBot')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('checkbox', { name: 'GuardBot' }))
    expect(screen.getByText('已选 1 个 Bot')).toBeInTheDocument()

    const batchBar = screen.getByText('已选 1 个 Bot').closest('div') as HTMLElement
    await userEvent.click(within(batchBar).getByRole('combobox'))
    await userEvent.click(await screen.findByRole('option', { name: '巡逻' }))
    await userEvent.click(within(batchBar).getByRole('button', { name: '批量设行为' }))

    await waitFor(() => {
      const guardRow = screen.getByText('GuardBot').closest('li') as HTMLElement
      expect(within(guardRow).getByText('巡逻')).toBeInTheDocument()
    })
  })

  it('等待与停止 Bot 点击重连只提交 start 批量动作', async () => {
    loginMockUser()
    const requests: Record<string, unknown>[] = []
    const bots = [
      {
        id: 11,
        uuid: 'bot-waiting',
        instanceId: 1,
        name: 'WaitingBot',
        status: 'pending',
        config: JSON.stringify({ server: '127.0.0.1', port: 25565, auth: 'offline' }),
        behavior: 'idle',
        workerId: 'node-1',
        createdAt: '2026-06-28T00:00:00Z',
        updatedAt: '2026-06-28T00:00:00Z',
      },
      {
        id: 12,
        uuid: 'bot-stopped',
        instanceId: 1,
        name: 'StoppedBot',
        status: 'stopped',
        config: JSON.stringify({ server: '127.0.0.1', port: 25565, auth: 'offline' }),
        behavior: 'guard',
        workerId: 'node-1',
        createdAt: '2026-06-28T00:00:00Z',
        updatedAt: '2026-06-28T00:00:00Z',
      },
    ]
    server.use(
      http.get(API('/bots'), () => HttpResponse.json({ items: bots, total: bots.length, page: 1, pageSize: 50 })),
      http.post(API('/bots/batch'), async (info) => {
        const body = (await info.request.json()) as Record<string, unknown>
        requests.push(body)
        return HttpResponse.json({
          action: body.action,
          requested: 1,
          succeeded: 1,
          failed: 0,
          skipped: 0,
          errors: [],
        })
      }),
    )
    renderWithProviders(<BotSegment instanceId={1} />)

    for (const name of ['WaitingBot', 'StoppedBot']) {
      const row = (await screen.findByText(name)).closest('li') as HTMLElement
      await userEvent.click(within(row).getByRole('button', { name: '重连' }))
      await waitFor(() => expect(requests).toHaveLength(name === 'WaitingBot' ? 1 : 2))
    }

    expect(requests).toEqual([
      { action: 'start', ids: [11] },
      { action: 'start', ids: [12] },
    ])
    for (const request of requests) {
      expect(request).not.toHaveProperty('behavior')
      expect(request.action).not.toBe('set-behavior')
    }
  })
})
