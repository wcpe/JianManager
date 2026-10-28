import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import ApprovalsPage from './ApprovalsPage'
import { ApprovalListView } from '@/components/views/approvals/ApprovalListView'
import type { ApprovalTicketInfo } from '@/api/approvals'
import api from '@/api/client'

vi.mock('@/api/client', () => ({
  default: {
    get: vi.fn(),
    post: vi.fn(),
  },
}))

const mockedApi = api as unknown as {
  get: ReturnType<typeof vi.fn>
  post: ReturnType<typeof vi.fn>
}

/** 造一张票据。默认 pending —— 其余用例按需覆盖 status。 */
function ticket(over: Partial<ApprovalTicketInfo> = {}): ApprovalTicketInfo {
  return {
    id: 1,
    uuid: 'u-1',
    applicantUserId: 7,
    applicantKind: 'human',
    action: 'instance_stop',
    targetType: 'instance',
    targetId: '42',
    payloadHash: 'h',
    payloadFrozen: '{"id":42}',
    reason: '维护窗口停机',
    status: 'pending',
    expiresAt: '2030-01-01T00:00:00Z',
    createdAt: '2029-12-31T00:00:00Z',
    updatedAt: '2029-12-31T00:00:00Z',
    ...over,
  }
}

/** 列表接口应答：GET /approvals。 */
function mockList(items: ApprovalTicketInfo[]) {
  mockedApi.get.mockResolvedValue({ data: { items } })
}

describe('ApprovalListView（DOM）', () => {
  it('非 pending 行不渲染决策按钮', () => {
    // 状态机里只有 pending 可被决策。给已完成的行留按钮，等于让审批人点一个
    // 后端必然拒绝的入口——所以这里断言**按钮不存在**，而不是「存在但禁用」。
    const onApprove = vi.fn()
    const onReject = vi.fn()
    renderWithProviders(
      <ApprovalListView
        items={[
          ticket({ id: 1, status: 'pending' }),
          ticket({ id: 2, status: 'succeeded', action: 'file_delete' }),
          ticket({ id: 3, status: 'rejected', action: 'config_write_text' }),
          ticket({ id: 4, status: 'failed', action: 'bot_send_command' }),
        ]}
        onApprove={onApprove}
        onReject={onReject}
      />,
    )

    // 只有 pending 那一行有按钮：批准/驳回各恰好 1 个。
    expect(screen.getAllByRole('button', { name: '批准' })).toHaveLength(1)
    expect(screen.getAllByRole('button', { name: '驳回' })).toHaveLength(1)
  })

  it('无决策权限时不渲染决策按钮', () => {
    renderWithProviders(
      <ApprovalListView items={[ticket()]} onApprove={vi.fn()} onReject={vi.fn()} canDecide={false} />,
    )
    expect(screen.queryByRole('button', { name: '批准' })).toBeNull()
    expect(screen.queryByRole('button', { name: '驳回' })).toBeNull()
  })

  it('详情展开后展示冻结参数；evidence 为字符串 "null" 时不展示空快照块', async () => {
    // 后端把 evidence 存成 text 列，未填时序列化出的是字符串 "null" 而非 nil。
    // 若只判真假值会凭空多出一块写着 null 的「申请时快照」，审批人会以为申请人
    // 留了快照却看不到内容——所以这里断言该块不出现。
    renderWithProviders(
      <ApprovalListView
        items={[ticket({ evidence: 'null' })]}
        expandedId={1}
        onApprove={vi.fn()}
        onReject={vi.fn()}
      />,
    )
    expect(screen.getByText('冻结参数')).toBeInTheDocument()
    expect(screen.getByText('{"id":42}')).toBeInTheDocument()
    expect(screen.queryByText('申请时快照')).toBeNull()
  })

  it('evidence 有内容时展示快照块', () => {
    renderWithProviders(
      <ApprovalListView
        items={[ticket({ evidence: '{"instanceName":"验收实例"}' })]}
        expandedId={1}
        onApprove={vi.fn()}
        onReject={vi.fn()}
      />,
    )
    expect(screen.getByText('申请时快照')).toBeInTheDocument()
    expect(screen.getByText('{"instanceName":"验收实例"}')).toBeInTheDocument()
  })
})

describe('ApprovalsPage（DOM）', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('「全部」按 status=all 下发，不再被后端当作一个不存在的状态而返回空列表', async () => {
    // 这条守的是一个真实踩过的坑：后端缺省把 status 补成 pending，
    // 前端若传别的值又不被识别，「全部」就恒为空，界面表现为「点了全部之后列表空了」。
    // 契约是：前端原样传 all，由服务端归一成「不加状态条件」。
    mockList([ticket()])
    const user = userEvent.setup()
    renderWithProviders(<ApprovalsPage />)

    await waitFor(() => expect(mockedApi.get).toHaveBeenCalled())
    expect(mockedApi.get.mock.calls[0][0]).toBe('/approvals')
    expect(mockedApi.get.mock.calls[0][1].params.status).toBe('pending')

    await user.click(screen.getByRole('button', { name: '全部' }))

    await waitFor(() => {
      const last = mockedApi.get.mock.calls.at(-1)
      expect(last?.[1]?.params?.status).toBe('all')
    })
  })

  it('批准前二次确认，且确认文案带上具体 action', async () => {
    // 批准会真实触发副作用（停实例、删文件…）。确认文案必须点名 action，
    // 否则审批人在弹窗里只看到「确认批准？」而不知道自己批的是什么。
    mockList([ticket({ action: 'instance_stop' })])
    mockedApi.post.mockResolvedValue({ data: {} })
    const user = userEvent.setup()
    renderWithProviders(<ApprovalsPage />)

    await waitFor(() => expect(screen.getByRole('button', { name: '批准' })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: '批准' }))

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/instance_stop/)).toBeInTheDocument()
    // 弹窗出现时**尚未**发出批准请求：真正的提交要在确认之后。
    expect(mockedApi.post).not.toHaveBeenCalled()

    await user.click(within(dialog).getByRole('button', { name: '批准' }))
    await waitFor(() => expect(mockedApi.post).toHaveBeenCalledWith('/approvals/1/approve'))
  })

  it('驳回同样二次确认，取消则不发请求', async () => {
    mockList([ticket()])
    const user = userEvent.setup()
    renderWithProviders(<ApprovalsPage />)

    await waitFor(() => expect(screen.getByRole('button', { name: '驳回' })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: '驳回' }))

    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: '取消' }))

    expect(screen.queryByRole('dialog')).toBeNull()
    expect(mockedApi.post).not.toHaveBeenCalled()
  })
})
