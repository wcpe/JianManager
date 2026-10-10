import * as React from 'react'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { CommandPalette, type CommandPaletteItem } from './CommandPalette'
import { ObjectPageHeader } from './ObjectPageHeader'

/**
 * 外壳补充件测试（FR-496 阶段 4）：命令面板 + 对象页头。
 *
 * 与阶段 3 的布局测试同一条思路——断言落在**结构性契约**上，不碰样式类名：
 * 命令面板锁的是「受控开合、键盘模型、检索口径（原页面名与原路由都能查到）」；
 * 对象页头锁的是「实例与节点共用同一套结构、单级面包屑不渲染、四段内容各司其职」。
 * 这些契约一旦被改坏，外壳重构就会静默退回到「侧栏塞满路由」之前的旧问题。
 */

/**
 * 首个条目（默认高亮项）。
 * 「执行当前高亮项」「鼠标点击结果」两个用例需要在保留原字段的前提下换掉 onSelect，
 * 具名常量让它们不必按 `ITEMS[0]` 取下标。
 */
const FIRST_ITEM: CommandPaletteItem = {
  id: 'page-instances',
  label: '全部实例',
  group: '页面',
  hint: '/instances',
  keywords: ['实例列表'],
  onSelect: () => {},
}

/** 检索用的样例条目：标签两两不同，避免用例里出现歧义定位。 */
const ITEMS: CommandPaletteItem[] = [
  FIRST_ITEM,
  {
    id: 'page-nodes',
    label: '节点管理',
    group: '页面',
    hint: '/nodes',
    keywords: ['机器清单'],
    onSelect: () => {},
  },
  {
    id: 'inst-srv',
    label: 'srv-0001',
    group: '实例',
    hint: 'node-main · 25565',
    keywords: ['/instances/42', '后端服'],
    onSelect: () => {},
  },
  {
    id: 'node-main',
    label: 'node-main',
    group: '节点',
    hint: '10.0.0.1',
    keywords: ['/nodes/main', '主控机'],
    onSelect: () => {},
  },
]

/**
 * 受控开合用具。
 *
 * 命令面板自己不持有 open，所以用例必须提供一个真实的状态宿主，
 * 否则「执行结果后关闭」「Esc 后关闭」这类断言只能看到回调被调用，
 * 看不到面板是否真的从 DOM 上消失。
 */
function PaletteHarness({
  items = ITEMS,
  initiallyOpen = true,
  onOpenChange,
}: {
  items?: CommandPaletteItem[]
  initiallyOpen?: boolean
  onOpenChange?: (open: boolean) => void
}) {
  const [open, setOpen] = React.useState(initiallyOpen)
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        打开命令面板
      </button>
      <CommandPalette
        open={open}
        onOpenChange={(next) => {
          onOpenChange?.(next)
          setOpen(next)
        }}
        items={items}
      />
    </>
  )
}

/** 当前高亮的选项（键盘模型的可观察结果）。 */
function activeOption() {
  return screen.getAllByRole('option').find((el) => el.getAttribute('aria-selected') === 'true')
}

/** 输入检索词。 */
function type(value: string) {
  fireEvent.change(screen.getByRole('combobox'), { target: { value } })
}

describe('CommandPalette 命令面板', () => {
  it('关闭态不渲染任何面板内容，且挂在门户而非渲染容器里', async () => {
    const { container } = render(<PaletteHarness initiallyOpen={false} />)

    expect(screen.queryByRole('listbox')).toBeNull()
    expect(document.querySelector('[data-slot="command-palette"]')).toBeNull()

    // 打开前触发器可被正常定位（打开后 Radix 会把门户之外的兄弟节点标为 aria-hidden）
    fireEvent.click(screen.getByRole('button', { name: '打开命令面板' }))
    await screen.findByRole('listbox')

    // 内容被 Portal 送到 body：渲染容器里查不到它，文档体里能查到
    expect(container.querySelector('[data-slot="command-palette"]')).toBeNull()
    expect(document.body).toContainElement(document.querySelector('[data-slot="command-palette"]'))
    expect(document.querySelector('[data-slot="command-palette-overlay"]')).not.toBeNull()
  })

  it('结构钩子齐全，打开即把焦点交给检索框', async () => {
    render(<PaletteHarness />)
    const list = await screen.findByRole('listbox')

    expect(document.querySelector('[data-slot="command-palette"]')).toHaveAttribute(
      'data-slot',
      'command-palette',
    )
    expect(list).toHaveAttribute('data-slot', 'command-palette-list')
    expect(screen.getByRole('combobox')).toHaveAttribute('data-slot', 'command-palette-input')
    expect(document.querySelector('[data-slot="command-palette-footer"]')).not.toBeNull()

    // ⌘K 呼出后应当直接可以打字，而不是先按一次 Tab
    expect(screen.getByRole('combobox')).toHaveFocus()
  })

  it('结果按 group 分组渲染，分组顺序跟随 items 顺序', async () => {
    render(<PaletteHarness />)
    await screen.findByRole('listbox')

    const headers = Array.from(
      document.querySelectorAll('[data-slot="command-palette-group"]'),
    ).map((el) => el.textContent)
    expect(headers).toEqual(['页面', '实例', '节点'])

    expect(screen.getAllByRole('option')).toHaveLength(ITEMS.length)
    expect(screen.getByRole('group', { name: '页面' })).toHaveTextContent('全部实例')
  })

  it('默认高亮首项，方向键在结果间移动且到边界即停', async () => {
    render(<PaletteHarness />)
    await screen.findByRole('listbox')
    const input = screen.getByRole('combobox')

    expect(activeOption()).toHaveTextContent('全部实例')

    fireEvent.keyDown(input, { key: 'ArrowDown' })
    expect(activeOption()).toHaveTextContent('节点管理')

    fireEvent.keyDown(input, { key: 'ArrowUp' })
    expect(activeOption()).toHaveTextContent('全部实例')

    // 已在首项，再按上不应环绕到末项
    fireEvent.keyDown(input, { key: 'ArrowUp' })
    expect(activeOption()).toHaveTextContent('全部实例')
  })

  it('Enter 执行当前高亮项，并关闭面板', async () => {
    const onSelect = vi.fn()
    const onOpenChange = vi.fn()
    render(
      <PaletteHarness
        items={[{ ...FIRST_ITEM, onSelect }]}
        onOpenChange={onOpenChange}
      />,
    )
    await screen.findByRole('listbox')

    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'Enter' })

    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onOpenChange).toHaveBeenLastCalledWith(false)
    await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull())
  })

  it('鼠标点击结果同样执行并关闭', async () => {
    const onSelect = vi.fn()
    render(<PaletteHarness items={[{ ...FIRST_ITEM, onSelect }]} />)
    await screen.findByRole('listbox')

    fireEvent.click(screen.getByRole('option', { name: /全部实例/ }))

    expect(onSelect).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull())
  })

  it('Esc 关闭面板', async () => {
    const onOpenChange = vi.fn()
    render(<PaletteHarness onOpenChange={onOpenChange} />)
    await screen.findByRole('listbox')

    // 输入框内与 document 两级都会走到关闭，故只断言「最后一次是 false」而不限定调用次数
    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'Escape' })

    expect(onOpenChange).toHaveBeenLastCalledWith(false)
    await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull())
  })

  it('按原路由与 keywords 检索：只留命中的那一条', async () => {
    render(<PaletteHarness />)
    await screen.findByRole('listbox')

    type('/instances/42')
    expect(screen.getAllByRole('option')).toHaveLength(1)
    expect(screen.getByRole('option')).toHaveTextContent('srv-0001')
  })

  it('按原页面名检索：命中 keywords 而非 label', async () => {
    render(<PaletteHarness />)
    await screen.findByRole('listbox')

    type('实例列表')
    expect(screen.getAllByRole('option')).toHaveLength(1)
    expect(screen.getByRole('option')).toHaveTextContent('全部实例')
  })

  it('多词按「与」收窄：两个词必须同时命中同一条', async () => {
    render(<PaletteHarness />)
    await screen.findByRole('listbox')

    // 单个词各自都会命中别处，只有合起来才唯一
    type('10.0.0.1')
    expect(screen.getAllByRole('option')).toHaveLength(1)
    type('10.0.0.1 主控机')
    expect(screen.getByRole('option')).toHaveTextContent('node-main')

    type('主控机 后端服')
    expect(screen.queryAllByRole('option')).toHaveLength(0)
  })

  it('无结果时给出空态而不是空白列表', async () => {
    render(<PaletteHarness />)
    await screen.findByRole('listbox')

    type('不存在的条目')

    expect(screen.queryAllByRole('option')).toHaveLength(0)
    const empty = document.querySelector('[data-slot="command-palette-empty"]')
    expect(empty).not.toBeNull()
    expect(empty).toHaveTextContent('没有匹配项')
  })

  it('命中片段被高亮，未命中部分原样保留', async () => {
    render(<PaletteHarness />)
    await screen.findByRole('listbox')

    type('srv')
    const option = screen.getByRole('option')
    expect(option.querySelector('mark')).toHaveTextContent('srv')
    // 高亮只负责分段，不能吃掉标签的其余字符
    expect(option).toHaveTextContent('srv-0001')
  })

  it('检索改变后高亮游标回到首项，不会停在越界位置', async () => {
    render(<PaletteHarness />)
    await screen.findByRole('listbox')
    const input = screen.getByRole('combobox')

    fireEvent.keyDown(input, { key: 'ArrowDown' })
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    type('/instances/42')

    expect(activeOption()).toHaveTextContent('srv-0001')
  })
})

/** 对象页头的基准 props：实例（后端服）身份。 */
const OBJECT_PROPS: React.ComponentProps<typeof ObjectPageHeader> = {
  breadcrumbs: [
    { label: '实例', to: '/instances' },
    { label: 'srv-0001' },
  ],
  title: 'srv-0001',
  status: { tone: 'success', label: '运行中' },
  meta: [
    { label: '所属节点', value: 'node-main' },
    { label: 'UUID', value: 'a1b2c3' },
  ],
  metrics: [
    { label: 'TPS', value: '20.3' },
    { label: 'CPU', value: '38%', tone: 'warning' },
  ],
  tools: [
    { key: 'overview', label: '概览', active: true },
    { key: 'terminal', label: '终端' },
  ],
}

/** 渲染对象页头，可按字段覆盖基准 props（覆盖项优先）。 */
function renderObjectHeader(overrides: Partial<React.ComponentProps<typeof ObjectPageHeader>> = {}) {
  return render(<ObjectPageHeader {...OBJECT_PROPS} {...overrides} />)
}

describe('ObjectPageHeader 对象页头', () => {
  it('四段结构自上而下：面包屑 → 身份 → 指标条 → 工具', () => {
    const { container } = renderObjectHeader()
    const header = container.querySelector('[data-slot="object-page-header"]')

    expect(header).not.toBeNull()
    expect(Array.from(header!.children).map((el) => el.getAttribute('data-slot'))).toEqual([
      'object-breadcrumb',
      'object-identity',
      'object-metrics',
      'object-tools',
    ])
  })

  it('实例与节点复用同一组件、同一结构', () => {
    /** 按首次出现顺序取去重结构钩子——两侧条目数不同，结构与顺序才是要锁的契约。 */
    const shapeOf = (root: HTMLElement) =>
      Array.from(new Set(Array.from(root.querySelectorAll('[data-slot]')).map((el) => el.getAttribute('data-slot'))))

    const instance = renderObjectHeader()
    const instanceShape = shapeOf(instance.container)
    instance.unmount()

    const node = renderObjectHeader({
      breadcrumbs: [
        { label: '节点', to: '/nodes' },
        { label: 'node-main' },
      ],
      title: 'node-main',
      status: { tone: 'warning', label: '维护中' },
      meta: [{ label: '地址', value: '10.0.0.1' }],
      metrics: [{ label: 'CPU', value: '12%' }],
      tools: [{ key: 'runtime', label: '运行时', active: true }],
      toolsLabel: '节点工具',
    })
    const nodeShape = shapeOf(node.container)

    expect(nodeShape).toEqual(instanceShape)
    // 节点侧同样保留状态、指标与对象级工具导航
    expect(screen.getByRole('navigation', { name: '节点工具' })).toBeInTheDocument()
    expect(node.container.querySelector('[data-slot="object-status"]')).toHaveAttribute(
      'data-tone',
      'warning',
    )
  })

  it('面包屑：多级渲染为链接 + 分隔符，末级为当前位置', () => {
    const { container } = renderObjectHeader()
    const crumb = container.querySelector('[data-slot="object-breadcrumb"]')!

    expect(crumb).toHaveTextContent('实例')
    expect(crumb).toHaveTextContent('srv-0001')
    const link = screen.getByRole('link', { name: '实例' })
    expect(link).toHaveAttribute('href', '/instances')
    expect(container.querySelector('[data-slot="object-breadcrumb-current"]')).toHaveTextContent(
      'srv-0001',
    )
  })

  it('面包屑：单级与空数组都整块省略（无信息增量）', () => {
    const single = renderObjectHeader({ breadcrumbs: [{ label: '实例', to: '/instances' }] })
    expect(single.container.querySelector('[data-slot="object-breadcrumb"]')).toBeNull()
    single.unmount()

    const none = renderObjectHeader({ breadcrumbs: [] })
    expect(none.container.querySelector('[data-slot="object-breadcrumb"]')).toBeNull()
  })

  it('面包屑链接交给 onNavigate 接管（不依赖具体路由实现）', () => {
    const onNavigate = vi.fn()
    renderObjectHeader({ onNavigate })

    // fireEvent 返回 false 即事件被 preventDefault，说明默认跳转确实被拦下
    expect(fireEvent.click(screen.getByRole('link', { name: '实例' }))).toBe(false)
    expect(onNavigate).toHaveBeenCalledWith('/instances')
  })

  it('身份区：对象名、状态徽章与元信息', () => {
    const { container } = renderObjectHeader()

    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('srv-0001')
    // 对象名就是这一屏唯一的 h1，不再重复渲染页名
    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)

    const status = container.querySelector('[data-slot="object-status"]')!
    expect(status).toHaveAttribute('data-tone', 'success')
    // 状态色沿用设计系统的 StatusBadge，不在对象头里另起一份色表
    expect(status.querySelector('[data-slot="status-badge"]')).toHaveTextContent('运行中')

    expect(container.querySelector('[data-slot="object-meta"]')).toHaveTextContent('所属节点')
    expect(container.querySelector('[data-slot="object-meta"]')).toHaveTextContent('a1b2c3')
  })

  it('状态缺省色调落回 default，并映射为中性徽章', () => {
    const { container } = renderObjectHeader({ status: { label: '未知' } })
    const status = container.querySelector('[data-slot="object-status"]')!
    expect(status).toHaveAttribute('data-tone', 'default')
    expect(status.querySelector('[data-slot="status-badge"]')).toHaveTextContent('未知')
  })

  it('操作区承载影响当前对象的操作，是身份区的兄弟而非子项', () => {
    const { container } = renderObjectHeader({
      actions: <button type="button">启动</button>,
    })

    const actions = container.querySelector<HTMLElement>('[data-slot="object-actions"]')!
    expect(actions).toHaveTextContent('启动')
    expect(container.querySelector<HTMLElement>('[data-slot="object-identity"]')).toContainElement(
      actions,
    )
  })

  it('指标条：一行数字，带语义色调与时效说明', () => {
    const { container } = renderObjectHeader({ note: '探针 · 2 秒前更新' })

    const metrics = container.querySelectorAll('[data-slot="object-metric"]')
    expect(metrics).toHaveLength(2)
    expect(metrics[0]).toHaveTextContent('TPS')
    expect(metrics[0]).toHaveTextContent('20.3')
    expect(metrics[0]).toHaveAttribute('data-tone', 'default')
    expect(metrics[1]).toHaveAttribute('data-tone', 'warning')
    expect(container.querySelector('[data-slot="object-metrics"]')).toHaveTextContent(
      '探针 · 2 秒前更新',
    )
  })

  it('指标条：没有指标但有说明时仍要出现（观测不可用也要交代采样时间）', () => {
    const { container } = renderObjectHeader({
      metrics: [],
      note: '观测不可用 · 上次采样 8 分钟前',
    })

    expect(container.querySelector('[data-slot="object-metrics"]')).toHaveTextContent(
      '观测不可用 · 上次采样 8 分钟前',
    )
    expect(container.querySelectorAll('[data-slot="object-metric"]')).toHaveLength(0)
  })

  it('工具导航：当前工具标记 aria-current，点击回调可切换', () => {
    const onSelect = vi.fn()
    renderObjectHeader({
      tools: [
        { key: 'overview', label: '概览', active: true, onSelect },
        { key: 'terminal', label: '终端' },
      ],
      toolsLabel: '实例工具',
    })

    const nav = screen.getByRole('navigation', { name: '实例工具' })
    expect(nav).toHaveAttribute('data-slot', 'object-tools')

    const active = screen.getByRole('button', { name: '概览' })
    expect(active).toHaveAttribute('data-active', 'true')
    expect(active).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('button', { name: '终端' })).not.toHaveAttribute('data-active')

    fireEvent.click(active)
    expect(onSelect).toHaveBeenCalledTimes(1)
  })

  it('未传面包屑 / 指标 / 工具时不渲染空块', () => {
    const { container } = renderObjectHeader({
      breadcrumbs: [],
      metrics: [],
      tools: [],
      meta: [],
    })

    expect(container.querySelector('[data-slot="object-breadcrumb"]')).toBeNull()
    expect(container.querySelector('[data-slot="object-metrics"]')).toBeNull()
    expect(container.querySelector('[data-slot="object-tools"]')).toBeNull()
    expect(container.querySelector('[data-slot="object-meta"]')).toBeNull()
    // 根元素始终存在，且只剩身份一行
    expect(container.querySelector('[data-slot="object-page-header"]')).not.toBeNull()
  })
})
