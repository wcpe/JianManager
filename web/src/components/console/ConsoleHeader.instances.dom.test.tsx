import { describe, expect, it } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { server } from '@/mocks/server'
import ConsoleHeader from './ConsoleHeader'
import CommandPalette from './CommandPalette'

function collectInstanceRequests() {
  const paths: string[] = []
  const listener = ({ request }: { request: Request }) => {
    const url = new URL(request.url)
    if (url.pathname.startsWith('/api/v1/instances')) paths.push(url.pathname)
  }
  server.events.on('request:start', listener)
  return {
    paths,
    stop: () => server.events.removeListener('request:start', listener),
  }
}

describe('ConsoleHeader 实例规模化数据源', () => {
  it('页眉与命令面板不触发裸实例全集请求', async () => {
    loginMockUser()
    const requests = collectInstanceRequests()
    try {
      renderWithProviders(
        <>
          <ConsoleHeader />
          <CommandPalette />
        </>,
        { route: '/instances' },
      )

      await screen.findByRole('button', { name: /搜索/ })
      await userEvent.click(screen.getByRole('button', { name: /搜索/ }))
      await screen.findByRole('dialog')
      await waitFor(() => expect(requests.paths).toContain('/api/v1/instances/search'))

      expect(requests.paths).toContain('/api/v1/instances/aggregate')
      expect(requests.paths).not.toContain('/api/v1/instances')
    } finally {
      requests.stop()
    }
  })
})
