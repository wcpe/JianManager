import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
import { useInstanceAggregate, useSearchInstances } from './instances'

function Probe() {
  const search = useSearchInstances({
    q: 'server',
    nodeId: 2,
    page: 2,
    pageSize: 25,
    sort: 'createdAt',
    order: 'desc',
  })
  const aggregate = useInstanceAggregate({ q: 'server', nodeId: 2, status: 'RUNNING' })

  return (
    <div>
      <output aria-label="search-total">{search.data?.total ?? 'loading'}</output>
      <output aria-label="aggregate-total">{aggregate.data?.total ?? 'loading'}</output>
    </div>
  )
}

describe('实例规模化查询 hook', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('useSearchInstances / useInstanceAggregate 按 FR-247 端点传递查询参数', async () => {
    const seen: Record<string, string>[] = []
    server.use(
      http.get(API('/instances/search'), ({ request }) => {
        seen.push(Object.fromEntries(new URL(request.url).searchParams.entries()))
        return HttpResponse.json({ items: [], total: 321, page: 2, pageSize: 25 })
      }),
      http.get(API('/instances/aggregate'), ({ request }) => {
        seen.push(Object.fromEntries(new URL(request.url).searchParams.entries()))
        return HttpResponse.json({
          total: 12,
          byStatus: { RUNNING: 12, STOPPED: 0, CRASHED: 0, STARTING: 0, STOPPING: 0 },
          byNode: [{ nodeId: 2, count: 12 }],
          byRole: { backend: 12, proxy: 0, universal: 0 },
        })
      }),
    )

    renderWithProviders(<Probe />)

    await waitFor(() => expect(screen.getByLabelText('search-total')).toHaveTextContent('321'))
    await waitFor(() => expect(screen.getByLabelText('aggregate-total')).toHaveTextContent('12'))
    await waitFor(() => expect(seen).toHaveLength(2))
    expect(seen[0]).toMatchObject({ q: 'server', nodeId: '2', page: '2', pageSize: '25', sort: 'createdAt', order: 'desc' })
    expect(seen[1]).toMatchObject({ q: 'server', nodeId: '2', status: 'RUNNING' })
  })
})
