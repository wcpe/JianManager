import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'

import { cardSurface, cardSurfaceTone } from '../lib/interaction-overlay'
import { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from './card'
import { Panel } from './panel'
import { StatCard } from './stat-card'

/**
 * 卡片体系一致性（S1）：
 * `Card`、`Panel`、`StatCard` 三者共用 `cardSurface` 常量，确保基础视觉（圆角/描边/
 * 底色/阴影）由一处决定。此前的状况是三处各自手写同一串类名——巧合一致但没有保证，
 * 改圆角漏一处就静默漂移，这正是「整体样式不统一」的来源。
 *
 * 这些用例锁的是**共享关系**而非具体取值：只要三者仍指向同一常量，改常量不会让它们分叉。
 */
describe('卡片体系共享基础表面', () => {
  it('Card 使用共享的 cardSurface', () => {
    render(<Card data-testid="card" />)
    const el = screen.getByTestId('card')
    for (const cls of cardSurface.split(' ')) {
      expect(el.className).toContain(cls)
    }
  })

  it('Panel 使用共享的 cardSurface', () => {
    render(<Panel data-testid="panel" />)
    const el = screen.getByTestId('panel')
    for (const cls of cardSurface.split(' ')) {
      expect(el.className).toContain(cls)
    }
  })

  it('StatCard 使用共享的 cardSurface', () => {
    render(<StatCard label="实例" value={42} />)
    const el = screen.getByText('实例').closest('[data-slot="stat-card"], *')!
    // 向上找到真正的卡片根（带 cardSurface 的那个）
    let root: HTMLElement | null = el as HTMLElement
    while (root && !cardSurface.split(' ').every((c) => root!.className.includes(c))) {
      root = root.parentElement
    }
    expect(root).not.toBeNull()
  })

  it('cardSurface 自身描述结构与边框，底色由 cardSurfaceTone 分档提供', () => {
    // 圆角 / 描边 / 前景色 / 阴影：这四项是每张卡片都固定的
    expect(cardSurface).toContain('rounded-')
    expect(cardSurface).toContain('border')
    expect(cardSurface).toContain('text-card-foreground')
    expect(cardSurface).toContain('shadow-')
    // 底色不再是单一取值，而是按承载关系分三档（solid/subtle/faint）
    expect(cardSurface).not.toContain('bg-card')
    expect(Object.keys(cardSurfaceTone)).toEqual(['solid', 'subtle', 'faint'])
  })

  it('surface 档位影响底色，且默认是 solid', () => {
    const { rerender } = render(<Card data-testid="c" />)
    expect(screen.getByTestId('c').className).toContain('bg-card/95')

    rerender(<Card data-testid="c" surface="faint" />)
    expect(screen.getByTestId('c').className).toContain('bg-card/40')
    expect(screen.getByTestId('c')).toHaveAttribute('data-surface', 'faint')
  })
})

describe('Card 五段结构', () => {
  it('各段都带 data-slot 钩子，页面可精确命中', () => {
    render(
      <Card>
        <CardHeader>
          <CardTitle>标题</CardTitle>
          <CardDescription>说明</CardDescription>
          <CardAction>
            <button>操作</button>
          </CardAction>
        </CardHeader>
        <CardContent>内容</CardContent>
        <CardFooter>底部</CardFooter>
      </Card>,
    )
    expect(document.querySelector('[data-slot="card-header"]')).not.toBeNull()
    expect(document.querySelector('[data-slot="card-title"]')).not.toBeNull()
    expect(document.querySelector('[data-slot="card-description"]')).not.toBeNull()
    expect(document.querySelector('[data-slot="card-action"]')).not.toBeNull()
    expect(document.querySelector('[data-slot="card-content"]')).not.toBeNull()
    expect(document.querySelector('[data-slot="card-footer"]')).not.toBeNull()
  })

  it('interactive 时叠加 hover 与焦点环，非 interactive 时不叠加', () => {
    const { rerender } = render(<Card data-testid="c" />)
    expect(screen.getByTestId('c').className).not.toContain('hover:border-primary')

    rerender(<Card data-testid="c" interactive />)
    const cls = screen.getByTestId('c').className
    expect(cls).toContain('hover:border-primary')
    expect(cls).toContain('focus-visible:ring-2')
  })
})
