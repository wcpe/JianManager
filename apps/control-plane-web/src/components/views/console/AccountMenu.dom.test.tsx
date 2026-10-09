import { beforeAll, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { AccountMenu } from '@/components/views/console/AccountMenu'

/**
 * FR-162 账户菜单 · 受控视图测（ADR-097 c 范式）。
 *
 * 补的是**账户态呈现与退出上报**：用户名/角色文案（复用包内 roles 的 ROLE_LABEL_KEY）、
 * 未知角色不显示角色行、退出经回调上报。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        header: { account: '账户' },
        common: { logout: '退出登录' },
        users: {
          member: '成员', groupAdmin: '组管理员', groupOperator: '组操作员',
          groupViewer: '组查看者', platformAdmin: '平台管理员',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

beforeAll(() => {
  globalThis.ResizeObserver ??= class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

function renderMenu(props: Partial<Parameters<typeof AccountMenu>[0]> = {}) {
  const merged = { username: 'alice', role: 10, onLogout: vi.fn(), ...props }
  render(
    <I18nextProvider i18n={testI18n}>
      <AccountMenu {...merged} />
    </I18nextProvider> as ReactNode,
  )
  return merged
}

describe('AccountMenu（FR-162 账户菜单）', () => {
  it('展开后显示用户名与角色文案（角色文案与用户管理页同源）。', async () => {
    const user = userEvent.setup()
    renderMenu()
    await user.click(screen.getByRole('button', { name: '账户' }))
    // 用户名在触发按钮与浮窗里各出现一次，故用 findAllByText 取浮窗内那份。
    const names = await screen.findAllByText('alice')
    expect(names.length).toBeGreaterThan(1)
    expect(screen.getByText('平台管理员')).toBeInTheDocument()
  })

  it('角色未知时不显示角色行，用户名未加载时显示占位。', async () => {
    const user = userEvent.setup()
    renderMenu({ username: null, role: null })
    await user.click(screen.getByRole('button', { name: '账户' }))
    expect(await screen.findByText('—')).toBeInTheDocument()
    expect(screen.queryByText('平台管理员')).not.toBeInTheDocument()
  })

  it('点退出登录经回调上报。', async () => {
    const user = userEvent.setup()
    const props = renderMenu()
    await user.click(screen.getByRole('button', { name: '账户' }))
    await user.click(await screen.findByText('退出登录'))
    expect(props.onLogout).toHaveBeenCalled()
  })
})
