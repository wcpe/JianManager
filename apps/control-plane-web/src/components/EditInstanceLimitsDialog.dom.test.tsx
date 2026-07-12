import { describe, it, expect, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import EditInstanceLimitsDialog from './EditInstanceLimitsDialog'

describe('EditInstanceLimitsDialog 资源限额编辑器（FR-079）', () => {
  it('具备共享 Dialog 语义并支持 Esc 关闭', async () => {
    const onClose = vi.fn()
    const user = userEvent.setup()

    renderWithProviders(
      <EditInstanceLimitsDialog
        instanceId={7}
        instanceName="docker-mc"
        processType="docker"
        cpuLimit={0}
        memLimitMb={0}
        diskLimitMb={0}
        onClose={onClose}
      />,
    )

    const heading = await screen.findByRole('heading', { name: 'docker-mc 的资源限额' })
    expect(heading.closest('[role="dialog"]')).toBeInTheDocument()

    await user.keyboard('{Escape}')

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  })

  it('Docker 模式提交 CPU / 内存 / 磁盘限额，留空字段按 0 清除', async () => {
    const user = userEvent.setup()
    let payload: Record<string, unknown> | undefined
    server.use(
      http.put(API('/instances/:id'), async ({ request }) => {
        payload = await request.json() as Record<string, unknown>
        return HttpResponse.json({ id: 7, ...payload })
      }),
    )

    renderWithProviders(
      <EditInstanceLimitsDialog
        instanceId={7}
        instanceName="docker-mc"
        processType="docker"
        cpuLimit={0}
        memLimitMb={0}
        diskLimitMb={0}
        onClose={() => {}}
      />,
    )

    await user.type(screen.getByPlaceholderText('1.5'), '1.5')
    await user.type(screen.getByPlaceholderText('2048'), '2048')
    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(payload).toBeDefined())
    expect(payload).toEqual({
      cpuLimit: 1.5,
      memLimitMb: 2048,
      diskLimitMb: 0,
    })
  })

  it('允许负值作为不限制语义提交给后端归一化', async () => {
    const user = userEvent.setup()
    let payload: Record<string, unknown> | undefined
    server.use(
      http.put(API('/instances/:id'), async ({ request }) => {
        payload = await request.json() as Record<string, unknown>
        return HttpResponse.json({ id: 7, ...payload })
      }),
    )

    renderWithProviders(
      <EditInstanceLimitsDialog
        instanceId={7}
        instanceName="docker-mc"
        processType="docker"
        cpuLimit={0}
        memLimitMb={0}
        diskLimitMb={0}
        onClose={() => {}}
      />,
    )

    await user.type(screen.getByPlaceholderText('1.5'), '-1')
    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(payload).toBeDefined())
    expect(payload).toEqual({
      cpuLimit: -1,
      memLimitMb: 0,
      diskLimitMb: 0,
    })
  })

  it('非 Docker 模式只提示资源限额需要 Docker，不提交更新', async () => {
    const user = userEvent.setup()
    let called = false
    server.use(
      http.put(API('/instances/:id'), () => {
        called = true
        return HttpResponse.json({})
      }),
    )

    renderWithProviders(
      <EditInstanceLimitsDialog
        instanceId={8}
        instanceName="direct-server"
        processType="direct"
        cpuLimit={1}
        memLimitMb={1024}
        diskLimitMb={4096}
        onClose={() => {}}
      />,
    )

    expect(screen.getByText(/资源限额需 Docker 模式/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '关闭' }))
    expect(called).toBe(false)
  })
})
