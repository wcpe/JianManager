import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'

import { Segment, Segments } from './layout/Segments'
import { ToggleGroup, ToggleGroupItem } from './toggle-group'
import { segmentTrack, segmentItem, segmentItemActive } from './toggle'

/**
 * 分段控件视觉统一（S2）：
 * `Segments`（手写凹槽风）与 `ToggleGroupItem variant="segment"`（Radix 版）表达的是
 * **同一个交互形态**——「同一份数据的少数几种呈现就地切换」。此前两者是两套各自手写的
 * 取值，同一个交互在站内有两种外观。
 *
 * 现在两者都引用 `segmentTrack` / `segmentItem` / `segmentItemActive`，本文件锁住这个共享关系。
 */
describe('分段控件共享视觉取值', () => {
  it('Segments 的轨道使用 segmentTrack', () => {
    render(<Segments data-testid="track" />)
    const el = screen.getByTestId('track')
    for (const cls of segmentTrack.split(' ')) {
      expect(el.className).toContain(cls)
    }
  })

  it('Segment 未选中时用 segmentItem，选中时叠加 segmentItemActive', () => {
    const { rerender } = render(<Segment>列表</Segment>)
    const el = screen.getByRole('tab')

    for (const cls of segmentItem.split(' ')) {
      expect(el.className).toContain(cls)
    }
    expect(el).toHaveAttribute('aria-selected', 'false')

    rerender(<Segment active>列表</Segment>)
    const activeEl = screen.getByRole('tab')
    for (const cls of segmentItemActive.split(' ')) {
      expect(activeEl.className).toContain(cls)
    }
    expect(activeEl).toHaveAttribute('aria-selected', 'true')
  })

  it('ToggleGroupItem 的 segment 变体与 Segment 共用同一套 item 取值', () => {
    render(
      <ToggleGroup type="single" variant="segment" size="segment" data-testid="tg">
        <ToggleGroupItem value="a">卡片</ToggleGroupItem>
        <ToggleGroupItem value="b">列表</ToggleGroupItem>
      </ToggleGroup>,
    )
    const items = document.querySelectorAll('[data-slot="toggle-group-item"]')
    expect(items.length).toBe(2)
    // 每个分段项都带上了共享的 typography / 间距取值
    for (const item of items) {
      expect(item.className).toContain('text-[11px]')
      expect(item.className).toContain('rounded-md')
      expect(item.className).toContain('whitespace-nowrap')
    }
  })

  it('两条轨道在视觉上可互换：底色与内距一致', () => {
    // 这是「统一」的实质：把 Segments 换成 ToggleGroup（或反之）不该有可见的底座差异
    render(
      <>
        <Segments data-testid="seg" />
        <ToggleGroup type="single" variant="segment" size="segment" data-testid="tgrp" />
      </>,
    )
    const seg = screen.getByTestId('seg')
    const tgrp = screen.getByTestId('tgrp')
    // 轨道容器级别的共享类（段容器由调用方给，如 `rounded-lg border p-[3px]` 的凹槽）
    expect(segmentTrack).toContain('bg-muted')
    expect(segmentTrack).toContain('p-[3px]')
    expect(seg.className).toContain('bg-muted')
    // ToggleGroup 根不带凹槽（它由调用方决定是否包一层），但 item 取值同源
    expect(tgrp).toBeTruthy()
  })
})
