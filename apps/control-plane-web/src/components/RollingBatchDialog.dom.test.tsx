import { describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import RollingBatchDialog from './RollingBatchDialog'
import type { BatchSelectedInstance } from './InstanceBatchBar'

const selected: BatchSelectedInstance[] = [
  { id: 101, name: 'a', status: 'RUNNING' },
  { id: 102, name: 'b', status: 'RUNNING' },
]

function renderDialog() {
  loginMockUser()
  const user = userEvent.setup()
  const onClose = vi.fn()
  renderWithProviders(<RollingBatchDialog selected={selected} onClose={onClose} />)
  return { user, onClose }
}

describe('RollingBatchDialog（FR-457 滚动/分批/灰度编排）', () => {
  it('按默认策略创建编排并展示进度', async () => {
    const { user } = renderDialog()

    expect(screen.getByText(/对选中的 2 个实例按批推进/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '开始编排' }))

    // 进度视图出现，且计数反映两台已执行（假后端创建即执行全量并置 done）。
    expect(await screen.findByTestId('rolling-progress')).toBeInTheDocument()
    expect(await screen.findByText('2/2')).toBeInTheDocument()
  })

  it('提交策略参数：批大小/批间隔/失败即停/灰度比例', async () => {
    let captured: unknown = null
    server.use(
      http.post(API('/instances/rolling'), async ({ request }) => {
        captured = await request.json()
        return HttpResponse.json({
          id: 55, action: 'restart', state: 'done', targets: [101], requested: 1, cursor: 1,
          succeeded: 1, failed: 0, skipped: 0, errors: [], batchSize: 0, batchIntervalSec: 0,
          failFast: true, ratio: 0.2, createdAt: '', updatedAt: '',
        })
      }),
    )
    const { user } = renderDialog()

    const sizeInput = screen.getByLabelText('批大小（台/批）')
    await user.clear(sizeInput)
    await user.type(sizeInput, '0')
    const intervalInput = screen.getByLabelText('批间隔（秒）')
    await user.clear(intervalInput)
    await user.type(intervalInput, '0')
    await user.click(screen.getByRole('button', { name: '20%' }))
    await user.click(screen.getByRole('button', { name: '开始编排' }))

    await waitFor(() =>
      expect(captured).toEqual({
        action: 'restart',
        ids: [101, 102],
        batchSize: 0,
        batchIntervalSec: 0,
        failFast: true,
        ratio: 0.2,
      }),
    )
  })

  it('进度视图可暂停/继续/取消（FR-457）', async () => {
    let state = 'running'
    const op = () => ({
      id: 9, action: 'restart', state, targets: [101, 102], requested: 2, cursor: 0,
      succeeded: 0, failed: 0, skipped: 0, errors: [], batchSize: 5, batchIntervalSec: 30,
      failFast: true, ratio: 0, createdAt: '', updatedAt: '',
    })
    const calls: string[] = []
    server.use(
      http.post(API('/instances/rolling'), () => HttpResponse.json(op())),
      http.get(API('/instances/rolling/9'), () => HttpResponse.json(op())),
      http.post(API('/instances/rolling/9/pause'), () => { calls.push('pause'); state = 'paused'; return HttpResponse.json(op()) }),
      http.post(API('/instances/rolling/9/cancel'), () => { calls.push('cancel'); state = 'canceled'; return HttpResponse.json(op()) }),
    )
    const { user } = renderDialog()

    await user.click(screen.getByRole('button', { name: '开始编排' }))
    await screen.findByTestId('rolling-progress')

    await user.click(screen.getByTestId('rolling-pause'))
    await waitFor(() => expect(calls).toContain('pause'))
    // 暂停后出现「继续」。
    expect(await screen.findByTestId('rolling-resume')).toBeInTheDocument()

    await user.click(screen.getByTestId('rolling-cancel'))
    await waitFor(() => expect(calls).toContain('cancel'))
  })
})
