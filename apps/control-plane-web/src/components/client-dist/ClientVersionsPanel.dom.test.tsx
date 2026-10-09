import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import ClientVersionsPanel from './ClientVersionsPanel'

describe('ClientVersionsPanel（FR-352）', () => {
  it('标题区展示内嵌更新器旁路摘要', async () => {
    loginMockUser()
    renderWithProviders(<ClientVersionsPanel channelId="skyblock-s1" />)

    expect(await screen.findByTestId('embedded-updater-summary')).toHaveTextContent('内嵌更新器 v0.9.0 · core 3')
  })
})
