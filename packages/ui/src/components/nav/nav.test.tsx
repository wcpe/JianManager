import { fireEvent, render, screen, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { SideNav, SideNavBottom, SideNavHead, SideNavResource, SideNavRow, SideNavShortcuts } from './SideNav'
import { SidebarFooter } from './SidebarFooter'
import { SidebarGroup } from './SidebarGroup'
import { SidebarRail } from './SidebarRail'
import { SidebarSection } from './SidebarSection'
import { SidebarShell } from './SidebarShell'
import { TopNav } from './TopNav'

/**
 * 导航原语测试（FR-502 阶段 4；原 `shell.test.tsx`）。
 *
 * 与阶段 3 的布局层测试同一取向：不测视觉，只锁**结构性契约**与**交互行为**——
 * `data-slot` / `data-*` 钩子、槽位透传、回调参数、折叠态（后续页面迁移与阶段 5
 * 接路由都会依赖这些契约保持稳定）。
 */
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

  it('导航行区分当前项：data-active + sidebar-accent 底色（aria-current 归调用方的链接）', () => {
    const { container } = renderSideNav()
    const rows = [...container.querySelectorAll('[data-slot="side-nav-row"]')]
    const active = rows.find((r) => r.getAttribute('data-active') === 'true')!
    expect(active).toHaveTextContent('集群总览')
    // 侧栏行改用 shadcn 的 sidebar token 组（--sidebar-accent），不再借用正文区的 accent：
    // 侧栏是常驻导航区，它的 hover/选中底与正文区不是同一套取值。
    expect(active.className).toContain('bg-sidebar-accent')

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

/**
 * 侧栏通用原语（ADR-103 拆分判据）：外壳 / 分组 / 底栏 / 钢轨 / 分区标题。
 *
 * 这五个件是给「应用侧自建侧栏」用的通用件，因此断言只锁两件事：
 * ① **自给自足**——外壳自己注入宽度变量，脱离主控台外壳也能工作；
 * ② **只认基础值**——props 是字符串/数字/布尔/回调/节点，出现业务类型就说明分错了。
 */
describe('侧栏通用原语', () => {
  describe('SidebarShell 外壳', () => {
    it('自己注入折叠/展开宽度变量（不依赖宿主提供）', () => {
      const { container } = render(
        <SidebarShell>
          <SideNavShortcuts>
            <SideNavRow>首页</SideNavRow>
          </SideNavShortcuts>
        </SidebarShell>,
      )
      const aside = container.querySelector('[data-slot="sidebar-shell"]') as HTMLElement
      expect(aside).toBeInTheDocument()
      // 行组件读这两个变量算居中 padding；外壳不注入时它只能退回 fallback。
      expect(aside.style.getPropertyValue('--sidebar-expanded-width')).toBe('246px')
      expect(aside.style.getPropertyValue('--sidebar-collapsed-width')).toBe('54px')
      expect(aside.className).toContain('transition-[width]')
    })

    it('宽度可覆盖，且折叠态同时体现在 data-collapsed 与 data-state', () => {
      const { container } = render(
        <SidebarShell collapsed expandedWidth={300} collapsedWidth={64}>
          <span>内容</span>
        </SidebarShell>,
      )
      const aside = container.querySelector('[data-slot="sidebar-shell"]') as HTMLElement
      expect(aside.style.getPropertyValue('--sidebar-expanded-width')).toBe('300px')
      expect(aside.style.getPropertyValue('--sidebar-collapsed-width')).toBe('64px')
      expect(aside).toHaveAttribute('data-collapsed', 'true')
      expect(aside).toHaveAttribute('data-state', 'collapsed')
    })

    it('折叠态通过共享 context 下发给行（复用 SideNavRow，不另造行）', () => {
      const { container } = render(
        <SidebarShell collapsed>
          <SideNavShortcuts>
            <SideNavRow count={3}>全部实例</SideNavRow>
          </SideNavShortcuts>
        </SidebarShell>,
      )
      const row = container.querySelector('[data-slot="side-nav-row"]') as HTMLElement
      // 行的折叠动画仍由 ui 包一处真源负责：这里只确认外壳能把状态传下去。
      expect(row.className).toContain('gap-0')
      expect(row.querySelector('[data-slot="side-nav-count"]')!.className).toContain('max-w-0')
    })
  })

  describe('SidebarGroup 分组容器', () => {
    it('默认展开：标题 + 计数 + 内容都在，且不带开合语义', () => {
      const { container } = render(
        <SidebarGroup title="常用" count={12}>
          <span>条目</span>
        </SidebarGroup>,
      )
      const group = container.querySelector('[data-slot="sidebar-group"]') as HTMLElement
      expect(group).toHaveTextContent('常用')
      expect(group.querySelector('[data-slot="sidebar-group-count"]')).toHaveTextContent('12')
      expect(group).toHaveTextContent('条目')
      expect(group.querySelector('button')).toBeNull()
      expect(container.querySelector('[data-slot="sidebar-group-trigger"]')).toBeNull()
    })

    it('可折叠：用 aria-expanded 表达状态，收起走 grid-rows 0fr + inert（不是卸载）', () => {
      const { container } = render(
        <SidebarGroup title="分组" collapsible defaultOpen={false}>
          <span>条目</span>
        </SidebarGroup>,
      )
      const trigger = container.querySelector('[data-slot="sidebar-group-trigger"]') as HTMLElement
      expect(trigger).toHaveAttribute('aria-expanded', 'false')
      // 内容仍在 DOM（收起是过渡，不是卸载）：只有 grid 行高归零 + 不可交互。
      const body = trigger.nextElementSibling as HTMLElement
      expect(body.className).toContain('grid-rows-[0fr]')
      expect(body).toHaveAttribute('inert')
      expect(body).toHaveTextContent('条目')
    })

    it('可折叠：点击开合，受控与非受控都走 onOpenChange', () => {
      const onOpenChange = vi.fn()
      const { container } = render(
        <SidebarGroup title="分组" collapsible onOpenChange={onOpenChange}>
          <span>条目</span>
        </SidebarGroup>,
      )
      const trigger = container.querySelector('[data-slot="sidebar-group-trigger"]') as HTMLElement
      expect(trigger).toHaveAttribute('aria-expanded', 'true')
      fireEvent.click(trigger)
      expect(onOpenChange).toHaveBeenCalledWith(false)
      expect(trigger).toHaveAttribute('aria-expanded', 'false')
      expect(trigger.nextElementSibling?.className).toContain('grid-rows-[0fr]')
    })
  })

  describe('SidebarFooter 底栏', () => {
    it('一行并排：自带 flex 排布，子节点按序渲染', () => {
      const { container } = render(
        <SidebarFooter className="custom">
          <button type="button">A</button>
          <button type="button">B</button>
        </SidebarFooter>,
      )
      const footer = container.querySelector('[data-slot="sidebar-footer"]') as HTMLElement
      expect(footer.className).toContain('flex')
      expect(footer.className).toContain('items-center')
      expect(footer.className).toContain('custom')
      expect(footer.querySelectorAll('button')).toHaveLength(2)
    })
  })

  describe('SidebarRail 钢轨', () => {
    it('竖向堆叠、图标居中、自行滚动（行数多时不把底栏顶出视口）', () => {
      const { container } = render(
        <SidebarRail>
          <span>图标</span>
        </SidebarRail>,
      )
      const rail = container.querySelector('[data-slot="sidebar-rail"]') as HTMLElement
      expect(rail.className).toContain('flex-col')
      expect(rail.className).toContain('items-center')
      expect(rail.className).toContain('overflow-y-auto')
      expect(rail.className).toContain('min-h-0')
    })
  })

  describe('SidebarSection 分区标题', () => {
    it('默认不画分隔线，divider 时用 border-t（不额外插 hr 占高度）', () => {
      const { container: plain } = render(<SidebarSection title="最近" />)
      const plainEl = plain.querySelector('[data-slot="sidebar-section"]') as HTMLElement
      expect(plainEl.className).not.toContain('border-t')
      expect(plainEl).toHaveTextContent('最近')

      const { container: divided } = render(<SidebarSection title="最近" divider />)
      const dividedEl = divided.querySelector('[data-slot="sidebar-section"]') as HTMLElement
      expect(dividedEl.className).toContain('border-t')
      expect(dividedEl.querySelector('hr')).toBeNull()
    })
  })
})
