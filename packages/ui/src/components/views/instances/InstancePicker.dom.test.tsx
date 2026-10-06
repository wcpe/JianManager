import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { INSTANCE_PICKER_ALL, InstancePicker } from './InstancePicker'

/**
 * 千级实例选择器 · 受控视图测（ADR-097 a 范式）。
 *
 * 本组件在应用侧原本没有测试，故这里是**新补**而非迁移。断言聚焦三件本组件自己负责的事：
 * 候选窗口的呈现、截断提示、以及「当前值不在窗口内」时的回显兜底——服务端搜索与防抖属
 * 外壳策略，不在本测范围。
 *
 * 触发器用 `aria-label` 定位而非 `getByRole('combobox')`：Combobox 的触发器是 Popover
 * 按钮而非原生 select，其可访问名来自显式 ariaLabel（同一处出现多个选择器时正是靠它区分）。
 * 文案取自 zh.json 原值——截断提示的断言依赖插值，必须给真实模板。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: {
          all: '全部',
          selectPlaceholder: '请选择',
          searchTruncated: '已显示前 {{shown}} 项，共 {{total}} 项；继续输入可缩小范围',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

const items = [
  { id: 3, uuid: 'u-3', name: 'creative-1' },
  { id: 21, uuid: 'u-21', name: 'creative-plot' },
]

function renderPicker(props: Partial<Parameters<typeof InstancePicker>[0]> = {}) {
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
  }
  return render(<InstancePicker items={items} value={null} ariaLabel="选择实例" {...props} />, { wrapper: Wrapper })
}

describe('InstancePicker（ADR-097 a 范式）', () => {
  it('渲染触发器并带显式可访问名', () => {
    renderPicker()
    expect(screen.getByLabelText('选择实例')).toBeInTheDocument()
  })

  it('服务端截断时提示引导键入', () => {
    renderPicker({ total: 1200 })
    expect(screen.getByText(/共 1200 项/)).toBeInTheDocument()
  })

  it('未截断时不提示', () => {
    renderPicker({ total: items.length })
    expect(screen.queryByText(/共 2 项/)).not.toBeInTheDocument()
  })

  it('「全部」哨兵与候选一起出现在选项列表里', async () => {
    const user = userEvent.setup()
    renderPicker({ allowAll: true, allLabel: '全部实例' })

    await user.click(screen.getByLabelText('选择实例'))
    expect((await screen.findAllByText('全部实例')).length).toBeGreaterThanOrEqual(1)
    expect(screen.getByText('creative-1')).toBeInTheDocument()
    expect(screen.getByText('creative-plot')).toBeInTheDocument()
  })

  it('当前值不在候选窗口内时，用 valueLabel 兜底回显而非裸 id', async () => {
    const user = userEvent.setup()
    // value=99 不在 items 里：若不兜底，触发器会显示 "99"。
    renderPicker({ value: 99, valueLabel: 'survival-01' })

    const trigger = screen.getByLabelText('选择实例')
    expect(trigger).toHaveTextContent('survival-01')
    expect(trigger).not.toHaveTextContent('99')

    await user.click(trigger)
    // 兜底项也在选项里（选中态因此能正确高亮）。
    expect((await screen.findAllByText('survival-01')).length).toBeGreaterThanOrEqual(1)
  })

  it('allowAll 关闭且未选中时显示占位文案，而非哨兵值', () => {
    renderPicker({ placeholder: '请选择实例' })
    const trigger = screen.getByLabelText('选择实例')
    expect(trigger).toHaveTextContent('请选择实例')
    expect(trigger).not.toHaveTextContent(INSTANCE_PICKER_ALL)
  })
})
