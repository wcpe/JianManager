import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { RangePicker } from './RangePicker'

describe('RangePicker', () => {
  it('使用统一工具条与按钮控件渲染时间范围', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()

    render(<RangePicker value="24h" onChange={onChange} />)

    expect(screen.getByRole('tablist')).toHaveClass('jm-toolbar-surface')
    expect(screen.getByRole('tab', { name: '24h' })).toHaveAttribute('data-slot', 'button')
    expect(screen.getByRole('tab', { name: '1y' })).toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: '7d' }))
    expect(onChange).toHaveBeenCalledWith('7d')
  })
})
