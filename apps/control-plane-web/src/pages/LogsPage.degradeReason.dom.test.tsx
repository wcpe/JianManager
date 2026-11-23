/**
 * 验证两种降级来源在覆盖横幅上给出不同文案，页头只保留统一操作：
 * - 引擎未启用 → 明确未启用；
 * - 接口不可达（404）→ 暂不可用，不诱导用户去开启。
 */
import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { FEDERATED_NOT_READY_MESSAGE } from '@/api/logFederation'
import LogsPage from './LogsPage'

describe('降级文案区分', () => {
  it('引擎未启用 → 覆盖横幅保留明确原因，页头不堆叠说明', async () => {
    loginMockUser()
    server.use(
      http.get(API('/logs/federation'), () =>
        HttpResponse.json({ sourceTag: 'federated', federatedReady: false, items: [], notes: [FEDERATED_NOT_READY_MESSAGE] }),
      ),
    )
    renderWithProviders(<LogsPage />)
    const hint = await screen.findByTestId('logs-coverage-title')
    expect(hint.textContent).toContain('未启用')
    expect(screen.getByTestId('logs-header-actions')).not.toHaveTextContent('未启用')
    expect(screen.queryByTestId('logs-not-ready-hint')).not.toBeInTheDocument()
  })

  it('联邦接口 404 → 不谎称「未启用」，且不给无效的启用指引', async () => {
    loginMockUser()
    server.use(
      http.get(API('/logs/federation'), () =>
        HttpResponse.json({ message: 'not found' }, { status: 404 }),
      ),
    )
    renderWithProviders(<LogsPage />)
    const hint = await screen.findByTestId('logs-coverage-title')
    expect(hint.textContent).toContain('暂不可用')
    expect(hint.textContent).not.toContain('未启用')
    expect(screen.getByTestId('logs-coverage-banner')).not.toHaveTextContent('日志运行时面板启用')
    expect(screen.queryByTestId('logs-not-ready-hint')).not.toBeInTheDocument()
  })
})
