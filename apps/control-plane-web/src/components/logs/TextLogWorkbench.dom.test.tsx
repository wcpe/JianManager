import { beforeEach, describe, expect, it } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import { renderWithProviders } from '@/test/render'
import { loginMockUserAs } from '@/test/auth'
import { TextLogWorkbench } from './TextLogWorkbench'

const props = { nodes: [{ id: 1, name: 'dev-node', status: 1 }], onNodeChange: () => {}, sourceControl: <span>source</span> }
const tail = { content: 'time=2026-11-11T10:00:00Z level=INFO msg="ready"\ntime=2026-11-11T10:00:01Z level=ERROR msg="failed"', truncated: true, fileBytes: 100000, path: '/var/log/process.log' }

beforeEach(() => {
  loginMockUserAs(10)
  document.getSelection()?.removeAllRanges()
})

describe('text log windows', () => {
  it('reads the node text endpoint without requesting federation and shows bounded metadata', async () => {
    server.use(http.get(API('/nodes/1/diagnostics/log'), ({ request }) => {
      expect(new URL(request.url).searchParams.get('lines')).toBe('200')
      return HttpResponse.json(tail)
    }), http.get(API('/logs/federation'), () => { throw new Error('file logs must never query VL') }))
    renderWithProviders(<TextLogWorkbench {...props} kind="worker" nodeId={1} />)
    expect(await screen.findByText(/msg="failed"/)).toBeInTheDocument()
    expect(screen.getByText(tail.path)).toBeInTheDocument()
    expect(screen.getByTestId('logs-text-status')).toHaveTextContent('截断')
    expect(screen.queryByTestId('logs-federation-insights')).not.toBeInTheDocument()
    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('搜索已加载文本…'), 'failed')
    expect(screen.getAllByTestId('log-message')).toHaveLength(1)
  })

  it('requires an explicit node and does not invent an all-node file window', async () => {
    renderWithProviders(<TextLogWorkbench {...props} kind="worker" nodeId={null} />)
    expect(screen.getByTestId('logs-empty-state')).toHaveAttribute('data-empty-reason', 'choose_node')
    expect(screen.getByRole('button', { name: '刷新' })).toBeDisabled()
  })

  it('shows source failures rather than treating an unread file as an empty success', async () => {
    server.use(http.get(API('/diagnostics/log'), () => HttpResponse.json({ message: 'log file unavailable' }, { status: 503 })))
    renderWithProviders(<TextLogWorkbench {...props} kind="control_plane" nodeId={null} />)
    expect(await screen.findByRole('alert')).toHaveTextContent('log file unavailable')
    expect(screen.getByTestId('logs-empty-state')).toHaveAttribute('data-empty-success', 'false')
  })

  it('keeps native text selection and releases refresh after selection is cleared', async () => {
    server.use(http.get(API('/diagnostics/log'), () => HttpResponse.json(tail)))
    renderWithProviders(<TextLogWorkbench {...props} kind="control_plane" nodeId={null} />)
    await screen.findByText(/msg="failed"/)
    const message = screen.getAllByTestId('log-message')[0]
    const range = document.createRange()
    range.selectNodeContents(message)
    document.getSelection()?.addRange(range)
    fireEvent(document, new Event('selectionchange'))
    await waitFor(() => expect(screen.getByRole('button', { name: '刷新' })).toBeDisabled())
    expect(document.getSelection()?.toString()).toContain('failed')
    document.getSelection()?.removeAllRanges()
    fireEvent(document, new Event('selectionchange'))
    await waitFor(() => expect(screen.getByRole('button', { name: '刷新' })).toBeEnabled())
  })
})
