import { describe, expect, it, vi } from 'vitest'
import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useInstance, useInstanceLifecyclePendingMap, useSearchInstances, useStartInstance, useStopInstance } from './instances'

function ConcurrentProbe() {
  const first = useStartInstance()
  const second = useStartInstance()
  const stop = useStopInstance()
  const pending = useInstanceLifecyclePendingMap()
  return <>
    <button onClick={() => { first.mutate(42); second.mutate(42); stop.mutate(42) }}>同时提交</button>
    <button onClick={() => second.mutate(43)}>启动另一台</button>
    <output aria-label="pending42">{String(pending.get(42)?.starting ?? false)}</output>
    <output aria-label="pending43">{String(pending.get(43)?.starting ?? false)}</output>
  </>
}

function FailedPreflightProbe() {
  const start = useStartInstance()
  const detail = useInstance(42)
  const list = useSearchInstances()
  return <>
    <button onClick={() => start.mutate(42)}>启动</button>
    <output aria-label="detail-reason">{detail.data?.statusReason ?? 'none'}</output>
    <output aria-label="list-reason">{list.data?.items[0]?.statusReason ?? 'none'}</output>
    <output aria-label="detail-state">{detail.data?.status ?? 'loading'}</output>
  </>
}

describe('实例生命周期 mutation cache', () => {
  it('同实例跨 hook/跨动作重复提交只发一次 HTTP，别的实例可同时发且保留各自在途态', async () => {
    loginMockUser()
    const user = userEvent.setup()
    let release!: () => void
    const gate = new Promise<void>((resolve) => { release = resolve })
    const start42 = vi.fn(async () => { await gate; return HttpResponse.json({}) })
    const start43 = vi.fn(async () => { await gate; return HttpResponse.json({}) })
    const stop42 = vi.fn(() => HttpResponse.json({}))
    server.use(
      http.post(API('/instances/42/start'), start42),
      http.post(API('/instances/43/start'), start43),
      http.post(API('/instances/42/stop'), stop42),
    )
    renderWithProviders(<ConcurrentProbe />)
    try {
      await user.click(screen.getByRole('button', { name: '同时提交' }))
      await waitFor(() => expect(start42).toHaveBeenCalledTimes(1))
      expect(stop42).not.toHaveBeenCalled()
      expect(screen.getByLabelText('pending42')).toHaveTextContent('true')
      await user.click(screen.getByRole('button', { name: '启动另一台' }))
      await waitFor(() => expect(start43).toHaveBeenCalledTimes(1))
      expect(screen.getByLabelText('pending42')).toHaveTextContent('true')
      expect(screen.getByLabelText('pending43')).toHaveTextContent('true')
    } finally {
      await act(async () => { release(); await gate })
    }
    await waitFor(() => expect(screen.getByLabelText('pending42')).toHaveTextContent('false'))
    await waitFor(() => expect(screen.getByLabelText('pending43')).toHaveTextContent('false'))
  })

  it('预检 422 后刷新列表和详情原因，且不乐观伪造 STARTING', async () => {
    loginMockUser()
    const user = userEvent.setup()
    let reason = ''
    let release!: () => void
    const gate = new Promise<void>((resolve) => { release = resolve })
    const instance = () => ({ id: 42, uuid: 'preflight', status: 'STOPPED', statusReason: reason })
    server.use(
      http.get(API('/instances/42'), () => HttpResponse.json(instance())),
      http.get(API('/instances/search'), () => HttpResponse.json({ items: [instance()], total: 1, page: 1, pageSize: 50 })),
      http.post(API('/instances/42/start'), async () => {
        await gate
        reason = '启动失败：实例未绑定 JDK'
        return HttpResponse.json({ error: 'PREFLIGHT_FAILED', message: reason }, { status: 422 })
      }),
    )
    renderWithProviders(<FailedPreflightProbe />)
    await waitFor(() => expect(screen.getByLabelText('detail-state')).toHaveTextContent('STOPPED'))
    await user.click(screen.getByRole('button', { name: '启动' }))
    expect(screen.getByLabelText('detail-state')).toHaveTextContent('STOPPED')
    await act(async () => { release(); await gate })
    await waitFor(() => expect(screen.getByLabelText('detail-reason')).toHaveTextContent('启动失败：实例未绑定 JDK'))
    await waitFor(() => expect(screen.getByLabelText('list-reason')).toHaveTextContent('启动失败：实例未绑定 JDK'))
    expect(screen.getByLabelText('detail-state')).toHaveTextContent('STOPPED')
  })
})
