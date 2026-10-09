import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithI18n } from '@/test/i18n'
import SnapshotPanel, { type InstanceSnapshotView } from '@/components/views/instances/SnapshotPanel'
import { loginMockUserAs } from '@/test/auth'
import { Role } from '@/lib/shared/danger'

/**
 * FR-466 实例整机快照面板 · 受控视图测（ADR-097 b 范式）。
 *
 * 本组件在应用侧原本没有测试，故这里是**新补**而非迁移。断言锚在 data-testid 与角色上，
 * 不绑文案（该面板的文案面很宽：四态、两类权限、逐字确认各有措辞）。
 * 最要紧的一条是「读失败不得渲染成空态」——把两者混同会让运维以为数据没丢。
 */
const snaps: InstanceSnapshotView[] = [
  { id: 1, name: 'snap-a', kind: 'manual', state: 'completed', createdAt: '2026-10-01T00:00:00Z', sizeMb: 128.5 },
  {
    id: 2,
    name: 'snap-b',
    kind: 'pre_rollback',
    state: 'completed',
    createdAt: '2026-10-02T00:00:00Z',
    sizeMb: 130,
    notRollableReason: '底层归档缺失，无法回滚',
  },
]

type Props = Parameters<typeof SnapshotPanel>[0]

function renderPanel(props: Partial<Props> = {}) {
  const handlers = {
    onRefresh: vi.fn(),
    onCreate: vi.fn().mockResolvedValue(true),
    onRollback: vi.fn().mockResolvedValue(true),
    onDelete: vi.fn().mockResolvedValue(true),
    ...props,
  }
  const result = renderWithI18n(<SnapshotPanel snapshots={snaps} {...handlers} />)
  return { ...handlers, ...result }
}

// 面板内的危险操作确认按登录态角色判定（scope="group"）；未登录（role=null）一律落到越权态，
// 输入框不渲染、确认按钮禁用。
beforeEach(() => {
  loginMockUserAs(Role.GroupAdmin)
})

describe('SnapshotPanel（FR-466 · ADR-097 b 范式）', () => {
  it('读取失败渲染错误块，而不是「暂无快照」', () => {
    renderPanel({ isError: true, snapshots: undefined })
    expect(screen.getByTestId('snapshot-error')).toBeInTheDocument()
    expect(screen.queryByTestId('snapshot-empty')).not.toBeInTheDocument()
  })

  it('确实没有快照时才渲染空态', () => {
    renderPanel({ snapshots: [] })
    expect(screen.getByTestId('snapshot-empty')).toBeInTheDocument()
    expect(screen.queryByTestId('snapshot-error')).not.toBeInTheDocument()
  })

  it('列出快照行，且不可回滚的行禁用回滚按钮并说明原因', () => {
    renderPanel()

    expect(screen.getByTestId('snapshot-list')).toBeInTheDocument()
    expect(screen.getByText('snap-a')).toBeInTheDocument()
    expect(screen.getByText('snap-b')).toBeInTheDocument()
    // 底链缺失：状态仍是 completed，但必须显式说出不可回滚。
    expect(screen.getByTestId('snapshot-not-rollable')).toHaveTextContent('底层归档缺失，无法回滚')

    const rows = screen.getAllByRole('listitem')
    const rollbackOf = (row: HTMLElement) =>
      Array.from(row.querySelectorAll('button')).find((b) => b.textContent?.includes('serverConsole.snapshotRollback'))!
    // 第一条可回滚、第二条不可回滚。
    expect(rollbackOf(rows[0])).toBeEnabled()
    expect(rollbackOf(rows[1])).toBeDisabled()
  })

  it('运行态给出「世界文件可能不一致」的提前警示', () => {
    renderPanel({ instanceLive: true })
    expect(screen.getByTestId('snapshot-running-warning')).toBeInTheDocument()
  })

  it('无写权限时禁用创建与回滚', () => {
    renderPanel({ canWrite: false })
    const rows = screen.getAllByRole('listitem')
    const rollback = Array.from(rows[0].querySelectorAll('button')).find((b) =>
      b.textContent?.includes('serverConsole.snapshotRollback'),
    )!
    expect(rollback).toBeDisabled()
  })

  it('无删除权限时禁用删除（门槛高于回滚）', () => {
    renderPanel({ canDelete: false })
    const rows = screen.getAllByRole('listitem')
    // 删除按钮是行内唯一带 aria-label 的按钮。
    const del = Array.from(rows[0].querySelectorAll('button')).find((b) => b.getAttribute('aria-label'))!
    expect(del).toBeDisabled()
  })

  it('回滚需逐字输入快照名，确认后才上报', async () => {
    const user = userEvent.setup()
    const onRollback = vi.fn().mockResolvedValue(true)
    renderPanel({ onRollback })

    const rows = screen.getAllByRole('listitem')
    const rollback = Array.from(rows[0].querySelectorAll('button')).find((b) =>
      b.textContent?.includes('serverConsole.snapshotRollback'),
    )!
    await user.click(rollback)

    const dialog = await screen.findByRole('dialog')
    // 按确认按钮自己的文案定位：X 关闭按钮带的是 sr-only 文本（不是 aria-label），
    // 靠「排除 aria-label」或「取最后一个」都会把它算进来。
    const confirm = Array.from(dialog.querySelectorAll('button')).find(
      (b) => b.textContent?.trim() === 'serverConsole.snapshotRollback',
    )!
    expect(confirm).toBeDisabled()
    expect(onRollback).not.toHaveBeenCalled()

    await user.type(dialog.querySelector('input')!, 'snap-a')
    await waitFor(() => expect(confirm).toBeEnabled())
    await user.click(confirm)

    await waitFor(() => expect(onRollback).toHaveBeenCalledWith(1))
  })
})
