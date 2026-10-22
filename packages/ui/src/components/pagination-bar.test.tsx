import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { PaginationBar } from './pagination'

/**
 * 受控分页器（本轮新增）。
 *
 * 主控台所有分页都是「`page` state + `onChange` 回调」，不是链接跳转。
 * 初版 `Pagination` 是纯链接导航（渲染 `<a href>`），套到受控场景只能把 `<a>` 当按钮用，
 * 既别扭又留下一个假链接。本组件是真正的受控版本。
 */
describe('PaginationBar 受控翻页', () => {
  it('点击上一页/下一页回调目标页', async () => {
    const onChange = vi.fn()
    render(<PaginationBar page={2} totalPages={5} onChange={onChange} />)

    await userEvent.click(screen.getByRole('button', { name: '上一页' }))
    expect(onChange).toHaveBeenLastCalledWith(1)

    await userEvent.click(screen.getByRole('button', { name: '下一页' }))
    expect(onChange).toHaveBeenLastCalledWith(3)
  })

  it('首页禁用上一页、末页禁用下一页（防止越界请求）', () => {
    const { rerender } = render(<PaginationBar page={1} totalPages={3} onChange={() => {}} />)
    expect(screen.getByRole('button', { name: '上一页' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '下一页' })).toBeEnabled()

    rerender(<PaginationBar page={3} totalPages={3} onChange={() => {}} />)
    expect(screen.getByRole('button', { name: '上一页' })).toBeEnabled()
    expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled()
  })

  it('单页时两侧都禁用', () => {
    render(<PaginationBar page={1} totalPages={1} onChange={() => {}} />)
    expect(screen.getByRole('button', { name: '上一页' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled()
  })

  it('toalLabel / pageLabel 使用调用方传入的已格式化文案（i18n 在调用方）', () => {
    render(
      <PaginationBar
        page={2}
        totalPages={5}
        total={48}
        onChange={() => {}}
        totalLabel="共 48 条"
        pageLabel="第 2 / 5 页"
      />,
    )
    expect(screen.getByText('共 48 条')).toBeInTheDocument()
    expect(screen.getByText('第 2 / 5 页')).toBeInTheDocument()
  })

  it('省略文案时回退到默认拼接', () => {
    render(<PaginationBar page={2} totalPages={5} onChange={() => {}} />)
    expect(screen.getByText('2 / 5')).toBeInTheDocument()
  })

  it('渲染的是按钮而非链接（受控场景不该有假 href）', () => {
    render(<PaginationBar page={2} totalPages={5} onChange={() => {}} />)
    expect(screen.queryAllByRole('link')).toHaveLength(0)
    expect(screen.getAllByRole('button')).toHaveLength(2)
  })

  it('total 未传时不渲染总数区', () => {
    const { container } = render(<PaginationBar page={1} totalPages={3} onChange={() => {}} />)
    expect(container.querySelector('[data-slot="pagination-bar"]')?.textContent).not.toContain('共')
  })
})
