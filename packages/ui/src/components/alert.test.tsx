import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'

import { Alert, AlertAction, AlertDescription, AlertTitle } from './alert'

/**
 * Alert 变体补齐（本轮）：
 * 初版只有 `default` / `destructive`，而主控台大量警告用的是项目 `status-warning`
 * 语义色。缺这档的直接后果是 40+ 处告警条全部手写——「带边框带图标的警告框」
 * 没有对应组件可用。现在补 `warning` / `success` / `info`，且一律取自既有
 * `--status-*` 色板，不引入新颜色变量。
 */
describe('Alert 语义变体', () => {
  it('四个语义变体都走项目既有的 status 色板，不引入硬编码颜色', () => {
    const { rerender } = render(<Alert variant="warning" data-testid="a" />)
    expect(screen.getByTestId('a').className).toContain('status-warning')

    rerender(<Alert variant="success" data-testid="a" />)
    expect(screen.getByTestId('a').className).toContain('status-success')

    rerender(<Alert variant="info" data-testid="a" />)
    expect(screen.getByTestId('a').className).toContain('primary')
  })

  it('任何变体都保持 role="alert"（读屏必须能播报）', () => {
    render(<Alert variant="warning" data-testid="a" />)
    expect(screen.getByTestId('a')).toHaveAttribute('role', 'alert')
  })

  it('标题与描述各带 data-slot 钩子', () => {
    render(
      <Alert variant="warning">
        <AlertTitle>注意</AlertTitle>
        <AlertDescription>配额接近上限</AlertDescription>
      </Alert>,
    )
    expect(document.querySelector('[data-slot="alert-title"]')).not.toBeNull()
    expect(document.querySelector('[data-slot="alert-description"]')).not.toBeNull()
  })
})

describe('AlertAction 定位可配置', () => {
  it('默认 inline：随文档流排布，不再钉死在右上角', () => {
    render(<AlertAction data-testid="act" />)
    const el = screen.getByTestId('act')
    expect(el).toHaveAttribute('data-placement', 'inline')
    expect(el.className).not.toContain('absolute')
  })

  it('corner：需要角标形态时才绝对定位', () => {
    render(<AlertAction placement="corner" data-testid="act" />)
    const el = screen.getByTestId('act')
    expect(el).toHaveAttribute('data-placement', 'corner')
    expect(el.className).toContain('absolute')
    expect(el.className).toContain('right-3')
  })
})
