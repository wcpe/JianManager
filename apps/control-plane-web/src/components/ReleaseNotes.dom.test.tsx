import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { ReleaseNotes } from './ReleaseNotes'

describe('ReleaseNotes 外链确认', () => {
  const openSpy = vi.fn()

  beforeEach(() => {
    vi.stubGlobal('open', openSpy)
    openSpy.mockReset()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('点击安全外链先打开共享 Dialog，确认后新标签打开', async () => {
    const user = userEvent.setup()
    const href = 'https://github.com/wcpe/JianManager/releases'
    renderWithProviders(<ReleaseNotes markdown={`[release](${href})`} />)

    await user.click(screen.getByRole('link', { name: 'release' }))

    const dialog = await screen.findByRole('dialog', { name: '打开外部链接？' })
    expect(dialog).toHaveTextContent(href)
    expect(openSpy).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: '打开' }))

    expect(openSpy).toHaveBeenCalledWith(href, '_blank', 'noopener,noreferrer')
    expect(screen.queryByRole('dialog', { name: '打开外部链接？' })).not.toBeInTheDocument()
  })

  it('危险 scheme 不打开确认弹窗', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ReleaseNotes markdown="[bad](javascript:alert(1))" />)

    await user.click(screen.getByText('bad'))

    expect(screen.queryByRole('dialog', { name: '打开外部链接？' })).not.toBeInTheDocument()
    expect(openSpy).not.toHaveBeenCalled()
  })
})
