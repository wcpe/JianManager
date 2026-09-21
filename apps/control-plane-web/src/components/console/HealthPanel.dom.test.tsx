import { beforeEach, describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import { http, HttpResponse } from 'msw'

import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { HealthPanel } from './HealthPanel'

/**
 * FR-450 §2.3.2 / FR-447：端口健康可达性按连接态派生四态（可达/不可达/超时/未知）。
 * 探针「在位但本次取不回」应呈现「超时」而非笼统「未知」——这是审计补的缺失态。
 */
function serverState(connected: boolean, available: boolean) {
  return http.get(API('/instances/:id/server-state'), () =>
    HttpResponse.json({
      instanceId: 1,
      connected,
      available,
      state: null,
      error: available ? '' : '采集超时或失败',
    }),
  )
}

describe('HealthPanel 可达性四态（FR-450 / FR-447）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('探针在位且本次取回成功 → 可达', async () => {
    server.use(serverState(true, true))
    renderWithProviders(<HealthPanel instanceId={1} />)
    expect(await screen.findByText('可达')).toBeInTheDocument()
  })

  it('探针在位但本次取不回 → 超时', async () => {
    server.use(serverState(true, false))
    renderWithProviders(<HealthPanel instanceId={1} />)
    expect(await screen.findByText('超时')).toBeInTheDocument()
  })

  it('探针未连入 → 未知', async () => {
    server.use(serverState(false, false))
    renderWithProviders(<HealthPanel instanceId={1} />)
    expect(await screen.findByText('未知')).toBeInTheDocument()
  })
})
