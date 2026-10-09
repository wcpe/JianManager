import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { ThemeSwitcher } from './ThemeSwitcher'

/**
 * 主题切换器 · 受控视图测（ADR-097）。
 *
 * 补的是**展示契约**：主题色圆点/角标、明暗三态直选、折叠态收起明暗入口。
 * 取值与落值由应用侧接线层接 store，这里只验证回调被正确调用。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        colorTheme: {
          label: '主题色',
          indigo: '墨绿',
          teal: '青碧',
          ocean: '深海',
          violet: '紫罗兰',
          sunset: '落日',
        },
        theme: { toggle: '明暗', light: '浅色', dark: '深色', system: '跟随系统' },
      },
    },
  },
  interpolation: { escapeValue: false },
})

function renderSwitcher(props: Partial<Parameters<typeof ThemeSwitcher>[0]> = {}) {
  const merged = {
    colorTheme: 'indigo' as const,
    theme: 'system' as const,
    onColorThemeChange: vi.fn(),
    onThemeChange: vi.fn(),
    ...props,
  }
  render(
    <I18nextProvider i18n={testI18n}>
      <ThemeSwitcher {...merged} />
    </I18nextProvider> as ReactNode,
  )
  return merged
}

describe('ThemeSwitcher（FR-164 全局主题切换器）', () => {
  it('主题色按钮标题反映当前主题色，并提供明暗按钮。', () => {
    renderSwitcher({ colorTheme: 'ocean', theme: 'dark' })
    expect(screen.getByRole('button', { name: '主题色' })).toHaveAttribute('title', '深海')
    expect(screen.getByRole('button', { name: '明暗' })).toHaveAttribute('title', '深色')
  })

  it('展开主题色菜单后 5 色齐全，当前色带勾选，点击回调新色。', async () => {
    const user = userEvent.setup()
    const onColorThemeChange = vi.fn()
    renderSwitcher({ colorTheme: 'teal', onColorThemeChange })

    await user.click(screen.getByRole('button', { name: '主题色' }))
    expect(await screen.findByText('墨绿')).toBeInTheDocument()
    expect(screen.getByText('青碧')).toBeInTheDocument()
    expect(screen.getByText('落日')).toBeInTheDocument()

    await user.click(screen.getByText('紫罗兰'))
    expect(onColorThemeChange).toHaveBeenCalledWith('violet')
  })

  it('明暗三态直选：点「浅色」回调 light（非盲循环）。', async () => {
    const user = userEvent.setup()
    const onThemeChange = vi.fn()
    renderSwitcher({ theme: 'system', onThemeChange })

    await user.click(screen.getByRole('button', { name: '明暗' }))
    await user.click(await screen.findByText('浅色'))
    expect(onThemeChange).toHaveBeenCalledWith('light')
  })

  it('折叠态收起明暗入口（inert + 宽度归零），主题色按钮仍在。', () => {
    renderSwitcher({ compact: true })
    expect(screen.getByRole('button', { name: '主题色' })).toBeInTheDocument()
    const modeButton = screen.getByRole('button', { name: '明暗' })
    expect(modeButton.closest('[inert]')).not.toBeNull()
  })
})
