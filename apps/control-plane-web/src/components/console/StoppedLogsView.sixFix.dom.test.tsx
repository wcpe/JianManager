import { afterEach, describe, expect, it, vi } from 'vitest'
import { AxiosError } from 'axios'
import { useQueryClient } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import api from '@/api/client'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import StoppedLogsView from './StoppedLogsView'

afterEach(() => vi.restoreAllMocks())

/**
 * 2026-10-29：数据源已从 CP `logs` 表（`GET /logs`，已下线）改为 VL 联邦
 * （`GET /logs/federation`，见 StoppedLogsView 顶部注释）。本文件原先整组仍在 mock `/logs`，
 * 于是全部靠一个已 404 的接口「假绿/假红」；现改为 mock 真实的联邦端点。
 * 联邦成功信封需要 sourceTag + coverage，否则组件会判成「覆盖不完整」而不是「成功空」。
 */
const emptyFederation = () =>
  HttpResponse.json({ sourceTag: 'federated', items: [], coverage: { complete: true, targets: [] } })

describe('停机历史错误与空集合', () => {
  it.each([403, 500])('%d显示失败并支持重试，不谎报暂无历史日志', async (status) => {
    loginMockUser()
    let attempts = 0
    server.use(http.get(API('/logs/federation'), () => {
      attempts++
      return attempts === 1 ? HttpResponse.json({ error: 'FAIL' }, { status }) : emptyFederation()
    }))
    renderWithProviders(<StoppedLogsView instanceId={1} status="STOPPED" />)
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(status === 403 ? '无权读取历史日志' : '历史日志加载失败')
    expect(screen.queryByText('暂无历史日志')).not.toBeInTheDocument()
    await userEvent.setup().click(screen.getByRole('button', { name: '重试' }))
    expect(await screen.findByText('暂无历史日志')).toBeInTheDocument()
    expect(attempts).toBe(2)
  })

  it('超时独立呈现，既不是空成功也不是权限失败', async () => {
    loginMockUser()
    vi.spyOn(api, 'get').mockRejectedValueOnce(new AxiosError('timeout', 'ECONNABORTED'))
    renderWithProviders(<StoppedLogsView instanceId={1} status="STOPPED" />)
    expect(await screen.findByRole('alert')).toHaveTextContent('历史日志请求超时')
    expect(screen.queryByText('暂无历史日志')).not.toBeInTheDocument()
  })

  it('成功空才呈现空态', async () => {
    loginMockUser()
    server.use(http.get(API('/logs/federation'), () => emptyFederation()))
    renderWithProviders(<StoppedLogsView instanceId={1} status="STOPPED" />)
    expect(await screen.findByText('暂无历史日志')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('刷新失败保留同实例旧行并明确未刷新，不把失败覆盖为成功空', async () => {
    loginMockUser()
    let fail = false
    server.use(http.get(API('/logs/federation'), () => fail
      ? HttpResponse.json({}, { status: 500 })
      : HttpResponse.json({
        sourceTag: 'federated',
        coverage: { complete: true, targets: [] },
        items: [{ event_id: 'e1', message: '上次关服现场', event_time_utc: '2026-07-18T12:00:01Z', level: 'ERROR' }],
      })))
    function Refresh() {
      const client = useQueryClient()
      return <button onClick={() => { fail = true; void client.invalidateQueries({ queryKey: ['logsFederation'] }) }}>触发刷新</button>
    }
    renderWithProviders(<><StoppedLogsView instanceId={1} status="STOPPED" /><Refresh /></>)
    await screen.findByText('上次关服现场')
    await userEvent.setup().click(screen.getByRole('button', { name: '触发刷新' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('仍显示上次成功加载的旧行，本次未刷新'))
    expect(screen.getByText('上次关服现场')).toBeInTheDocument()
    expect(screen.queryByText('暂无历史日志')).not.toBeInTheDocument()
  })
})
