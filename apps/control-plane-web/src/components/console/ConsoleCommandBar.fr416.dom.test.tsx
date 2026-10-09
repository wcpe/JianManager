import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { clearCommandHistory, loadCommandHistory, pushCommandHistory } from '@/lib/console-command-history'
import { renderWithProviders } from '@/test/render'
import ConsoleCommandBar from './ConsoleCommandBar'
import InstanceConsoleView from './InstanceConsoleView'

/**
 * 命令栏智能输入（FR-416）：历史持久化 / `^R` 模糊搜索 / 候选式 Tab 补全 / 多行粘贴保护。
 *
 * FR-415 把 Tab 补全随旧 xterm 输入路径删掉了（ADR-086「已知回退」），这些用例是补回后的锁。
 */

const LABEL = '控制台命令输入'
const PLAYERS = ['Steve', 'Stevie', 'Alex']

function setup(props: Partial<React.ComponentProps<typeof ConsoleCommandBar>> = {}) {
  const onSubmit = vi.fn()
  renderWithProviders(<ConsoleCommandBar onSubmit={onSubmit} {...props} />)
  const input = screen.getByRole('textbox', { name: LABEL }) as HTMLInputElement
  return { onSubmit, input }
}

/** 造一次真实的 paste 事件（userEvent.paste 不带 clipboardData 的多行文本控制权）。 */
function pasteText(input: HTMLInputElement, text: string) {
  input.focus()
  fireEvent.paste(input, { clipboardData: { getData: () => text } })
}

const candidateList = () => screen.queryByRole('listbox', { name: '命令补全候选' })
const historyList = () => screen.queryByRole('listbox', { name: '历史命令匹配结果' })

describe('FR-416 Tab 补全：候选式，不盲补第一个', () => {
  it('多候选：只推进到公共前缀并列出候选，输入不等于任何单个候选', async () => {
    const user = userEvent.setup()
    const { input } = setup({ players: PLAYERS })

    await user.type(input, 'sav')
    fireEvent.keyDown(input, { key: 'Tab' })

    // 'save-all' / 'save-off' / 'save-on' 的公共前缀是 'save-'——停在这里。
    expect(input).toHaveValue('save-')
    // 关键反面断言：**没有**变成第一个候选。
    expect(input).not.toHaveValue('save-all')

    const list = candidateList()
    expect(list).toBeInTheDocument()
    expect(within(list!).getAllByRole('option').map((o) => o.textContent)).toEqual([
      'save-all',
      'save-off',
      'save-on',
    ])
  })

  it('唯一候选：直接补全（无歧义不算盲补），且不留候选列表', async () => {
    const user = userEvent.setup()
    const { input } = setup()

    await user.type(input, 'stopso')
    fireEvent.keyDown(input, { key: 'Tab' })

    expect(input).toHaveValue('stopsound ')
    expect(candidateList()).not.toBeInTheDocument()
  })

  it('无候选时 Tab 交还浏览器（不吞无障碍键）', async () => {
    const user = userEvent.setup()
    const { input } = setup()
    await user.type(input, 'zzzz')

    // fireEvent 返回 !defaultPrevented：true 即事件被放行给浏览器默认行为（移焦点）。
    expect(fireEvent.keyDown(input, { key: 'Tab' })).toBe(true)
    expect(input).toHaveValue('zzzz')
  })

  it('↑↓ 在候选间移动、Enter 采用高亮候选而不是提交命令', async () => {
    const user = userEvent.setup()
    const { onSubmit, input } = setup()

    await user.type(input, 'sav')
    fireEvent.keyDown(input, { key: 'Tab' })
    fireEvent.keyDown(input, { key: 'ArrowDown' })

    const options = within(candidateList()!).getAllByRole('option')
    expect(options[1]).toHaveAttribute('aria-selected', 'true')

    fireEvent.keyDown(input, { key: 'Enter' })
    // Enter 采用候选，**不**提交——否则用户刚打开候选就误发一条半截命令。
    expect(onSubmit).not.toHaveBeenCalled()
    expect(input).toHaveValue('save-off ')
    expect(candidateList()).not.toBeInTheDocument()
  })

  it('Esc 关候选后 Enter 才提交', async () => {
    const user = userEvent.setup()
    const { onSubmit, input } = setup()

    await user.type(input, 'sav')
    fireEvent.keyDown(input, { key: 'Tab' })
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(candidateList()).not.toBeInTheDocument()

    fireEvent.keyDown(input, { key: 'Enter' })
    expect(onSubmit).toHaveBeenCalledWith('save-')
  })

  it('连按 Tab 在候选间下移（显式动作），输入仍不被改写', async () => {
    const user = userEvent.setup()
    const { input } = setup()

    await user.type(input, 'sav')
    fireEvent.keyDown(input, { key: 'Tab' })
    fireEvent.keyDown(input, { key: 'Tab' })

    expect(within(candidateList()!).getAllByRole('option')[1]).toHaveAttribute('aria-selected', 'true')
    expect(input).toHaveValue('save-')
  })

  it('第二段补真实在线玩家名，且不编造：无玩家时无候选', async () => {
    const user = userEvent.setup()
    const { input } = setup({ players: PLAYERS })

    await user.type(input, 'op ')
    fireEvent.keyDown(input, { key: 'Tab' })
    expect(within(candidateList()!).getAllByRole('option').map((o) => o.textContent)).toEqual(PLAYERS)
  })

  it('无在线玩家时第二段不给任何候选（不硬编码假玩家名）', async () => {
    const user = userEvent.setup()
    const { input } = setup({ players: [] })

    await user.type(input, 'op ')
    fireEvent.keyDown(input, { key: 'Tab' })
    expect(candidateList()).not.toBeInTheDocument()
    expect(input).toHaveValue('op ')
  })

  it('玩家名补全纠正大小写并停在公共前缀（MC 玩家名大小写敏感）', async () => {
    const user = userEvent.setup()
    const { input } = setup({ players: PLAYERS })

    await user.type(input, 'op st')
    fireEvent.keyDown(input, { key: 'Tab' })
    // Steve / Stevie 的公共前缀 'Stev'——不是 'Steve'，也不是用户敲的 'stev'。
    expect(input).toHaveValue('op Stev')
  })

  it('ghost 预览显示高亮候选的剩余部分', async () => {
    const user = userEvent.setup()
    const { input } = setup()

    await user.type(input, 'sto')
    fireEvent.keyDown(input, { key: 'Tab' })
    // 'stop' / 'stopsound' 公共前缀为 'stop'，推进后高亮首候选 'stop'，ghost 为空；
    // ↓ 到 'stopsound' 后 ghost 显示剩余的 'sound'。
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    expect(screen.getByTestId('console-completion-ghost')).toHaveTextContent('sound')
  })

  it('点击候选项即采用', async () => {
    const user = userEvent.setup()
    const { input } = setup()

    await user.type(input, 'sav')
    fireEvent.keyDown(input, { key: 'Tab' })
    await user.click(within(candidateList()!).getByRole('option', { name: 'save-on' }))

    expect(input).toHaveValue('save-on ')
  })
})

describe('FR-416 ^R 历史模糊搜索', () => {
  const history = ['say hello', 'gamemode creative Steve', 'list', 'op Steve']

  it('Ctrl+R 打开搜索浮层并阻止浏览器刷新', () => {
    const { input } = setup({ history })
    // Ctrl+R 是刷新键，必须被拦下，否则一按就整页重载、命令栏内容全丢。
    // fireEvent 返回 false 即 preventDefault 已调用（且 fireEvent 包了 act，状态会 flush）。
    expect(fireEvent.keyDown(input, { key: 'r', ctrlKey: true })).toBe(false)
    expect(historyList()).toBeInTheDocument()
  })

  it('模糊命中：子序列匹配（gmc → gamemode creative ...）', async () => {
    const user = userEvent.setup()
    const { input } = setup({ history })
    fireEvent.keyDown(input, { key: 'r', ctrlKey: true })

    const search = screen.getByRole('textbox', { name: '历史命令模糊搜索' })
    await user.type(search, 'gmc')

    expect(within(historyList()!).getAllByRole('option').map((o) => o.textContent)).toEqual([
      'gamemode creative Steve',
    ])
    expect(screen.getByText('1 条匹配')).toBeInTheDocument()
  })

  it('↑↓ 选择、Enter 填入命令栏（不直接提交——找到的命令常要改参数）', async () => {
    const user = userEvent.setup()
    const { onSubmit, input } = setup({ history })
    fireEvent.keyDown(input, { key: 'r', ctrlKey: true })

    const search = screen.getByRole('textbox', { name: '历史命令模糊搜索' })
    await user.type(search, 'e')
    // 新→旧：第 0 项是最新的命中，↓ 到第 1 项。
    fireEvent.keyDown(search, { key: 'ArrowDown' })
    const options = within(historyList()!).getAllByRole('option')
    expect(options[1]).toHaveAttribute('aria-selected', 'true')
    const picked = options[1].textContent

    fireEvent.keyDown(search, { key: 'Enter' })
    expect(historyList()).not.toBeInTheDocument()
    expect(input).toHaveValue(picked)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('Esc 取消：浮层关闭且输入框不被改动', async () => {
    const user = userEvent.setup()
    const { input } = setup({ history })
    await user.type(input, 'draft')
    fireEvent.keyDown(input, { key: 'r', ctrlKey: true })

    const search = screen.getByRole('textbox', { name: '历史命令模糊搜索' })
    await user.type(search, 'say')
    fireEvent.keyDown(search, { key: 'Escape' })

    expect(historyList()).not.toBeInTheDocument()
    expect(input).toHaveValue('draft')
  })

  it('无命中给明确空态而非空白浮层', async () => {
    const user = userEvent.setup()
    const { input } = setup({ history })
    fireEvent.keyDown(input, { key: 'r', ctrlKey: true })
    await user.type(screen.getByRole('textbox', { name: '历史命令模糊搜索' }), 'zzz')

    expect(screen.getByText('无匹配历史')).toBeInTheDocument()
    expect(screen.getByText('0 条匹配')).toBeInTheDocument()
  })

  it('点击命中项即填入', async () => {
    const user = userEvent.setup()
    const { input } = setup({ history })
    fireEvent.keyDown(input, { key: 'r', ctrlKey: true })
    await user.click(within(historyList()!).getByRole('option', { name: 'list' }))
    expect(input).toHaveValue('list')
  })
})

describe('FR-416 多行粘贴保护', () => {
  const TWO_LINES = 'say first\nsay second'

  it('粘贴 ≥2 行弹确认，不一股脑连发', () => {
    const { onSubmit, input } = setup()
    pasteText(input, TWO_LINES)

    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText('粘贴了多行内容')).toBeInTheDocument()
    // 弹窗期间一条都不许发出去。
    expect(onSubmit).not.toHaveBeenCalled()
    expect(input).toHaveValue('')
  })

  it('分支①逐行发送：按顺序逐行提交', async () => {
    const user = userEvent.setup()
    const { onSubmit, input } = setup()
    pasteText(input, `${TWO_LINES}\nsay third`)

    await user.click(screen.getByRole('button', { name: '逐行发送（3 行）' }))

    expect(onSubmit.mock.calls.map((c) => c[0])).toEqual(['say first', 'say second', 'say third'])
    expect(input).toHaveValue('')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('分支②仅取第一行：填入输入框且不提交', async () => {
    const user = userEvent.setup()
    const { onSubmit, input } = setup()
    pasteText(input, TWO_LINES)

    await user.click(screen.getByRole('button', { name: '仅取第一行' }))

    expect(input).toHaveValue('say first')
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('分支③取消：什么都不发、输入框不变', async () => {
    const user = userEvent.setup()
    const { onSubmit, input } = setup()
    pasteText(input, TWO_LINES)

    await user.click(screen.getByRole('button', { name: '取消' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(onSubmit).not.toHaveBeenCalled()
    expect(input).toHaveValue('')
  })

  it('单行粘贴放行给浏览器原生路径（不弹窗）——readText 在 HTTP 下不可用，不能接管', () => {
    const { input } = setup()
    const event = new Event('paste', { bubbles: true, cancelable: true })
    Object.defineProperty(event, 'clipboardData', { value: { getData: () => 'say only-one' } })
    input.dispatchEvent(event)

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(event.defaultPrevented).toBe(false)
  })

  it('带尾随换行的单行仍算一行（从别处复制的命令常带 \\n）', () => {
    const { input } = setup()
    pasteText(input, 'stop\n')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('中间空行不算一行，也不会被当命令发出去', async () => {
    const user = userEvent.setup()
    const { onSubmit, input } = setup()
    pasteText(input, 'say a\n\n\nsay b\n')

    expect(screen.getByText('共 2 行。', { exact: false })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '逐行发送（2 行）' }))
    expect(onSubmit.mock.calls.map((c) => c[0])).toEqual(['say a', 'say b'])
  })
})

describe('FR-416 历史按实例持久化（刷新后仍在）', () => {
  const INSTANCE = 4242

  beforeEach(() => {
    clearCommandHistory(INSTANCE)
  })

  afterEach(() => {
    clearCommandHistory(INSTANCE)
  })

  it('已持久化的历史在组件首次挂载即可用（等价于刷新后 ↑ 就能翻到）', () => {
    // 预置存储 = 上一次会话留下的历史；本次「刷新」后组件全新挂载。
    pushCommandHistory(INSTANCE, 'say from-last-session')
    pushCommandHistory(INSTANCE, 'list')

    renderWithProviders(
      <InstanceConsoleView instanceId={INSTANCE} isLoading={false} readOnly />,
    )

    const input = screen.getByRole('textbox', { name: LABEL }) as HTMLInputElement
    // 历史抽屉列出持久化内容（命令栏被 readOnly 禁用，↑ 走不通，故断言渲染出的历史）。
    fireEvent.click(screen.getByRole('button', { name: '历史' }))
    expect(screen.getByRole('button', { name: 'list' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'say from-last-session' })).toBeInTheDocument()
    expect(input).toBeDisabled()
  })

  it('未持久化过的实例首挂载历史为空（不串到别的实例）', () => {
    pushCommandHistory(INSTANCE, 'only-for-4242')

    renderWithProviders(<InstanceConsoleView instanceId={9999} isLoading={false} readOnly />)
    fireEvent.click(screen.getByRole('button', { name: '历史' }))

    expect(screen.getByText('暂无历史')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'only-for-4242' })).not.toBeInTheDocument()
    // 4242 的历史没被清掉，只是没被读进 9999。
    expect(loadCommandHistory(INSTANCE)).toEqual(['only-for-4242'])
  })
})
