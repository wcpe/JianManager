import { fireEvent, render, screen, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { AppShell } from './AppShell'
import { ResourceTree, type ResourceNode } from './ResourceTree'
import { SideNav, SideNavBottom, SideNavHead, SideNavResource, SideNavRow, SideNavShortcuts } from './SideNav'
import { TopNav } from './TopNav'

/**
 * 导航骨架外壳测试（FR-496 阶段 4）。
 *
 * 与阶段 3 的布局层测试同一取向：不测视觉，只锁**结构性契约**与**交互行为**——
 * `data-slot` / `data-*` 钩子、槽位透传、回调参数、折叠态、以及资源树最关键的那条
 * 「默认只列 7 个实例」限流规则（后续页面迁移与阶段 5 接路由都会依赖这些契约保持稳定）。
 */
describe('AppShell 应用外壳', () => {
  it('顶栏固定 53px，主体撑满剩余高度', () => {
    render(
      <AppShell topbar={<span>顶栏</span>} data-testid="shell">
        <span>页面</span>
      </AppShell>,
    )
    const shell = screen.getByTestId('shell')
    expect(shell).toHaveAttribute('data-slot', 'app-shell')
    expect(shell.className).toContain('h-dvh')

    const topbar = shell.querySelector('[data-slot="app-shell-topbar"]')
    expect(topbar).toBeInTheDocument()
    expect(topbar!.className).toContain('h-[53px]')
    expect(topbar!.className).toContain('shrink-0')

    const body = shell.querySelector('[data-slot="app-shell-body"]')
    expect(body!.className).toContain('min-h-0')
    expect(body!.className).toContain('min-w-0')
  })

  it('页面区占剩余宽度并自身滚动（页面内有滚动模型时内层先滚）', () => {
    render(<AppShell data-testid="shell">页面</AppShell>)
    const main = screen.getByTestId('shell').querySelector('[data-slot="app-shell-main"]')!
    expect(main.className).toContain('flex-1')
    expect(main.className).toContain('overflow-auto')
    expect(main.tagName).toBe('MAIN')
    expect(main).toHaveTextContent('页面')
  })

  it('不传 topbar / sidebar 时仍渲染主体（登录、独立认证页复用同一外壳）', () => {
    const { container } = render(<AppShell />)
    expect(screen.queryByRole('banner')).toBeNull()
    expect(container.querySelector('[data-slot="app-shell-topbar"]')).toBeNull()
    expect(container.querySelector('[data-slot="app-shell-body"]')).toBeInTheDocument()
  })

  it('槽位内容与 className 合并而非替换', () => {
    render(
      <AppShell className="custom-x" topbar={<span>栏</span>} sidebar={<span>栏侧</span>}>
        页面
      </AppShell>,
    )
    const shell = screen.getByRole('main').parentElement!.parentElement!
    expect(shell.className).toContain('custom-x')
    expect(shell.className).toContain('h-dvh')
    expect(screen.getByText('栏侧')).toBeInTheDocument()
  })
})

describe('TopNav 顶栏', () => {
  const workspaces = [
    { key: 'servers', label: '服务器运维', active: true },
    { key: 'platform', label: '平台管理' },
    { key: 'identity', label: '身份与访问' },
    { key: 'content', label: '运行时与内容' },
  ]

  it('渲染品牌、四个工作区与右端工具区', () => {
    render(
      <TopNav
        brand={<a href="#/instances">JianManager</a>}
        workspaces={workspaces}
        actions={<button type="button">通知</button>}
      />,
    )
    expect(screen.getByRole('banner')).toHaveAttribute('data-slot', 'top-nav')
    expect(screen.getByRole('link', { name: 'JianManager' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '通知' })).toBeInTheDocument()

    const nav = screen.getByRole('navigation', { name: '工作区' })
    expect(within(nav).getAllByRole('button')).toHaveLength(4)
    expect(nav.className).toContain('gap-[18px]')
    expect(nav.className).toContain('px-[10px]')
  })

  it('当前工作区标记 data-active / aria-current，并以主色下划线（伪元素）强调', () => {
    render(<TopNav workspaces={workspaces} />)
    const active = screen.getByRole('button', { name: '服务器运维' })
    expect(active).toHaveAttribute('data-active', 'true')
    expect(active).toHaveAttribute('aria-current', 'page')
    expect(active.className).toContain('after:bg-primary')
    expect(active.className).toContain('text-accent-foreground')

    const idle = screen.getByRole('button', { name: '平台管理' })
    expect(idle).not.toHaveAttribute('data-active')
    expect(idle).not.toHaveAttribute('aria-current')
  })

  it('点击工作区按钮回传 key', () => {
    const onWorkspaceChange = vi.fn()
    render(<TopNav workspaces={workspaces} onWorkspaceChange={onWorkspaceChange} />)
    fireEvent.click(screen.getByRole('button', { name: '身份与访问' }))
    expect(onWorkspaceChange).toHaveBeenCalledWith('identity')
  })

  it('全局查找入口带快捷键提示，点击触发 onSearch', () => {
    const onSearch = vi.fn()
    render(<TopNav onSearch={onSearch} />)
    const search = screen.getByRole('button', { name: '全局搜索' })
    expect(search).toHaveAttribute('data-slot', 'top-nav-search')
    expect(within(search).getByText('⌘ K')).toBeInTheDocument()
    fireEvent.click(search)
    expect(onSearch).toHaveBeenCalledTimes(1)
  })

  it('未传 onSearch 时不渲染查找入口', () => {
    const { container } = render(<TopNav />)
    expect(container.querySelector('[data-slot="top-nav-search"]')).toBeNull()
  })

  it('折叠按钮透传状态并可切换侧栏', () => {
    const onToggleSidebar = vi.fn()
    const { rerender } = render(
      <TopNav brand={<span>品牌</span>} sidebarCollapsed={false} onToggleSidebar={onToggleSidebar} />,
    )
    const toggle = screen.getByRole('button', { name: '收起 / 展开资源导航' })
    expect(toggle).toHaveAttribute('aria-pressed', 'false')
    fireEvent.click(toggle)
    expect(onToggleSidebar).toHaveBeenCalledTimes(1)

    rerender(<TopNav brand={<span>品牌</span>} sidebarCollapsed onToggleSidebar={onToggleSidebar} />)
    expect(screen.getByRole('button', { name: '收起 / 展开资源导航' })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
  })
})

describe('SideNav 资源侧栏', () => {
  function renderSideNav(collapsed = false) {
    return render(
      <SideNav collapsed={collapsed}>
        <SideNavHead icon={<span>ico</span>} pill="资源视图">
          服务器运维
        </SideNavHead>
        <SideNavShortcuts>
          <SideNavRow icon={<span>a</span>} active>
            集群总览
          </SideNavRow>
          <SideNavRow icon={<span>b</span>} count={1248}>
            全部实例
          </SideNavRow>
        </SideNavShortcuts>
        <SideNavResource>
          <span>资源区</span>
        </SideNavResource>
        <SideNavBottom>
          <SideNavRow icon={<span>c</span>}>群组与拓扑</SideNavRow>
        </SideNavBottom>
      </SideNav>,
    )
  }

  it('四段结构各自暴露 data-slot，顺序为 头 / 固定入口 / 资源区 / 底部', () => {
    const { container } = renderSideNav()
    const aside = container.querySelector('[data-slot="side-nav"]')!
    expect(aside.tagName).toBe('ASIDE')
    expect(aside.className).toContain('w-[246px]')
    expect(aside.className).toContain('border-r')
    expect(aside.className).toContain('flex-col')
    expect(aside).not.toHaveAttribute('data-collapsed')

    const slots = Array.from(aside.children).map((el) => el.getAttribute('data-slot'))
    expect(slots).toEqual([
      'side-nav-head',
      'side-nav-shortcuts',
      'side-nav-resource',
      'side-nav-bottom',
    ])
  })

  it('头段承载工作区名与 pill 徽章', () => {
    const { container } = renderSideNav()
    const head = container.querySelector('[data-slot="side-nav-head"]')!
    expect(head).toHaveTextContent('服务器运维')
    expect(head.querySelector('[data-slot="side-nav-pill"]')).toHaveTextContent('资源视图')
  })

  it('导航行区分当前项：data-active + accent 底色（aria-current 归调用方的链接）', () => {
    const { container } = renderSideNav()
    const rows = [...container.querySelectorAll('[data-slot="side-nav-row"]')]
    const active = rows.find((r) => r.getAttribute('data-active') === 'true')!
    expect(active).toHaveTextContent('集群总览')
    expect(active.className).toContain('bg-accent')

    const idle = rows.find((r) => r.textContent?.includes('群组与拓扑'))!
    expect(idle).not.toHaveAttribute('data-active')
    // `aria-current` 不再由行组件自持：行是 `<div>`，该语义必须落在调用方包的 `<a>` 上才有效
    // （读屏只把可聚焦元素上的 aria-current 当作「当前页」）。
    expect(idle).not.toHaveAttribute('aria-current')
  })

  it('计数徽章渲染；0 也要显示（0 是有意义的值）', () => {
    render(
      <SideNav>
        <SideNavShortcuts>
          <SideNavRow count={1248}>全部实例</SideNavRow>
          <SideNavRow count={0}>异常实例</SideNavRow>
        </SideNavShortcuts>
      </SideNav>,
    )
    const badges = document.querySelectorAll('[data-slot="side-nav-count"]')
    expect(badges).toHaveLength(2)
    expect(badges[0]).toHaveTextContent('1,248')
    expect(badges[1]).toHaveTextContent('0')
  })

  it('导航行只负责外观：透传原生属性，语义交给调用方外层包的元素', () => {
    const { container } = render(
      <SideNav>
        <SideNavShortcuts>
          <SideNavRow className="custom-row">集群总览</SideNavRow>
        </SideNavShortcuts>
      </SideNav>,
    )
    const row = container.querySelector('[data-slot="side-nav-row"]')!
    expect(row).toHaveTextContent('集群总览')
    expect(row.className).toContain('custom-row')
    // 行组件**不得**自己渲染 `<button>`：`<a>` 里不允许嵌 `<button>`，
    // 一旦自持 button，调用方就永远包不出真链接（中键 / 新标签页 / 复制链接地址都会丢）。
    // 这条断言是该约束的守护。
    expect(row.tagName).toBe('DIV')
    expect(row).not.toHaveAttribute('type')
    expect(row).not.toHaveAttribute('disabled')
  })

  it('折叠态：宽 54px，文字与计数隐藏、图标居中', () => {
    const { container } = renderSideNav(true)
    const aside = container.querySelector('[data-slot="side-nav"]')!
    expect(aside).toHaveAttribute('data-collapsed', 'true')
    expect(aside.className).toContain('w-[54px]')

    // 头段整段收起（54px 放不下工作区名）。收起方式不是 `hidden` 瞬跳，
    // 而是 grid-rows 0fr + 淡出，与侧栏宽度过渡同节拍。
    expect(container.querySelector('[data-slot="side-nav-head"]')!.className).toContain('grid-rows-[0fr]')
    // 资源区同样改为 grid-rows 收起（`hidden` 不可过渡，会让剩下几段立刻重排高度）。
    expect(container.querySelector('[data-slot="side-nav-resource"]')!.className).toContain('grid-rows-[0fr]')

    // 行组件不再渲染 `<button>`（语义交给调用方外层包的元素），故按 data-slot 取。
    const row = [...container.querySelectorAll('[data-slot="side-nav-row"]')].find((r) =>
      r.textContent?.includes('全部实例'),
    ) as HTMLElement
    // 【图标居中的实现方式】用对称 padding 让图标自然落中，而**不是** `justify-center`：
    // 后者是 `justify-content`、不参与过渡（瞬跳），折叠一开始就把「图标 + 尚未收窄的文字」
    // 整体居中，图标先偏左、等文字收完再跳回中间——即「位移出去又闪现居中」。
    // `gap-0` 是前提：文字宽度收为 0 后若仍留 `gap-[9px]`，图标会被那 9px 推偏。
    expect(row.className).toContain('gap-0')
    expect(row.className).toContain('px-[calc((var(--sidebar-collapsed-width,3.5rem)-29px)/2)]')
    // 文字与计数改为「宽度收 0 + 淡出」而非 `hidden` 瞬跳（后者是撕裂感的来源之一）。
    expect(within(row).getByText('全部实例').className).toContain('max-w-0')
    expect(row.querySelector('[data-slot="side-nav-count"]')!.className).toContain('max-w-0')
  })

  it('底部段的行比固定入口矮一档', () => {
    const { container } = renderSideNav()
    expect(container.querySelector('[data-slot="side-nav-bottom"]')!.className).toContain(
      '[&_[data-slot=side-nav-row]]:min-h-[33px]',
    )
  })
})

describe('ResourceTree 资源树：展开与限流', () => {
  const makeNodes = (count: number, extra: Partial<ResourceNode> = {}): ResourceNode[] => [
    {
      id: 'node-east-02',
      name: 'node-east-02',
      status: 'normal',
      instances: Array.from({ length: count }, (_, index) => ({
        id: `i-${index + 1}`,
        name: `survival-${index + 1}`,
        status: 'normal' as const,
      })),
      ...extra,
    },
  ]

  it('节点行：展开开关 + 状态点 + 名称 + 实例数徽章；默认折叠', () => {
    const { container } = render(<ResourceTree nodes={makeNodes(3)} />)
    const node = container.querySelector('[data-slot="resource-tree-node"]')!
    expect(node).not.toHaveAttribute('data-open')
    expect(node.querySelector('[data-slot="resource-status-dot"]')).toHaveAttribute(
      'data-status',
      'normal',
    )
    expect(node.querySelector('[data-slot="resource-tree-node-count"]')).toHaveTextContent('3')
    expect(screen.queryByText('survival-1')).toBeNull()

    const toggle = screen.getByRole('button', { name: '展开 node-east-02' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(toggle)
    expect(screen.getByRole('button', { name: '收起 node-east-02' })).toHaveAttribute(
      'aria-expanded',
      'true',
    )
    expect(node).toHaveAttribute('data-open', 'true')
    expect(screen.getByText('survival-1')).toBeInTheDocument()
  })

  it('恰好 7 个实例：全部列出，不出现「查看其余」', () => {
    render(<ResourceTree nodes={makeNodes(7)} />)
    fireEvent.click(screen.getByRole('button', { name: '展开 node-east-02' }))
    expect(document.querySelectorAll('[data-slot="resource-tree-instance"]')).toHaveLength(7)
    expect(document.querySelector('[data-slot="resource-tree-more"]')).toBeNull()
  })

  it('第 8 个实例起被折叠成「查看其余 1 个实例」', () => {
    render(<ResourceTree nodes={makeNodes(8)} />)
    fireEvent.click(screen.getByRole('button', { name: '展开 node-east-02' }))
    expect(document.querySelectorAll('[data-slot="resource-tree-instance"]')).toHaveLength(7)
    expect(screen.queryByText('survival-8')).toBeNull()
    expect(screen.getByRole('button', { name: '查看其余 1 个实例' })).toBeInTheDocument()
  })

  it('实例数远超 7 时链接数字反映缺口；点击回传节点', () => {
    const onShowAll = vi.fn()
    render(<ResourceTree nodes={makeNodes(20)} onShowAll={onShowAll} />)
    fireEvent.click(screen.getByRole('button', { name: '展开 node-east-02' }))
    const more = screen.getByRole('button', { name: '查看其余 13 个实例' })
    fireEvent.click(more)
    expect(onShowAll).toHaveBeenCalledWith(expect.objectContaining({ id: 'node-east-02' }))
  })

  it('只下发了部分实例时，用 instanceCount / hiddenCount 如实声明总量', () => {
    render(<ResourceTree nodes={[makeNodes(3, { instanceCount: 60, hiddenCount: 2 })[0]!]} />)
    fireEvent.click(screen.getByRole('button', { name: '展开 node-east-02' }))
    // 3 条已下发 + 2 条未下发 → 60 - 3 + 2
    expect(screen.getByRole('button', { name: '查看其余 59 个实例' })).toBeInTheDocument()
  })

  it('visibleLimit 可调（大屏可放宽到 12）', () => {
    render(<ResourceTree nodes={makeNodes(20)} visibleLimit={12} />)
    fireEvent.click(screen.getByRole('button', { name: '展开 node-east-02' }))
    expect(document.querySelectorAll('[data-slot="resource-tree-instance"]')).toHaveLength(12)
    expect(screen.getByRole('button', { name: '查看其余 8 个实例' })).toBeInTheDocument()
  })

  it('点击实例行回传实例与所属节点', () => {
    const onSelectInstance = vi.fn()
    render(<ResourceTree nodes={makeNodes(3)} onSelectInstance={onSelectInstance} />)
    fireEvent.click(screen.getByRole('button', { name: '展开 node-east-02' }))
    fireEvent.click(screen.getByRole('button', { name: 'survival-2' }))
    expect(onSelectInstance).toHaveBeenCalledWith(
      expect.objectContaining({ id: 'i-2', name: 'survival-2' }),
      expect.objectContaining({ id: 'node-east-02' }),
    )
  })
})

describe('ResourceTree 资源树：状态与搜索', () => {
  const nodes: ResourceNode[] = [
    {
      id: 'n1',
      name: 'node-east-02',
      status: 'warn',
      instances: [
        { id: 'i1', name: 'lobby-01', status: 'bad' },
        { id: 'i2', name: 'lobby-02', status: 'muted' },
      ],
    },
    {
      id: 'n2',
      name: 'node-west-01',
      status: 'normal',
      instances: [{ id: 'i3', name: 'survival-01', status: 'normal' }],
    },
  ]

  it('状态点三态（含已停机的中性态）由 data-status 暴露', () => {
    const { container } = render(<ResourceTree nodes={nodes} />)
    fireEvent.click(screen.getByRole('button', { name: '展开 node-east-02' }))
    const dots = container.querySelectorAll('[data-slot="resource-status-dot"]')
    expect(Array.from(dots).map((dot) => dot.getAttribute('data-status'))).toEqual([
      'warn',
      'bad',
      'muted',
      'normal',
    ])
  })

  it('输入搜索词触发 onQueryChange', () => {
    const onQueryChange = vi.fn()
    render(<ResourceTree nodes={nodes} query="" onQueryChange={onQueryChange} />)
    fireEvent.change(screen.getByLabelText('搜索资源树'), { target: { value: 'lobby' } })
    expect(onQueryChange).toHaveBeenCalledWith('lobby')
  })

  it('搜索命中实例名：只留匹配实例、节点自动展开、其余节点过滤掉', () => {
    const { container } = render(<ResourceTree nodes={nodes} query="lobby" />)
    expect(container.querySelectorAll('[data-slot="resource-tree-node"]')).toHaveLength(1)
    expect(container.querySelector('[data-slot="resource-tree-node"]')).toHaveAttribute('data-open', 'true')
    expect(screen.getByText('lobby-01')).toBeInTheDocument()
    expect(screen.queryByText('survival-01')).toBeNull()
  })

  it('搜索命中节点名时保留该节点下全部实例', () => {
    render(<ResourceTree nodes={nodes} query="west" />)
    expect(screen.getByText('survival-01')).toBeInTheDocument()
    expect(screen.queryByText('lobby-01')).toBeNull()
  })

  it('无匹配时给出空态', () => {
    const { container } = render(<ResourceTree nodes={nodes} query="不存在的资源" />)
    expect(container.querySelector('[data-slot="resource-tree-empty"]')).toHaveTextContent('没有匹配资源')
    expect(container.querySelectorAll('[data-slot="resource-tree-node"]')).toHaveLength(0)
  })

  it('只传 query 不传 onQueryChange 时由外部搜索框驱动，不渲染内置输入框', () => {
    const { container } = render(<ResourceTree nodes={nodes} query="lobby" />)
    expect(container.querySelector('[data-slot="resource-tree-search"]')).toBeNull()
    expect(container.querySelector('[data-slot="resource-tree"]')).toHaveAttribute('data-query', 'lobby')
  })
})
