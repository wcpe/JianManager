import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import {
  CardsGrid,
  MetricCell,
  MetricGrid,
  PageHeader,
  PageShell,
  PlatformTab,
  PlatformTabs,
  ScopeBar,
  Segment,
  Segments,
  SettingsLayout,
  SummaryItem,
  SummaryStrip,
  ThreeCol,
  Toolbar,
  ToolbarSpacer,
  TwoCol,
} from './index'
import { Panel } from '../panel'

/**
 * 布局层测试（FR-496 阶段 3）。
 *
 * 这些组件的职责是「约束页面骨架」，因此测试重点不是视觉，而是**结构性契约**：
 * 壳态语义（data-variant / 滚动模型）、必需的结构钩子（data-slot）、
 * 以及内容是否被无损渲染。后续页面迁移依赖这些契约保持稳定。
 */
describe('PageShell 三种壳态', () => {
  it('默认壳态：整页滚动，带标准留白与纵向节奏', () => {
    render(
      <PageShell data-testid="shell">
        <span>内容</span>
      </PageShell>,
    )
    const shell = screen.getByTestId('shell')
    expect(shell).toHaveAttribute('data-slot', 'page-shell')
    expect(shell).toHaveAttribute('data-variant', 'default')
    expect(shell.className).toContain('overflow-auto')
    expect(shell.className).toContain('px-[25px]')
    expect(shell.className).toContain('py-[22px]')
    expect(shell).toHaveTextContent('内容')
  })

  it('fixed 壳态：固定视口，内部区域自行滚动', () => {
    render(<PageShell variant="fixed" data-testid="shell" />)
    const shell = screen.getByTestId('shell')
    expect(shell).toHaveAttribute('data-variant', 'fixed')
    expect(shell.className).toContain('overflow-hidden')
  })

  it('tool 壳态：去除外层留白与间隙，交由工具自身占满', () => {
    render(<PageShell variant="tool" data-testid="shell" />)
    const shell = screen.getByTestId('shell')
    expect(shell).toHaveAttribute('data-variant', 'tool')
    expect(shell.className).toContain('p-0')
    expect(shell.className).toContain('gap-0')
  })

  it('自定义 className 与壳态类合并而非替换', () => {
    render(<PageShell variant="fixed" className="custom-x" data-testid="shell" />)
    const shell = screen.getByTestId('shell')
    expect(shell.className).toContain('custom-x')
    expect(shell.className).toContain('overflow-hidden')
  })
})

describe('PageHeader 页头', () => {
  it('渲染标题，并用二级文本承载描述', () => {
    render(<PageHeader title="实例" description="跨节点查找服务器。" />)
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('实例')
    expect(screen.getByText('跨节点查找服务器。')).toBeInTheDocument()
  })

  it('数字计数按 zh-CN 千分位展示，字符串计数原样保留', () => {
    const { rerender } = render(<PageHeader title="实例" count={1248} />)
    expect(screen.getByText('1,248')).toBeInTheDocument()

    rerender(<PageHeader title="实例" count="约 1.2k" />)
    expect(screen.getByText('约 1.2k')).toBeInTheDocument()
  })

  it('计数为 0 时也要渲染（0 是有意义的值，不能被 falsy 判断吞掉）', () => {
    render(<PageHeader title="节点" count={0} />)
    expect(screen.getByText('0')).toBeInTheDocument()
  })

  it('无 count / description 时不产生多余节点', () => {
    const { container } = render(<PageHeader title="仅标题" />)
    expect(container.querySelectorAll('p')).toHaveLength(0)
    expect(container.querySelectorAll('span')).toHaveLength(0)
  })

  it('操作区渲染在右侧且不影响标题结构', () => {
    render(<PageHeader title="实例" actions={<button type="button">创建实例</button>} />)
    expect(screen.getByRole('button', { name: '创建实例' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('实例')
  })
})

describe('ScopeBar 作用域条', () => {
  it('以 chip 形式前置当前范围，并把说明推到最右', () => {
    render(
      <ScopeBar scope="全部节点" note="数据截至 12:00">
        <select aria-label="切换节点" />
      </ScopeBar>,
    )
    expect(screen.getByText('全部节点')).toBeInTheDocument()
    expect(screen.getByText('数据截至 12:00').tagName).toBe('SMALL')
    expect(screen.getByLabelText('切换节点')).toBeInTheDocument()
  })

  it('未传 scope / note 时只渲染子内容', () => {
    const { container } = render(
      <ScopeBar>
        <span>仅子项</span>
      </ScopeBar>,
    )
    expect(container.querySelector('small')).toBeNull()
    expect(screen.getByText('仅子项')).toBeInTheDocument()
  })
})

describe('SummaryStrip 摘要条', () => {
  it('渲染等宽统计格，并把状态语义写进 data-tone', () => {
    render(
      <SummaryStrip>
        <SummaryItem label="运行中" value={1200} tone="success" />
        <SummaryItem label="已停止" value={48} />
        <SummaryItem label="异常" value={0} tone="danger" />
      </SummaryStrip>,
    )
    expect(screen.getByText('运行中')).toBeInTheDocument()
    expect(screen.getByText('1,200')).toBeInTheDocument()
    expect(screen.getByText('运行中').closest('button')).toHaveAttribute('data-tone', 'success')
    expect(screen.getByText('已停止').closest('button')).toHaveAttribute('data-tone', 'default')
  })

  it('统计格语义为按钮，可承载跳转', () => {
    render(<SummaryItem label="异常" value={3} />)
    expect(screen.getByRole('button')).toHaveTextContent('异常')
  })
})

describe('Toolbar 工具条', () => {
  it('渲染操作区，并提供右对齐占位', () => {
    const { container } = render(
      <Toolbar>
        <input aria-label="搜索" />
        <ToolbarSpacer />
        <button type="button">刷新</button>
      </Toolbar>,
    )
    expect(container.querySelector('[data-slot="toolbar"]')).toBeInTheDocument()
    expect(container.querySelector('[data-slot="toolbar-spacer"]')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '刷新' })).toBeInTheDocument()
  })
})

describe('PlatformTabs 工作区页签', () => {
  it('标记当前页签并暴露 aria-current', () => {
    render(
      <PlatformTabs>
        <PlatformTab active>身份与访问</PlatformTab>
        <PlatformTab>运行时与内容</PlatformTab>
      </PlatformTabs>,
    )
    const activeTab = screen.getByRole('button', { name: '身份与访问' })
    expect(activeTab).toHaveAttribute('data-active', 'true')
    expect(activeTab).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('button', { name: '运行时与内容' })).not.toHaveAttribute('data-active')
  })

  it('页签容器语义为 nav', () => {
    render(<PlatformTabs aria-label="平台管理分组" />)
    expect(screen.getByRole('navigation', { name: '平台管理分组' })).toBeInTheDocument()
  })
})

describe('Segments 分段控件', () => {
  it('容器语义为 tablist，选中项暴露 data-active 与 aria-selected', () => {
    render(
      <Segments aria-label="节点视图">
        <Segment active>活跃</Segment>
        <Segment>归档</Segment>
      </Segments>,
    )
    const list = screen.getByRole('tablist', { name: '节点视图' })
    expect(list).toHaveAttribute('data-slot', 'segments')

    const active = screen.getByRole('tab', { name: '活跃' })
    expect(active).toHaveAttribute('data-active', 'true')
    expect(active).toHaveAttribute('aria-selected', 'true')

    // 未选中项：无 data-active，但仍要显式 aria-selected=false（tab 语义要求）
    const idle = screen.getByRole('tab', { name: '归档' })
    expect(idle).not.toHaveAttribute('data-active')
    expect(idle).toHaveAttribute('aria-selected', 'false')
  })

  it('选中块抬为卡片底色，未选中项走中性前景色', () => {
    render(
      <Segments>
        <Segment active>活跃</Segment>
        <Segment>归档</Segment>
      </Segments>,
    )
    expect(screen.getByRole('tab', { name: '活跃' }).className).toContain('bg-card')
    expect(screen.getByRole('tab', { name: '归档' }).className).not.toContain('bg-card')
  })

  it('容器与项都透传 className（调用方需要 w-full / flex-1 这类宽度覆盖）', () => {
    render(
      <Segments className="w-full" data-testid="list">
        <Segment className="flex-1">活跃</Segment>
      </Segments>,
    )
    expect(screen.getByTestId('list').className).toContain('w-full')
    expect(screen.getByRole('tab', { name: '活跃' }).className).toContain('flex-1')
  })
})

describe('布局网格族', () => {
  const grids = [
    ['two-col', TwoCol],
    ['three-col', ThreeCol],
    ['metric-grid', MetricGrid],
    ['cards-grid', CardsGrid],
    ['settings-layout', SettingsLayout],
  ] as const

  it.each(grids)('%s 暴露结构钩子', (slot, Grid) => {
    const { container } = render(
      <Grid>
        <span>x</span>
      </Grid>,
    )
    expect(container.querySelector(`[data-slot="${slot}"]`)).toBeInTheDocument()
  })

  it('MetricCell 可作为指标网格的子项', () => {
    render(
      <MetricGrid>
        <MetricCell>TPS 20</MetricCell>
      </MetricGrid>,
    )
    expect(screen.getByText('TPS 20')).toBeInTheDocument()
  })
})

describe('Panel 三段式（阶段 3 增强）', () => {
  it('无 footer 时保持两段结构', () => {
    const { container } = render(<Panel title="面板">正文</Panel>)
    expect(screen.getByText('正文')).toBeInTheDocument()
    expect(container.querySelectorAll('[data-slot="panel"] > div')).toHaveLength(2)
  })

  it('传入 footer 时渲染底部区，且不泄漏为 DOM 属性', () => {
    const { container } = render(
      <Panel title="面板" footer={<span>共 250 条</span>}>
        正文
      </Panel>,
    )
    expect(screen.getByText('共 250 条')).toBeInTheDocument()
    const panel = container.querySelector('[data-slot="panel"]')
    expect(panel).not.toHaveAttribute('footer')
    expect(panel!.children).toHaveLength(3)
  })
})
