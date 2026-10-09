import { describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithI18n } from '@/test/i18n'
import BinarySegment, { LaunchParamsPanel } from '@/components/views/instances/BinarySegment'

/**
 * FR-450 §2.3.4 进程能力分段 · 受控视图测（ADR-097 b 范式）。
 *
 * 断言不依赖具体文案（按钮按角色取），进程指标块由 slot 注入、这里用一个标记元素代替，
 * 以证明「分段只负责把它放在启动参数上方」这一契约，而不引入 ProcessPanel 的取数。
 */
describe('BinarySegment / LaunchParamsPanel（FR-450 · ADR-097 b 范式）', () => {
  const launch = { startCommand: './paper.jar --nogui', loaded: true, saving: false, onSave: vi.fn() }

  it('进程指标块经 slot 注入，且位于启动参数区之前', () => {
    const { container } = renderWithI18n(
      <BinarySegment processSlot={<div data-testid="process-slot" />} launch={launch} />,
    )
    const slot = screen.getByTestId('process-slot')
    const params = screen.getByTestId('launch-params-panel')
    expect(slot).toBeInTheDocument()
    // 顺序：进程指标在前、启动参数在后。
    expect(slot.compareDocumentPosition(params) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(container).toBeTruthy()
  })

  it('草稿从注入的已持久化命令初始化，未改动时保存禁用', () => {
    renderWithI18n(<LaunchParamsPanel {...launch} />)
    const input = screen.getByRole('textbox') as HTMLInputElement
    expect(input.value).toBe('./paper.jar --nogui')
    expect(screen.getByRole('button')).toBeDisabled()
  })

  it('改动后保存可用，点击上报新命令', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn().mockResolvedValue(true)
    renderWithI18n(<LaunchParamsPanel {...launch} onSave={onSave} />)

    const input = screen.getByRole('textbox')
    await user.clear(input)
    await user.type(input, './server.jar')
    await user.click(screen.getByRole('button'))

    await waitFor(() => expect(onSave).toHaveBeenCalledWith('./server.jar'))
  })

  it('实例未加载（loaded=false）时即使草稿不同也不可保存', () => {
    renderWithI18n(<LaunchParamsPanel {...launch} startCommand="" loaded={false} />)
    expect(screen.getByRole('button')).toBeDisabled()
  })

  it('保存在途时按钮禁用', () => {
    renderWithI18n(<LaunchParamsPanel {...launch} saving />)
    expect(screen.getByRole('button')).toBeDisabled()
  })
})
