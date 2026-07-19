import { beforeEach, describe, expect, it } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'

import { API } from '@jianmanager/devmock/api'
import { db } from '@jianmanager/devmock/db'
import type { MockInstance } from '@jianmanager/devmock/handlers/domains/instance'
import { server } from '@jianmanager/devmock/server'
import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import InstanceConsolePage from './InstanceConsolePage'

describe('InstanceConsolePage 损毁与重建契约（FR-342）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('DAMAGED 显示损毁徽章与失败原因，只提供重建入口并调用重建端点', async () => {
    const rebuildRequests: string[] = []
    db<MockInstance>('instances').update(3, {
      status: 'DAMAGED',
      statusReason: '搭建未完成：核心下载校验失败',
    })
    server.use(
      http.post(API('/instances/:id/rebuild'), ({ request }) => {
        rebuildRequests.push(new URL(request.url).pathname)
        return HttpResponse.json({ taskId: 'task-rebuild-3' }, { status: 202 })
      }),
    )

    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })

    expect(await screen.findByText('损毁')).toBeInTheDocument()
    const failure = screen.getByRole('alert')
    expect(failure).toHaveTextContent('搭建未完成：核心下载校验失败')
    expect(screen.queryByRole('button', { name: '启动' })).not.toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: '重建' }))
    await waitFor(() => expect(rebuildRequests).toEqual(['/api/v1/instances/3/rebuild']))
  })

  it('重建中禁用重建且不恢复启动入口，也不把进行中原因渲染为失败横幅', async () => {
    db<MockInstance>('instances').update(3, {
      status: 'DAMAGED',
      statusReason: '重建中：正在重新下载核心',
    })

    renderWithProviders(<InstanceConsolePage instanceId={3} />, { route: '/instances/3' })

    const rebuild = await screen.findByRole('button', { name: '重建' })
    expect(rebuild).toBeDisabled()
    expect(rebuild.closest('span')).toHaveAttribute('title', '重建中…')
    expect(screen.queryByRole('button', { name: '启动' })).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})
