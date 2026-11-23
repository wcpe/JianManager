import { describe, it, expect, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { renderWithProviders } from '@/test/render'
import InstanceTagsDialog from './InstanceTagsDialog'

describe('InstanceTagsDialog（共享 Dialog）', () => {
  it('具备共享 Dialog 语义并支持 Esc 关闭', async () => {
    const onClose = vi.fn()
    const user = userEvent.setup()

    renderWithProviders(
      <InstanceTagsDialog
        instanceId={42}
        instanceName="survival-x"
        tags={['env:dev', 'survival']}
        onClose={onClose}
      />,
    )

    const heading = await screen.findByRole('heading', { name: '编辑标签 — survival-x' })
    expect(heading.closest('[role="dialog"]')).toBeInTheDocument()

    await user.keyboard('{Escape}')

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  })
})
