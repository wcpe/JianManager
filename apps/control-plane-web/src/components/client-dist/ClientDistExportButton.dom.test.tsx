import { describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import { toast } from 'sonner'
import userEvent from '@testing-library/user-event'
import { HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { domainRoute } from '@jianmanager/devmock/inject'
import ClientDistExportButton from './ClientDistExportButton'

describe('ClientDistExportButton', () => {
  it('点击时携带当前筛选下载 CSV', async () => {
    loginMockUser('admin')
    let requestUrl = ''
    server.use(domainRoute('get', '/client-dist/export', ({ request }) => {
      requestUrl = request.url
      return new HttpResponse('\ufeffid,channelId\n1,stable\n', {
        headers: {
          'Content-Type': 'text/csv; charset=utf-8',
          'Content-Disposition': 'attachment; filename="client-dist-dist-events.csv"',
        },
      })
    }))
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    const user = userEvent.setup()
    renderWithProviders(
      <ClientDistExportButton
        kind="dist-events"
        filters={{ channelId: 'stable', range: '7d', errCode: 'INVALID_CLIENT_KEY', outcome: 'failure', eventKind: 'manifest', machineId: 'm-1' }}
      />,
    )

    await user.click(screen.getByRole('button', { name: '导出 CSV' }))

    const url = new URL(requestUrl)
    expect(url.searchParams.get('kind')).toBe('dist-events')
    expect(url.searchParams.get('channelId')).toBe('stable')
    expect(url.searchParams.get('range')).toBe('7d')
    expect(url.searchParams.get('errCode')).toBe('INVALID_CLIENT_KEY')
    expect(url.searchParams.get('outcome')).toBe('failure')
    expect(url.searchParams.get('eventKind')).toBe('manifest')
    expect(url.searchParams.get('machineId')).toBe('m-1')
    expect(click).toHaveBeenCalledOnce()
    click.mockRestore()
  })

  it('429 时显示每分钟一次的提示', async () => {
    loginMockUser('admin')
    server.use(domainRoute('get', '/client-dist/export', () => HttpResponse.json(
      { error: 'RATE_LIMITED', message: 'CSV 导出每分钟最多一次' },
      { status: 429 },
    )))
    const errorToast = vi.spyOn(toast, 'error').mockImplementation(() => '')
    const user = userEvent.setup()
    renderWithProviders(<ClientDistExportButton kind="security-logs" filters={{ range: '7d' }} />)

    await user.click(screen.getByRole('button', { name: '导出 CSV' }))

    expect(errorToast).toHaveBeenCalledWith('导出过于频繁，请一分钟后重试')
    errorToast.mockRestore()
  })
})
