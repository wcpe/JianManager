import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { Button } from './button'

/**
 * Button 现状快照测试（FR-496 阶段 0）。
 *
 * 目的不是穷举行为，而是在重构前锁定对外契约：`data-*` 钩子、变体透传、
 * className 合并语义、asChild 降级。阶段 4 重做 Button 时，这些断言
 * 就是"行为不变"的安全网——重构可以改实现，但不能改这些对外可见的结果。
 */
describe('Button', () => {
  it('渲染原生 button 并带 data 钩子', () => {
    render(<Button>提交</Button>)

    const btn = screen.getByRole('button', { name: '提交' })
    expect(btn).toBeInTheDocument()
    expect(btn.tagName).toBe('BUTTON')
    expect(btn).toHaveAttribute('data-slot', 'button')
    expect(btn).toHaveAttribute('data-variant', 'default')
    expect(btn).toHaveAttribute('data-size', 'default')
  })

  it('按 variant / size 透传到 data 属性', () => {
    render(
      <Button variant="destructive" size="sm">
        删除
      </Button>,
    )

    const btn = screen.getByRole('button', { name: '删除' })
    expect(btn).toHaveAttribute('data-variant', 'destructive')
    expect(btn).toHaveAttribute('data-size', 'sm')
  })

  it('asChild 时渲染子元素但保留按钮外壳契约', () => {
    render(
      <Button asChild>
        <a href="/instances">实例列表</a>
      </Button>,
    )

    const link = screen.getByRole('link', { name: '实例列表' })
    expect(link).toHaveAttribute('href', '/instances')
    expect(link).toHaveAttribute('data-slot', 'button')
  })

  it('className 与变体类合并而非替换', () => {
    render(<Button className="custom-class">确定</Button>)

    const btn = screen.getByRole('button', { name: '确定' })
    expect(btn.className).toContain('custom-class')
    expect(btn.className).toContain('inline-flex')
  })

  it('转发原生属性与禁用态', () => {
    render(
      <Button type="submit" disabled>
        提交
      </Button>,
    )

    const btn = screen.getByRole('button', { name: '提交' })
    expect(btn).toHaveAttribute('type', 'submit')
    expect(btn).toBeDisabled()
  })
})

/**
 * FR-496 阶段 6 新增契约：加载态与提示。
 *
 * 都是"少做一步就静默出错"的语义——只转圈不禁用会重复提交、图标按钮没有可访问名
 * 屏幕阅读器只会念"按钮"、工具提示覆盖了可见文案则违反 WCAG 2.5.3。故逐条钉死。
 */
describe('Button 加载态与提示（FR-496 阶段 6）', () => {
  it('isLoading 同时给出禁用、aria-busy 与装饰性 Spinner', () => {
    render(<Button isLoading>保存</Button>)

    // 可访问名仍是「保存」：文案不跟着加载态改，否则正在操作的控件会从用户手里"消失"
    const btn = screen.getByRole('button', { name: '保存' })
    expect(btn).toBeDisabled()
    expect(btn).toHaveAttribute('aria-busy', 'true')
    expect(btn).toHaveAttribute('data-loading', 'true')

    const spinner = btn.querySelector('[data-slot="spinner"]')
    expect(spinner).not.toBeNull()
    // 指示器是装饰：加载状态由 aria-busy 表达，指示器再报一次只是噪音
    expect(spinner).toHaveAttribute('aria-hidden', 'true')
  })

  it('非加载态不带任何加载痕迹', () => {
    render(<Button>保存</Button>)

    const btn = screen.getByRole('button', { name: '保存' })
    expect(btn).not.toHaveAttribute('aria-busy')
    expect(btn).not.toHaveAttribute('data-loading')
    expect(btn.querySelector('[data-slot="spinner"]')).toBeNull()
    expect(btn).not.toBeDisabled()
  })

  it('已禁用且加载中时仍然是禁用的', () => {
    render(
      <Button disabled isLoading>
        保存
      </Button>,
    )

    expect(screen.getByRole('button', { name: '保存' })).toBeDisabled()
  })

  it('asChild + isLoading 不注入 Spinner（Slot 只接受单个子元素）', () => {
    render(
      <Button asChild isLoading>
        <a href="/instances">进入</a>
      </Button>,
    )

    const link = screen.getByRole('link', { name: '进入' })
    expect(link.querySelector('[data-slot="spinner"]')).toBeNull()
    expect(link).toHaveAttribute('aria-busy', 'true')
  })

  it('tooltip 落到原生 title（不加额外 DOM，避免动布局）', () => {
    render(<Button tooltip="按名称重新拉取实例">刷新</Button>)

    const btn = screen.getByRole('button', { name: '刷新' })
    expect(btn).toHaveAttribute('title', '按名称重新拉取实例')
  })

  it('图标按钮的 tooltip 同时作为可访问名', () => {
    render(
      <Button size="icon" tooltip="刷新">
        <span aria-hidden="true">⟳</span>
      </Button>,
    )

    // 没有这一步，屏幕阅读器只会念"按钮"
    expect(screen.getByRole('button', { name: '刷新' })).toBeInTheDocument()
  })

  it('带可见文案时 tooltip 不覆盖可访问名（WCAG 2.5.3 可见标签）', () => {
    render(<Button tooltip="保存并关闭">保存</Button>)

    expect(screen.getByRole('button', { name: '保存' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '保存并关闭' })).toBeNull()
  })

  it('调用点显式 aria-label 优先于 tooltip 推断', () => {
    render(
      <Button size="icon" tooltip="刷新" aria-label="刷新实例列表">
        <span aria-hidden="true">⟳</span>
      </Button>,
    )

    expect(screen.getByRole('button', { name: '刷新实例列表' })).toBeInTheDocument()
  })

  it('显式 title 优先于 tooltip', () => {
    render(
      <Button title="自定义提示" tooltip="tooltip 文本">
        刷新
      </Button>,
    )

    expect(screen.getByRole('button', { name: '刷新' })).toHaveAttribute('title', '自定义提示')
  })

  it('保留全部 8 个 size 与 6 个 variant 取值（139 处调用点的公共契约）', () => {
    const sizes = ['default', 'xs', 'sm', 'lg', 'icon', 'icon-xs', 'icon-sm', 'icon-lg'] as const
    const variants = ['default', 'destructive', 'outline', 'secondary', 'ghost', 'link'] as const

    for (const size of sizes) {
      const { unmount } = render(<Button size={size}>按钮</Button>)
      expect(screen.getByRole('button')).toHaveAttribute('data-size', size)
      unmount()
    }
    for (const variant of variants) {
      const { unmount } = render(<Button variant={variant}>按钮</Button>)
      expect(screen.getByRole('button')).toHaveAttribute('data-variant', variant)
      unmount()
    }
  })

  it('焦点环与按压反馈取共享常量（FR-496 阶段 6 收敛）', () => {
    render(<Button>提交</Button>)

    const className = screen.getByRole('button').className
    // FR-176 的细环取值：不得回退成 3px/50% 的粗环
    expect(className).toContain('focus-visible:ring-2')
    expect(className).toContain('focus-visible:ring-ring/40')
    expect(className).not.toContain('focus-visible:ring-[3px]')
    // 按压反馈：此前多数变体只有 hover 没有 active
    expect(className).toContain('active:bg-primary/80')
  })
})
