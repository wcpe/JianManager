/**
 * 验证降级两种来源在标题区与横幅上给出不同文案：
 * - 引擎未启用 → 去节点启用日志平台的指引；
 * - 接口不可达（404）→ 只说可查范围，不诱导用户去开启。
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
  it('引擎未启用 → 标题区提示「未启用」并带启用指引', async () => {
    loginMockUser()
    server.use(
      http.get(API('/logs/federation'), () =>
        HttpResponse.json({ sourceTag: 'federated', federatedReady: false, items: [], notes: [FEDERATED_NOT_READY_MESSAGE] }),
      ),
    )
    renderWithProviders(<LogsPage />)
    const hint = await screen.findByTestId('logs-not-ready-hint')
    expect(hint.textContent).toContain('未启用')
    // 指引里点明去哪启用。
    expect(hint.getAttribute('title')).toContain('节点')
  })

  it('联邦接口 404 → 不谎称「未启用」，且不给无效的启用指引', async () => {
    loginMockUser()
    server.use(
      http.get(API('/logs/federation'), () =>
        HttpResponse.json({ message: 'not found' }, { status: 404 }),
      ),
    )
    renderWithProviders(<LogsPage />)
    const hint = await screen.findByTestId('logs-not-ready-hint')
    expect(hint.textContent).toContain('暂不可用')
    expect(hint.textContent).not.toContain('未启用')
    // 接口没部署时让用户去「启用」是无效指令。
    expect(hint.getAttribute('title')).not.toContain('日志运行时面板启用')
  })
})
