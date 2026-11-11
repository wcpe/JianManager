/**
 * LogsPage 空态文案按原因分支（C 项优化）。
 *
 * 回归背景：空态原本只有一句「暂无日志」，而三种截然不同的原因共用它：
 *   ① 选了「平台」——CP 自身日志只落平台库、不进联邦数据面，**重试一万次也还是空**；
 *   ② 选了「节点」但没登记日志源——属配置缺失，不是等；
 *   ③ 覆盖尚未校验（pending）——等一下重试就行。
 * 混为一谈的结果：用户反复重试或直接报障，而三种里只有③真该重试。
 *
 * 来源筛选只能经下拉框设定（不读 URL 参数），所以这里走真实用户路径。
 */
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUserAs } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import LogsPage from './LogsPage'

const summary = { total: 0, partial: false, truncated: false, quality: 'exact' }

/** 空结果信封：items=null，coverage 可指定。 */
function emptyEnvelope(coverage: Record<string, unknown> = {}, ok = true) {
  return {
    ok,
    items: null,
    next_cursor: '',
    exhausted: false,
    sourceTag: 'federated',
    view: { view_id: 'v-empty' },
    quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
    coverage: { complete: true, pending: false, targets: [], partial_reasons: [], ...coverage },
    notes: [],
  }
}

function stubAll(envelope: () => Record<string, unknown>) {
  server.use(
    http.get(API('/logs/federation'), () => HttpResponse.json(envelope())),
    http.get(API('/logs/federation/stats'), () => HttpResponse.json({ points: [], ...summary })),
    http.get(API('/logs/federation/facets'), () => HttpResponse.json({ dimensions: [], ...summary })),
    http.get(API('/logs/federation/streams'), () => HttpResponse.json({ streams: [] })),
  )
}

/**
 * 经下拉框选一个来源类别（真实用户路径）。
 *
 * 两个坑（照既有 federation 用例的做法）：
 *  - 来源下拉**只在「全部日志」视图渲染**，先切视图；
 *  - jsdom 没实现 scrollIntoView，Radix Select 展开时会调用它，需垫片；
 *    并用键盘激活（聚焦 + Enter）而不是 click，否则弹层不展开。
 */
async function chooseSource(label: string) {
  Element.prototype.scrollIntoView = vi.fn()
  const user = userEvent.setup()
  // 等页面首个可交互元素出现，确认已渲染完成再找视图 tab。
  await screen.findByRole('tablist')
  await user.click(await screen.findByRole('tab', { name: '全部日志' }))
  const trigger = await screen.findByTestId('logs-source-select')
  trigger.focus()
  await user.keyboard('{Enter}')
  await user.click(await screen.findByRole('option', { name: label }))
}

describe('LogsPage 空态按原因分支', () => {
  beforeEach(() => {
    loginMockUserAs(10)
  })

  it('选平台来源实际读取CP文件尾部，文件未创建如实提示', async () => {
    stubAll(() => emptyEnvelope())
    server.use(http.get(API('/diagnostics/log'), () => HttpResponse.json({ content: '', truncated: false, fileBytes: 0, path: '/logs/cp.log', note: '日志文件尚未创建' })))
    renderWithProviders(<LogsPage />, { route: '/logs' })
    await chooseSource('平台')
    await waitFor(() => expect(screen.getByTestId('logs-text-status')).toHaveTextContent('日志文件尚未创建'))
    expect(screen.getByTestId('logs-empty-state')).toHaveAttribute('data-empty-success', 'false')
    expect(screen.queryByTestId('logs-fields-trigger')).not.toBeInTheDocument()
  })

  it('选节点来源先选择具体节点，不提示配置VL日志源', async () => {
    stubAll(() => emptyEnvelope())
    renderWithProviders(<LogsPage />, { route: '/logs' })
    await chooseSource('节点')
    const empty = await screen.findByTestId('logs-empty-state')
    await waitFor(() => expect(empty).toHaveAttribute('data-empty-reason', 'choose_node'))
    expect(screen.getByTestId('logs-empty-hint')).toHaveTextContent('选择节点')
    expect(screen.queryByTestId('logs-fields-trigger')).not.toBeInTheDocument()
  })

  it('覆盖 pending → 提示尚未校验（不得被 not_ready 吞掉）', async () => {
    stubAll(() => emptyEnvelope({ pending: true, complete: false }))
    renderWithProviders(<LogsPage />, { route: '/logs' })

    const empty = await screen.findByTestId('logs-empty-state')
    await waitFor(() => expect(empty).toHaveAttribute('data-empty-reason', 'pending'))
    expect(screen.getByTestId('logs-empty-hint')).toHaveTextContent(/尚未校验/)
  })

  it('普通无数据 → 提示放宽筛选，不说「设计如此」或「去配置」', async () => {
    stubAll(() => emptyEnvelope())
    renderWithProviders(<LogsPage />, { route: '/logs' })

    const empty = await screen.findByTestId('logs-empty-state')
    await waitFor(() => expect(empty).toHaveAttribute('data-empty-reason', 'no_data'))
    const hint = screen.getByTestId('logs-empty-hint')
    expect(hint).toHaveTextContent(/放宽时间范围/)
    expect(hint).not.toHaveTextContent(/设计如此/)
    expect(hint).not.toHaveTextContent(/登记日志源/)
  })

  it('非成功空态（查询失败）仍不得呈「暂无日志」', async () => {
    stubAll(() => emptyEnvelope({ complete: false, partial_reasons: ['OFFLINE'] }, false))
    renderWithProviders(<LogsPage />, { route: '/logs' })

    const empty = await screen.findByTestId('logs-empty-state')
    await waitFor(() => expect(empty).toHaveAttribute('data-empty-success', 'false'))
    expect(screen.queryByText('暂无日志')).not.toBeInTheDocument()
  })
})
