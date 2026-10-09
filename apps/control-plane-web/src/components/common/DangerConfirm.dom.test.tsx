import { describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { useAuthStore } from '@/stores/auth'
import { Role } from '@/lib/danger'
import DangerConfirm from './DangerConfirm'

/** 构造仅供前端解码角色声明的测试 JWT。 */
function makeJwt(role: number): string {
  const b64url = (obj: Record<string, unknown>) =>
    btoa(JSON.stringify(obj)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  return `${b64url({ alg: 'HS256', typ: 'JWT' })}.${b64url({ userId: 1, username: 'tester', role })}.sig`
}

/** 让 DangerConfirm 按指定角色执行前端门禁判定。 */
function loginAs(role: number): void {
  useAuthStore.getState().login(makeJwt(role), 'test-refresh-token')
}

describe('DangerConfirm（FR-059 危险操作确认）', () => {
  it('要求逐字输入确认文本后才允许执行危险操作', async () => {
    loginAs(Role.GroupAdmin)
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
    loginAs(Role.GroupAdmin)
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
})
