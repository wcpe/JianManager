import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import { PageHelp } from './PageHelp'

/**
 * PageHelp 是「点击问号展开分节说明」的可复用帮助浮层。
 *
 * 断言重点不是文案，而是**结构契约**：触发器存在且可键盘可达、浮层按节渲染、
 * 列表与段落两种内容形式都能装。后续其他页面靠这些契约做集成测试。
 */
describe('PageHelp 帮助浮层', () => {
  it('渲染问号触发器，默认不展开浮层', () => {
    render(
      <PageHelp
        testId="t"
        title="页面说明"
        sections={[{ title: '第一节', body: '正文' }]}
      />,
    )
    const trigger = screen.getByTestId('t-trigger')
    expect(trigger).toBeInTheDocument()
    // 浮层未打开时内容不在 DOM（Popover 走 portal + 条件渲染）。
    expect(screen.queryByTestId('t-content')).not.toBeInTheDocument()
  })

  it('点击后展开，段落与列表两种内容都渲染', async () => {
    const user = userEvent.setup()
    render(
      <PageHelp
        testId="t"
        title="页面说明"
        intro="一句话导语"
        sections={[
          { title: '有段落', body: '段内容' },
          { title: '有列表', items: ['条目一', '条目二'] },
        ]}
      />,
    )
    await user.click(screen.getByTestId('t-trigger'))
    const content = await screen.findByTestId('t-content')
    expect(content).toHaveTextContent('页面说明')
    expect(content).toHaveTextContent('一句话导语')
    expect(screen.getByTestId('t-section-0')).toHaveTextContent('段内容')
    // 列表节必须真的是列表：条目数量可被断言。
    const second = screen.getByTestId('t-section-1')
    expect(second.querySelectorAll('li')).toHaveLength(2)
  })

  it('触发器可键盘聚焦（帮助不是鼠标专属）', async () => {
    const user = userEvent.setup()
    render(<PageHelp testId="t" title="页面说明" sections={[{ title: 's', body: 'b' }]} />)
    await user.tab()
    expect(screen.getByTestId('t-trigger')).toHaveFocus()
    // 回车能展开（Enter 在 button 上触发 click）。
    await user.keyboard('{Enter}')
    expect(await screen.findByTestId('t-content')).toBeInTheDocument()
  })

  it('无 intro 时不渲染空段落', async () => {
    const user = userEvent.setup()
    render(<PageHelp testId="t" title="页面说明" sections={[{ title: 's', body: 'b' }]} />)
    await user.click(screen.getByTestId('t-trigger'))
    const content = await screen.findByTestId('t-content')
    // 只有标题与一节内容，没有多余的空白 <p>。
    expect(content.querySelectorAll(':scope > div > p')).toHaveLength(0)
  })
})
