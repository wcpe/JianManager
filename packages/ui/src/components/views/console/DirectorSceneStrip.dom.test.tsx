import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ComponentProps, ReactNode } from 'react'
import { DirectorSceneStrip } from './DirectorSceneStrip'
import { createDirectorState, type DirectorState } from '@jianmanager/ui/lib/director'

/**
 * 导播台缩略图条 · 受控视图测（ADR-097）。
 *
 * 补的是**展示契约**：场景缩略卡的序号/名称/三态点、激活与删除回调、并发上限滑块。
 * store 联动（持久化、LRU 驱逐）由应用侧与 `director.test.ts` 覆盖。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        director: {
          unnamedScene: '未命名场景 {{n}}',
          preheatLimit: '并发上限',
          preheatLimitHint: '最多 {{max}} 个保活连接',
          switchTo: '切到 {{name}}',
          removeScene: '删除 {{name}}',
          statusActive: '直播中',
          statusPreheated: '已预热',
          statusCold: '冷',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

/** 两场景状态机：s1 激活、s2 预热（limit=3）。 */
function machineWithBoth(): DirectorState {
  const base = createDirectorState(['s1', 's2'], 3)
  // 直接构造快照：active=s1，预热顺序含 s2。
  return { ...base, activeId: 's1', preheatOrder: ['s1', 's2'] }
}

function renderStrip(props: Partial<ComponentProps<typeof DirectorSceneStrip>> = {}) {
  const onActivate = vi.fn()
  const onRemove = vi.fn()
  const onLimitChange = vi.fn()
  const merged = {
    scenes: [
      { id: 's1', name: '主舞台' },
      { id: 's2', name: '备用舞台' },
    ],
    machine: machineWithBoth(),
    onActivate,
    onRemove,
    onLimitChange,
    ...props,
  }
  render(
    <I18nextProvider i18n={testI18n}>
      <DirectorSceneStrip {...merged} />
    </I18nextProvider> as ReactNode,
  )
  return { merged, onActivate, onRemove, onLimitChange }
}

describe('DirectorSceneStrip（FR-168 导播台缩略图条受控视图）', () => {
  it('渲染场景卡的序号、名称与三态点标签。', () => {
    renderStrip()
    expect(screen.getByRole('button', { name: '切到 主舞台' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '切到 备用舞台' })).toBeInTheDocument()
    // 三态点：active / preheated 各一。
    expect(screen.getByLabelText('直播中')).toBeInTheDocument()
    expect(screen.getByLabelText('已预热')).toBeInTheDocument()
  })

  it('未命名场景按序号回退显示名。', () => {
    renderStrip({ scenes: [{ id: 's1', name: '' }] })
    expect(screen.getByText('未命名场景 1')).toBeInTheDocument()
  })

  it('点击缩略卡走注入的激活回调，激活卡 aria-pressed=true。', async () => {
    const user = userEvent.setup()
    const { onActivate } = renderStrip()
    expect(screen.getByRole('button', { name: '切到 主舞台' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: '切到 备用舞台' })).toHaveAttribute('aria-pressed', 'false')

    await user.click(screen.getByRole('button', { name: '切到 备用舞台' }))
    expect(onActivate).toHaveBeenCalledWith('s2')
  })

  it('键盘 Enter 同样触发激活（可聚焦卡片）。', async () => {
    const user = userEvent.setup()
    const { onActivate } = renderStrip()
    const card = screen.getByRole('button', { name: '切到 备用舞台' })
    card.focus()
    await user.keyboard('{Enter}')
    expect(onActivate).toHaveBeenCalledWith('s2')
  })

  it('删除按钮走注入的移除回调，且不触发激活。', async () => {
    const user = userEvent.setup()
    const { onRemove, onActivate } = renderStrip()
    await user.click(screen.getByRole('button', { name: '删除 主舞台' }))
    expect(onRemove).toHaveBeenCalledWith('s1')
    expect(onActivate).not.toHaveBeenCalled()
  })

  it('并发上限滑块显示「已预热/上限」并回传新值。', async () => {
    const { onLimitChange } = renderStrip()
    const slider = screen.getByRole('slider', { name: '并发上限' })
    expect(slider).toHaveValue('3')
    expect(screen.getByText('2/3')).toBeInTheDocument()

    // 滑块 range 值直接改（userEvent 对 range 输入支持有限）。
    const { fireEvent } = await import('@testing-library/react')
    fireEvent.change(slider, { target: { value: '5' } })
    expect(onLimitChange).toHaveBeenCalledWith(5)
  })
})
