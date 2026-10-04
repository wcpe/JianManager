import * as React from 'react'
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { Input } from './input'

/**
 * Input 现状快照测试（FR-496 阶段 0）。
 *
 * Input 是薄封装的原生 `<input>`，它的价值全在"封装之后仍然像原生输入框"：type / name /
 * disabled / aria-* 等原生属性必须原样透传，受控与非受控两种用法都必须按浏览器语义工作。
 * 阶段 4 若把它改造成"带内部状态的自定义控件"，最先崩的就是这些契约，故此处逐一锁定：
 * `data-slot` 钩子、原生属性转发、受控 value + onChange、非受控 defaultValue、
 * className 合并（弱断言）与 ref 指向真实元素。
 *
 * 不断言具体样式类串——那属于样式实现，重构时会变。
 */
describe('Input', () => {
  it('渲染原生 input 并带 data-slot 钩子', () => {
    render(<Input placeholder="实例名称" />)

    const input = screen.getByPlaceholderText('实例名称')
    expect(input.tagName).toBe('INPUT')
    expect(input).toHaveAttribute('data-slot', 'input')
    // 未指定 type 时按浏览器默认暴露成文本输入框（辅助技术可见角色）
    expect(screen.getByRole('textbox')).toBe(input)
  })

  it('type 透传到原生元素', () => {
    render(<Input type="password" aria-label="管理密码" />)

    const input = screen.getByLabelText('管理密码')
    expect(input).toHaveAttribute('type', 'password')
    // 密码框不再具备 textbox 角色，说明 type 确实落到了原生元素上
    expect(screen.queryByRole('textbox')).toBeNull()
  })

  it('受控模式下 value 由父级决定，输入触发 onChange', () => {
    function Controlled() {
      const [value, setValue] = React.useState('初始值')
      return <Input value={value} onChange={(e) => setValue(e.target.value)} />
    }
    render(<Controlled />)

    const input = screen.getByDisplayValue('初始值') as HTMLInputElement
    fireEvent.change(input, { target: { value: '实例 A' } })
    expect(input).toHaveValue('实例 A')
  })

  it('非受控模式下 defaultValue 就地更新', () => {
    render(<Input defaultValue="初始值" />)

    const input = screen.getByDisplayValue('初始值') as HTMLInputElement
    expect(input).toHaveValue('初始值')

    fireEvent.change(input, { target: { value: '改动后' } })
    expect(input).toHaveValue('改动后')
  })

  it('转发原生约束与状态属性', () => {
    render(
      <Input name="instanceName" required disabled readOnly maxLength={32} aria-invalid="true" />,
    )

    const input = screen.getByRole('textbox') as HTMLInputElement
    expect(input).toHaveAttribute('name', 'instanceName')
    expect(input).toBeRequired()
    expect(input).toBeDisabled()
    expect(input).toHaveAttribute('readonly')
    expect(input).toHaveAttribute('maxlength', '32')
    // aria-invalid 是错误态描红（aria-invalid: 变体）依赖的对外属性，必须原样透传
    expect(input).toHaveAttribute('aria-invalid', 'true')
  })

  it('className 与基础类合并而非替换', () => {
    render(<Input className="custom-input" />)

    const input = screen.getByRole('textbox') as HTMLInputElement
    expect(input.className).toContain('custom-input')
    expect(input.className).toContain('w-full')
  })

  it('ref 指向真实 input 元素', () => {
    const ref = React.createRef<HTMLInputElement>()
    render(<Input ref={ref} />)

    expect(ref.current).not.toBeNull()
    expect(ref.current?.tagName).toBe('INPUT')
  })
})

/**
 * FR-496 阶段 6 新增契约：状态样式取值收敛到共享常量。
 *
 * 这三条守的都是「样式不能各自为政」：焦点环回退成粗环会糊到邻近文字（FR-176），
 * 无效态不描红则用户填错了也看不出来，禁用态丢掉光标提示会让人反复点击。
 */
describe('Input 状态样式收敛（FR-496 阶段 6）', () => {
  it('焦点环取共享细环常量（FR-176 不得回退成粗环）', () => {
    render(<Input />)

    const className = screen.getByRole('textbox').className
    expect(className).toContain('focus-visible:ring-2')
    expect(className).toContain('focus-visible:ring-ring/40')
    expect(className).toContain('focus-visible:border-ring')
    expect(className).not.toContain('focus-visible:ring-[3px]')
    expect(className).not.toContain('focus-visible:ring-ring/50')
  })

  it('aria-invalid 描红与危险色同源（失焦时也保持红色，不依赖 :focus-visible）', () => {
    render(<Input aria-invalid="true" />)

    const className = screen.getByRole('textbox').className
    expect(className).toContain('aria-invalid:border-destructive')
    expect(className).toContain('aria-invalid:ring-destructive/20')
    expect(className).toContain('dark:aria-invalid:ring-destructive/40')
  })

  it('禁用态屏蔽指针事件并保留 not-allowed 光标', () => {
    render(<Input disabled />)

    const className = screen.getByRole('textbox').className
    expect(className).toContain('disabled:pointer-events-none')
    expect(className).toContain('disabled:cursor-not-allowed')
    expect(className).toContain('disabled:opacity-50')
  })
})
