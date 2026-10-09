import { describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUserAs } from '@/test/auth'
import { Role } from '@/lib/shared/danger'
import DangerConfirm from './DangerConfirm'


describe('DangerConfirm（FR-059 危险操作确认）', () => {
  it('要求逐字输入确认文本后才允许执行危险操作', async () => {
    loginMockUserAs(Role.GroupAdmin)
    const user = userEvent.setup()
    const onConfirm = vi.fn()

    renderWithProviders(
      <DangerConfirm
        open
        title="强制关停实例「survival」？"
        description="强制关停会立即终止进程。"
        confirmLabel="执行"
        confirmText="survival"
        scope="group"
        onConfirm={onConfirm}
        onCancel={vi.fn()}
      />,
    )

    const dialog = await screen.findByRole('dialog', { name: '强制关停实例「survival」？' })
    const execute = within(dialog).getByRole('button', { name: '执行' })
    expect(within(dialog).getByText('请输入「survival」以确认此操作')).toBeInTheDocument()
    expect(execute).toBeDisabled()

    await user.type(within(dialog).getByPlaceholderText('survival'), 'wrong')
    expect(execute).toBeDisabled()

    await user.clear(within(dialog).getByPlaceholderText('survival'))
    await user.type(within(dialog).getByPlaceholderText('survival'), 'survival')
    expect(execute).toBeEnabled()
    await user.click(execute)
    expect(onConfirm).toHaveBeenCalledTimes(1)
  })

  it('组管理员执行平台级危险操作时显示门禁提示且不可确认', async () => {
    loginMockUserAs(Role.GroupAdmin)
    const user = userEvent.setup()
    const onConfirm = vi.fn()

    renderWithProviders(
      <DangerConfirm
        open
        title="下线节点「node-a」？"
        description="节点将被解除注册。"
        confirmLabel="执行"
        confirmText="node-a"
        scope="platform"
        onConfirm={onConfirm}
        onCancel={vi.fn()}
      />,
    )

    const dialog = await screen.findByRole('dialog', { name: '下线节点「node-a」？' })
    expect(within(dialog).getByText('你的角色无权执行此操作，请联系管理员。')).toBeInTheDocument()
    expect(within(dialog).queryByPlaceholderText('node-a')).not.toBeInTheDocument()

    const execute = within(dialog).getByRole('button', { name: '执行' })
    expect(execute).toBeDisabled()
    await user.click(execute)
    expect(onConfirm).not.toHaveBeenCalled()
  })

  /**
   * 正向用例：门禁必须真的区分角色，而不是一律拒绝。
   *
   * 光有「越权被拒」会掩盖一种退化——门禁恒判为拒（例如 hook 恒返回 false），
   * 那样所有危险操作都点不动，而越权用例照样通过。
   */
  it('平台管理员可通过平台级门禁', async () => {
    loginMockUserAs(Role.PlatformAdmin)
    const onConfirm = vi.fn()

    renderWithProviders(
      <DangerConfirm
        open
        title="下线节点「node-a」？"
        description="节点将被解除注册。"
        confirmLabel="执行"
        scope="platform"
        onConfirm={onConfirm}
        onCancel={vi.fn()}
      />,
    )

    const dialog = await screen.findByRole('dialog', { name: '下线节点「node-a」？' })
    expect(within(dialog).queryByText('你的角色无权执行此操作，请联系管理员。')).not.toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: '执行' })).toBeEnabled()
  })

  /**
   * 未声明 scope 时不做前端门禁（普通二次确认）。
   *
   * 这是既有语义：省略 scope 的操作只要求点一次确认，不参与角色判定。
   */
  it('未声明 scope 时不参与角色门禁', async () => {
    loginMockUserAs(Role.Member)

    renderWithProviders(
      <DangerConfirm
        open
        title="清空草稿？"
        confirmLabel="执行"
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />,
    )

    const dialog = await screen.findByRole('dialog', { name: '清空草稿？' })
    expect(within(dialog).getByRole('button', { name: '执行' })).toBeEnabled()
  })
})
