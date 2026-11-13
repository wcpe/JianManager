import { describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import type { LogEntry } from '@/lib/console/console-log-types'
import { InstanceOverviewLatestLogs } from './InstanceOverviewLatestLogs'

function entry(index: number): LogEntry {
  return { id: index, source: 'instance', level: 'info', instanceId: 1, instanceUuid: 'one', nodeId: 1, message: `startup line ${index}`, time: new Date(Date.UTC(2026, 10, 12, 10, 0, index)).toISOString() }
}

describe('InstanceOverviewLatestLogs', () => {
  it('按最新优先显示最多30行，正文可换行且滚动区域有界。', () => {
    const logs = Array.from({ length: 40 }, (_, index) => entry(index))
    const original = logs.map((log) => log.id)
    renderWithProviders(<InstanceOverviewLatestLogs logs={logs} onOpenLogs={vi.fn()} />)

    const preview = screen.getByRole('region', { name: '最近日志' })
    const rows = within(preview).getAllByTestId('overview-log-line')
    expect(rows).toHaveLength(30)
    expect(rows[0]).toHaveTextContent('startup line 39')
    expect(rows[29]).toHaveTextContent('startup line 10')
    expect(logs.map((log) => log.id)).toEqual(original)
    expect(preview).toHaveClass('h-[200px]', 'overflow-auto')
    expect(within(rows[0]!).getByText('startup line 39')).toHaveClass('whitespace-pre-wrap', 'break-words')
  })

  it('日志入口可点击，加载、空、失败互不冒充。', async () => {
    const onOpenLogs = vi.fn()
    const user = userEvent.setup()
    const { rerender } = renderWithProviders(<InstanceOverviewLatestLogs logs={[]} isLoading onOpenLogs={onOpenLogs} />)
    expect(screen.getByRole('status')).toHaveTextContent('加载')
    expect(screen.queryByText('暂无日志')).not.toBeInTheDocument()
    rerender(<InstanceOverviewLatestLogs logs={[]} isError onOpenLogs={onOpenLogs} />)
    expect(screen.getByRole('alert')).toHaveTextContent('暂时无法读取最近日志')
    expect(screen.queryByText('暂无日志')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /查看日志/ }))
    expect(onOpenLogs).toHaveBeenCalledTimes(1)
    rerender(<InstanceOverviewLatestLogs logs={[]} onOpenLogs={onOpenLogs} />)
    expect(screen.getByText('暂无日志')).toBeInTheDocument()
  })

  it('覆盖未确认时仍显示已返回记录，明确标注快照窗口。', () => {
    renderWithProviders(<InstanceOverviewLatestLogs logs={[entry(1)]} isIncomplete onOpenLogs={vi.fn()} />)
    expect(screen.getByRole('note')).toHaveTextContent('最近一小时快照，覆盖尚未确认')
    expect(screen.getByText('startup line 1')).toBeInTheDocument()
  })

  it('刷新失败保留最近数据并给出错误提示；坏时间不会显示Invalid Date。', () => {
    renderWithProviders(<InstanceOverviewLatestLogs logs={[{ ...entry(1), time: '' }]} isError onOpenLogs={vi.fn()} />)
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByText('startup line 1')).toBeInTheDocument()
    expect(screen.queryByText('Invalid Date')).not.toBeInTheDocument()
  })
})
