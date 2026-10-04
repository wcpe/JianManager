import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { Panel } from './panel'

/**
 * Panel 现状快照测试（FR-496 阶段 0）。
 *
 * Panel 是全站卡片原语（FR-061 / FR-163），几乎所有页面都在用它，而它的对外契约只有两条：
 * ① **标题栏要不要出现**——title / icon / actions 三者全空时必须不产生标题栏节点；
 * ② **内容区怎么透传**——children 恒定落在一个独立容器里，className / bodyClassName 是追加而非替换。
 * 这两点恰好是重构时最容易静默改掉的（比如把条件渲染改成"永远渲染一个空 header"、或把
 * bodyClassName 当整体 className 覆盖），所以这里把 DOM 槽位顺序、`data-*` 钩子与属性转发钉死。
 *
 * 样式类名只做弱断言（断言"包含某个布局类"而非完整类串），阶段 4 重做样式时不应被误伤。
 */
describe('Panel', () => {
  it('无标题栏时只渲染内容区', () => {
    render(<Panel>纯内容</Panel>)

    const root = screen.getByText('纯内容').closest('[data-slot="panel"]') as HTMLElement
    expect(root).not.toBeNull()
    // title / actions / icon 全空 ⇒ 只有内容区一个子节点，不产生多余的空标题栏
    expect(root.children).toHaveLength(1)
    expect(root.firstElementChild).toHaveTextContent('纯内容')
  })

  it('有标题时标题栏排在内容区之前', () => {
    render(<Panel title="实例概览">运行中 3 台</Panel>)

    const root = screen.getByText('实例概览').closest('[data-slot="panel"]') as HTMLElement
    expect(root.children).toHaveLength(2)
    expect(root.children[0]).toHaveTextContent('实例概览')
    expect(root.children[1]).toHaveTextContent('运行中 3 台')
  })

  it('图标块在标题左侧，actions 待在标题栏而非内容区', () => {
    render(
      <Panel
        title="节点健康"
        icon={<span data-testid="panel-icon">★</span>}
        actions={<button type="button">刷新</button>}
      >
        内容
      </Panel>,
    )

    const chip = screen.getByTestId('panel-icon').parentElement as HTMLElement
    expect(chip.tagName).toBe('SPAN')
    // 图标块与标题是兄弟且图标在前——标题栏内部顺序是重构极易丢掉的结构契约
    expect(chip.nextElementSibling).toBe(screen.getByText('节点健康'))

    const action = screen.getByRole('button', { name: '刷新' })
    expect(screen.getByText('内容')).not.toContainElement(action)
    expect(action.closest('[data-slot="panel"]')).not.toBeNull()
  })

  it('className 与 bodyClassName 是追加而非替换', () => {
    render(
      <Panel className="custom-panel" bodyClassName="custom-body">
        内容
      </Panel>,
    )

    const root = screen.getByText('内容').closest('[data-slot="panel"]') as HTMLElement
    expect(root.className).toContain('custom-panel')
    expect(root.className).toContain('flex')

    // 内容区的类名合并语义：调用方类名追加在默认类之后，默认布局类不被清空
    const body = screen.getByText('内容')
    expect(body.className).toContain('custom-body')
    expect(body.className).toContain('flex-1')
  })

  it('转发原生 div 属性与事件处理器', () => {
    const onClick = vi.fn()
    render(
      <Panel id="instances-panel" data-testid="panel-root" aria-label="实例面板" onClick={onClick}>
        内容
      </Panel>,
    )

    const root = screen.getByTestId('panel-root')
    expect(root).toHaveAttribute('id', 'instances-panel')
    expect(root).toHaveAttribute('aria-label', '实例面板')
    expect(root).toHaveAttribute('data-slot', 'panel')

    fireEvent.click(root)
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('tone 参与图标块取色但不改变结构', () => {
    const { unmount } = render(
      <Panel icon={<span data-testid="panel-icon">!</span>} tone="danger">
        内容
      </Panel>,
    )
    const dangerChip = screen.getByTestId('panel-icon').parentElement as HTMLElement
    expect(dangerChip.tagName).toBe('SPAN')
    const dangerClass = dangerChip.className
    unmount()

    render(
      <Panel icon={<span data-testid="panel-icon">!</span>} tone="primary">
        内容
      </Panel>,
    )
    const primaryChip = screen.getByTestId('panel-icon').parentElement as HTMLElement

    // 不钉死具体 token 类名（属于样式实现，重构会改），只锁定"tone 确实参与取色"这一契约
    expect(primaryChip.className).not.toBe(dangerClass)
  })

  it('hoverable 只加外观反馈类，不改变节点结构', () => {
    render(<Panel hoverable>内容</Panel>)

    const root = screen.getByText('内容').closest('[data-slot="panel"]') as HTMLElement
    expect(root.children).toHaveLength(1)
    // FR-176：hover 只换阴影不抬位，对外可见结果是 shadow-lift（弱断言，不锁完整类串）
    expect(root.className).toContain('shadow-lift')
  })
})

/**
 * FR-496 阶段 6 新增契约：三段式结构钩子与标度收敛。
 *
 * 结构钩子是页面迁移时的抓手（页面按 panel-header / panel-body / panel-footer 定位，
 * 不再靠 :first-child 猜），标度收敛则是「换主题时样式跟着变」的前提。
 */
describe('Panel 三段式钩子与标度收敛（FR-496 阶段 6）', () => {
  it('标题栏 / 内容区 / 底部区各自带 data-slot 钩子', () => {
    render(
      <Panel title="实例概览" footer={<span>共 3 条</span>}>
        运行中 3 台
      </Panel>,
    )

    const root = screen.getByText('运行中 3 台').closest('[data-slot="panel"]') as HTMLElement
    const header = root.querySelector('[data-slot="panel-header"]')
    const body = root.querySelector('[data-slot="panel-body"]')
    const footer = root.querySelector('[data-slot="panel-footer"]')

    expect(header).not.toBeNull()
    expect(body).not.toBeNull()
    expect(footer).not.toBeNull()
    expect(header).toContainElement(screen.getByText('实例概览'))
    expect(body).toHaveTextContent('运行中 3 台')
    expect(footer).toHaveTextContent('共 3 条')
  })

  it('不给 footer 时不渲染底部区节点', () => {
    render(<Panel title="实例概览">内容</Panel>)

    const root = screen.getByText('内容').closest('[data-slot="panel"]') as HTMLElement
    expect(root.querySelector('[data-slot="panel-footer"]')).toBeNull()
    expect(root.children).toHaveLength(2)
  })

  it('hover 抬升的过渡仍绑 motion token（FR-244 收敛不丢）', () => {
    render(<Panel hoverable>内容</Panel>)

    const root = screen.getByText('内容').closest('[data-slot="panel"]') as HTMLElement
    expect(root.className).toContain('duration-[var(--motion-duration-slow)]')
    expect(root.className).toContain('ease-ios')
  })

  it('页脚间距与字号回到设计标度，不再用任意像素值', () => {
    render(<Panel footer="共 3 条">内容</Panel>)

    const footer = document.querySelector('[data-slot="panel-footer"]') as HTMLElement
    expect(footer.className).toContain('px-4')
    expect(footer.className).toContain('py-2.5')
    expect(footer.className).toContain('text-xs')
    // 任意值不会跟主题/标度联动，属收敛对象
    expect(footer.className).not.toMatch(/\[1[0-9]px\]/)
  })
})
