import { describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import '@/i18n'
import { PathOverridesEditor } from './PathOverridesEditor'
import { emptyRow, type PathOverrideRow } from '@/lib/settings/path-overrides'

const options = [
  { value: '1', label: 'survival' },
  { value: '7', label: 'proxy' },
]

const row = (instanceId: string, path: string): PathOverrideRow => ({
  key: `k-${instanceId}`,
  instanceId,
  path,
})

function setup(rows: PathOverrideRow[]) {
  const onChange = vi.fn()
  render(<PathOverridesEditor rows={rows} onChange={onChange} options={options} />)
  return { onChange }
}

describe('PathOverridesEditor', () => {
  it('空态给出说明而不是空白（告诉用户什么时候才需要配）', () => {
    render(<PathOverridesEditor rows={[]} onChange={vi.fn()} options={options} />)
    expect(screen.getByTestId('path-overrides-editor')).toHaveTextContent('未配置覆盖')
  })

  it('每行一个实例下拉 + 一个路径输入 + 一个删除按钮', () => {
    setup([row('1', 'logs/a.log')])
    const r = screen.getByTestId('path-override-row-k-1')
    /*
      实例选择用 Combobox 而非裸 Input：它不把 data-testid 透传到内部触发器
      （见 packages/ui 的 ComboboxProps），所以按可访问名定位。
      这也顺带证明了"下拉确实渲染了"，而不只是"某个 div 存在"。
    */
    expect(within(r).getByRole('button', { name: '实例' })).toBeInTheDocument()
    expect(within(r).getByTestId('path-override-path-k-1')).toHaveValue('logs/a.log')
    expect(within(r).getByTestId('path-override-remove-k-1')).toBeInTheDocument()
  })

  it('改路径会回报整组新行', async () => {
    const { onChange } = setup([row('1', 'logs/a.log')])
    const input = screen.getByTestId('path-override-path-k-1')
    /*
      这是**受控**组件：值只来自 props。测试里不接 onChange 的回报去更新 props，
      所以连续 clear + type 会累积（"logs/a.log" + "x.log"）。
      这里只断言"每次输入都把新值回报出去了"，不假装它自己会同步。
      真正的受控回路由调用方（采集面板）负责，那侧的集成断言见 Panel 测试。
    */
    await userEvent.type(input, 'x')
    expect(onChange).toHaveBeenCalled()
    const last = onChange.mock.calls.at(-1)?.[0] as PathOverrideRow[]
    expect(last[0]?.path).toBe('logs/a.logx')
  })

  it('点「添加实例覆盖」追加一个空行', async () => {
    const { onChange } = setup([row('1', 'logs/a.log')])
    await userEvent.click(screen.getByTestId('path-overrides-add'))
    const last = onChange.mock.calls.at(-1)?.[0] as PathOverrideRow[]
    expect(last).toHaveLength(2)
    expect(last[1]?.instanceId).toBe('')
  })

  it('点删除移除该行', async () => {
    const { onChange } = setup([row('1', 'a.log'), row('7', 'b.log')])
    await userEvent.click(screen.getByTestId('path-override-remove-k-1'))
    const last = onChange.mock.calls.at(-1)?.[0] as PathOverrideRow[]
    expect(last).toHaveLength(1)
    expect(last[0]?.instanceId).toBe('7')
  })

  it('非法路径当场红框 + 行内错误（不等提交）', () => {
    setup([row('1', '/abs/path.log')])
    const input = screen.getByTestId('path-override-path-k-1')
    expect(input).toHaveAttribute('aria-invalid', 'true')
    // 行内错误文案（FieldError）应出现，而不是只在 toast 里。
    expect(screen.getByText(/相对实例工作目录/)).toBeInTheDocument()
  })

  it('重复实例给出提示（提交会丢一条，必须提交前可见）', () => {
    setup([row('1', 'a.log'), { key: 'k-1b', instanceId: '1', path: 'b.log' }])
    expect(screen.getByTestId('path-override-dup-k-1')).toBeInTheDocument()
  })

  it('有非法项时整表标记为不可保存', () => {
    setup([row('1', '/abs.log')])
    expect(screen.getByTestId('path-overrides-invalid')).toBeInTheDocument()
  })

  it('全部合法时不显示"需修正"提示', () => {
    setup([row('1', 'logs/a.log')])
    expect(screen.queryByTestId('path-overrides-invalid')).not.toBeInTheDocument()
  })

  it('没有实例清单时降级为手填 ID（节点离线也不能让编辑器不可用）', () => {
    render(<PathOverridesEditor rows={[emptyRow()]} onChange={vi.fn()} options={[]} />)
    // 降级成 Input 后仍然可以输入
    const rows = screen.getAllByTestId(/path-override-instance-/)
    expect(rows.length).toBe(1)
    expect(rows[0]?.tagName).toBe('INPUT')
  })
})
