import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { mockInject } from '@jianmanager/devmock/inject'
import InvitePage from './InvitePage'

describe('InvitePage', () => {
  it('只从 URL fragment 读取令牌并接受邀请，不把令牌显示到页面', async () => {
    mockInject('post', '/auth/invitations/accept', { kind: 'status', status: 201 })
    renderWithProviders(<InvitePage />, { route: '/invite#invite-token-for-test' })
    const user = userEvent.setup()

    await user.type(await screen.findByLabelText(/用户名/), 'invited-member')
    await user.type(screen.getByLabelText(/密码/), 'password123')
    await user.click(screen.getByRole('button', { name: '接受邀请' }))

    expect(await screen.findByText('账号已创建，请登录')).toBeInTheDocument()
    expect(document.body.textContent).not.toContain('invite-token-for-test')
  })
})
