import { describe, it, expect, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import EditInstanceConfigDialog from './EditInstanceConfigDialog'

describe('EditInstanceConfigDialog（共享 Dialog）', () => {
  it('具备共享 Dialog 语义并支持 Esc 关闭', async () => {
    const onClose = vi.fn()
    const user = userEvent.setup()
    loginMockUser()

    renderWithProviders(
      <EditInstanceConfigDialog
        instanceId={7}
        instanceName="docker-mc"
        nodeId={1}
        jdkId={0}
        startCommand="java -jar server.jar nogui"
        autoRestart={false}
        onClose={onClose}
      />,
    )

    const heading = await screen.findByRole('heading', { name: '编辑实例配置 · docker-mc' })
    expect(heading.closest('[role="dialog"]')).toBeInTheDocument()

    await user.keyboard('{Escape}')

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  })
})
