import { describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { Toaster } from 'sonner'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import InstanceBatchBar, { type BatchSelectedInstance } from './InstanceBatchBar'

const running: BatchSelectedInstance = { id: 101, name: 'fr058-running-a', status: 'RUNNING' }
const runningB: BatchSelectedInstance = { id: 102, name: 'fr058-running-b', status: 'RUNNING' }
const stopped: BatchSelectedInstance = { id: 103, name: 'fr058-stopped-c', status: 'STOPPED' }

/** 渲染批量栏并返回用户交互对象与回调 spy。 */
function renderBatchBar(selected: BatchSelectedInstance[]) {
  loginMockUser()
  const user = userEvent.setup()
  const onClear = vi.fn()
  const onRetainFailed = vi.fn()
  renderWithProviders(
    <>
      <InstanceBatchBar selected={selected} onClear={onClear} onRetainFailed={onRetainFailed} />
      <Toaster />
    </>,
  )
  return { user, onClear, onRetainFailed }
}

describe('InstanceBatchBar（FR-058 实例批量操作）', () => {
  it('批量下发命令会二次确认并提交 ids + command', async () => {
    let captured: unknown = null
    server.use(
      http.post(API('/instances/batch'), async ({ request }) => {
        captured = await request.json()
        return HttpResponse.json({ action: 'command', requested: 2, succeeded: 2, failed: 0, skipped: 0, errors: [] })
      }),
    )

    const { user, onClear } = renderBatchBar([running, runningB])

    expect(screen.getByText('已选 2 个')).toBeInTheDocument()
    await user.type(screen.getByPlaceholderText(/输入要下发到选中运行中实例的命令/), 'say fr058')
    await user.click(screen.getByRole('button', { name: '下发命令' }))

    const dialog = await screen.findByRole('dialog', { name: '确认批量下发命令？' })
    expect(within(dialog).getByText(/say fr058/)).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '执行' }))

    await waitFor(() => expect(captured).toEqual({ action: 'command', ids: [101, 102], command: 'say fr058' }))
    expect(onClear).toHaveBeenCalledTimes(1)
  })

  it('批量强制关服必须输入 FORCE 才能执行危险操作', async () => {
    let captured: unknown = null
    server.use(
      http.post(API('/instances/batch'), async ({ request }) => {
        captured = await request.json()
        return HttpResponse.json({ action: 'kill', requested: 1, succeeded: 1, failed: 0, skipped: 0, errors: [] })
      }),
    )

    const { user } = renderBatchBar([running])

    await user.click(screen.getByRole('button', { name: '批量强制关服' }))
    const dialog = await screen.findByRole('dialog', { name: '确认批量强制关服？' })
    const execute = within(dialog).getByRole('button', { name: '执行' })
    expect(execute).toBeDisabled()

    await user.type(within(dialog).getByPlaceholderText('FORCE'), 'FORCE')
    expect(execute).toBeEnabled()
    await user.click(execute)

    await waitFor(() => expect(captured).toEqual({ action: 'kill', ids: [101] }))
  })

  it('部分失败时列出失败实例并保留失败项供重试', async () => {
    server.use(
      http.post(API('/instances/batch'), () => HttpResponse.json({
        action: 'command',
        requested: 2,
        succeeded: 1,
        failed: 1,
        skipped: 0,
        errors: [{ instanceId: 103, error: '实例未运行，无法下发命令' }],
      })),
    )

    const { user, onRetainFailed } = renderBatchBar([running, stopped])

    await user.type(screen.getByPlaceholderText(/输入要下发到选中运行中实例的命令/), 'say partial')
    await user.click(screen.getByRole('button', { name: '下发命令' }))
    await user.click(within(await screen.findByRole('dialog', { name: '确认批量下发命令？' })).getByRole('button', { name: '执行' }))

    const failDialog = await screen.findByRole('dialog', { name: '部分操作失败' })
    expect(within(failDialog).getByText('fr058-stopped-c')).toBeInTheDocument()
    expect(within(failDialog).getByText('实例未运行，无法下发命令')).toBeInTheDocument()
    expect(onRetainFailed).toHaveBeenCalledWith([103])
  })
})
