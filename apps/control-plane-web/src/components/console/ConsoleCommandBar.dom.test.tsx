import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { renderWithProviders } from '@/test/render'
import ConsoleCommandBar from './ConsoleCommandBar'

/**
 * 命令栏（FR-415，spec §2.2/§2.3；ADR-086）。
 *
 * 这些用例锁的是「弃 xterm 输入换原生 input」买到的东西：整行提交、readline 惯用键、
 * 输入法组合期不被打断。旧实现在 xterm `onData` 里手搓这些，全都是坏的。
 */

const label = '控制台命令输入'

function setup(props: Partial<React.ComponentProps<typeof ConsoleCommandBar>> = {}) {
  const onSubmit = vi.fn()
  const view = renderWithProviders(<ConsoleCommandBar onSubmit={onSubmit} {...props} />)
  const input = screen.getByRole('textbox', { name: label }) as HTMLInputElement
  return { onSubmit, input, view }
}

describe('ConsoleCommandBar 提交', () => {
  it('Enter 提交整行并清空输入框', async () => {
    const user = userEvent.setup()
    const { onSubmit, input } = setup()

    await user.type(input, 'say hello world{Enter}')

    // 整行一次提交，不是逐字符（旧实现每个字符都往 stdin 走一趟）。
    expect(onSubmit).toHaveBeenCalledTimes(1)
    expect(onSubmit).toHaveBeenCalledWith('say hello world')
    expect(input).toHaveValue('')
  })

  it('提交的行不含换行符（Worker 侧写 stdin 时自己补）', async () => {
    const user = userEvent.setup()
    const { onSubmit, input } = setup()
    await user.type(input, 'stop{Enter}')
    expect(onSubmit.mock.calls[0][0]).toBe('stop')
    expect(onSubmit.mock.calls[0][0]).not.toContain('\n')
  })

  it('空行与纯空白不提交', async () => {
    const user = userEvent.setup()
    const { onSubmit, input } = setup()
    await user.type(input, '{Enter}')
    await user.type(input, '   {Enter}')
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('「发送」按钮与 Enter 等价；空输入时按钮禁用', async () => {
    const user = userEvent.setup()
    const { onSubmit, input } = setup()
    const send = screen.getByRole('button', { name: '发送' })
    expect(send).toBeDisabled()

    await user.type(input, 'list')
    expect(send).toBeEnabled()
    await user.click(send)
    expect(onSubmit).toHaveBeenCalledWith('list')
  })
})

describe('ConsoleCommandBar 输入法组合期（spec §2.3：中文输入无乱码、无重复回显）', () => {
  it('组合中的 Enter 是「确认候选词」，不得被当成提交', () => {
    const { onSubmit, input } = setup()

    // 模拟中文输入法：组合开始 → 候选未定 → 按 Enter 选词。
    fireEvent.compositionStart(input)
    fireEvent.change(input, { target: { value: 'say 大' } })
    // isComposing 为 true 的 keydown 必须被放行给输入法。
    fireEvent.keyDown(input, { key: 'Enter', isComposing: true })
    expect(onSubmit).not.toHaveBeenCalled()

    // 选词结束后再按 Enter 才提交，且值就是输入法上屏的完整文本（不重复、不乱码）。
    fireEvent.compositionEnd(input)
    fireEvent.change(input, { target: { value: 'say 大家好' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSubmit).toHaveBeenCalledTimes(1)
    expect(onSubmit).toHaveBeenCalledWith('say 大家好')
  })

  it('组合期的 ↑ 不被历史导航劫持（输入法用 ↑↓ 翻候选页）', () => {
    const { input } = setup({ history: ['earlier-command'] })
    fireEvent.change(input, { target: { value: 'shu' } })
    fireEvent.compositionStart(input)
    fireEvent.keyDown(input, { key: 'ArrowUp', isComposing: true })
    expect(input).toHaveValue('shu')
  })
})

describe('ConsoleCommandBar readline 惯用键（ADR-086 点名的失效键）', () => {
  it('Ctrl+A 移到行首、Ctrl+E 移到行尾（非浏览器默认的全选/无动作）', async () => {
    const user = userEvent.setup()
    const { input } = setup()
    await user.type(input, 'gamemode creative')

    fireEvent.keyDown(input, { key: 'a', ctrlKey: true })
    expect(input.selectionStart).toBe(0)
    expect(input.selectionEnd).toBe(0)

    fireEvent.keyDown(input, { key: 'e', ctrlKey: true })
    expect(input.selectionStart).toBe('gamemode creative'.length)
  })

  it('Ctrl+U 删到行首（只删光标之前）', async () => {
    const user = userEvent.setup()
    const { input } = setup()
    await user.type(input, 'say hello')
    input.setSelectionRange(4, 4)

    fireEvent.keyDown(input, { key: 'u', ctrlKey: true })
    expect(input).toHaveValue('hello')
    expect(input.selectionStart).toBe(0)
  })

  it('Ctrl+W 往前删一个词，连按逐词后退', async () => {
    const user = userEvent.setup()
    const { input } = setup()
    await user.type(input, 'tp Steve 100 64 100')

    fireEvent.keyDown(input, { key: 'w', ctrlKey: true })
    expect(input).toHaveValue('tp Steve 100 64 ')
    fireEvent.keyDown(input, { key: 'w', ctrlKey: true })
    expect(input).toHaveValue('tp Steve 100 ')
  })

  it('Ctrl+Backspace 与 Alt+Backspace 同为删词（Ctrl+W 被浏览器保留时的可用替代）', async () => {
    const user = userEvent.setup()
    const { input } = setup()
    await user.type(input, 'give Steve stone')

    fireEvent.keyDown(input, { key: 'Backspace', ctrlKey: true })
    expect(input).toHaveValue('give Steve ')

    fireEvent.keyDown(input, { key: 'Backspace', altKey: true })
    expect(input).toHaveValue('give ')
  })

  it('删词从光标位置生效，不动光标之后的内容', async () => {
    const user = userEvent.setup()
    const { input } = setup()
    await user.type(input, 'say alpha beta')
    input.setSelectionRange(9, 9) // 'say alpha| beta'

    fireEvent.keyDown(input, { key: 'w', ctrlKey: true })
    expect(input).toHaveValue('say  beta')
    expect(input.selectionStart).toBe(4)
  })
})

describe('ConsoleCommandBar 命令历史（内存内；持久化/^R 属 FR-416）', () => {
  it('↑ 回溯到最近一条，再 ↑ 到更早，↓ 回到草稿', () => {
    const { input } = setup({ history: ['first', 'second'] })
    fireEvent.change(input, { target: { value: 'draft' } })

    fireEvent.keyDown(input, { key: 'ArrowUp' })
    expect(input).toHaveValue('second')
    fireEvent.keyDown(input, { key: 'ArrowUp' })
    expect(input).toHaveValue('first')
    // 已到最早，继续 ↑ 不动。
    fireEvent.keyDown(input, { key: 'ArrowUp' })
    expect(input).toHaveValue('first')

    fireEvent.keyDown(input, { key: 'ArrowDown' })
    expect(input).toHaveValue('second')
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    // 回到草稿——草稿不能在历史导航中丢掉。
    expect(input).toHaveValue('draft')
  })

  it('历史为空时 ↑ 不改变输入', () => {
    const { input } = setup()
    fireEvent.change(input, { target: { value: 'typed' } })
    fireEvent.keyDown(input, { key: 'ArrowUp' })
    expect(input).toHaveValue('typed')
  })
})

describe('ConsoleCommandBar 非 RUNNING 禁用（spec §2.2/§2.3）', () => {
  it('disabled 时输入框与发送按钮均禁用，且给出原因与直达动作', () => {
    const onStart = vi.fn()
    setup({
      disabled: true,
      disabledReason: '实例未运行（CRASHED），命令输入已禁用',
      action: (
        <button type="button" onClick={onStart}>
          启动实例
        </button>
      ),
    })

    expect(screen.getByRole('textbox', { name: label })).toBeDisabled()
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled()
    // 原因与动作必须同时在场：只禁用不解释等于让用户猜。
    expect(screen.getByText('实例未运行（CRASHED），命令输入已禁用')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '启动实例' }))
    expect(onStart).toHaveBeenCalledTimes(1)
  })

  it('disabled 时敲不进字符、也提交不出去', async () => {
    const user = userEvent.setup()
    const { onSubmit, input } = setup({ disabled: true, disabledReason: 'x' })

    await user.type(input, 'stop{Enter}')

    expect(input).toHaveValue('')
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('未禁用时不显示原因行', () => {
    setup({ disabledReason: '不该出现' })
    expect(screen.queryByText('不该出现')).not.toBeInTheDocument()
  })
})
