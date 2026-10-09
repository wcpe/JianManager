import { beforeAll, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { NotificationBell, type NotificationFeedEntry } from './NotificationBell'

/**
 * FR-216 统一通知铃铛 · 受控视图测（ADR-097 c 范式）。
 *
 * 应用侧只验「合并入口能开能关」；这里补的是**未读徽标与跳转契约**：
 * 99+ 截断、空态、点条目按来源上报（站内信带 taskId / 告警 / 其余）、查看全部。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        header: {
          notifications: '通知', unreadCount: '{{count}} 条未读', viewAllNotifications: '查看全部通知',
        },
        notificationCenter: { empty: '暂无通知', badgeAlert: '告警', badgeMessage: '消息' },
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

function entry(over: Partial<NotificationFeedEntry> = {}): NotificationFeedEntry {
  return {
    id: 1, source: 'message', level: 'info', title: '标题', body: '正文',
    createdAt: '2025-01-01T00:00:00Z', read: false, ...over,
  }
}

function renderBell(props: Partial<Parameters<typeof NotificationBell>[0]> = {}) {
  const merged = { unread: 0, items: undefined as NotificationFeedEntry[] | undefined, onOpenItem: vi.fn(), onViewAll: vi.fn(), ...props }
  render(
    <I18nextProvider i18n={testI18n}>
      <NotificationBell {...merged} />
    </I18nextProvider> as ReactNode,
  )
  return merged
}

describe('NotificationBell（FR-216 统一通知铃铛）', () => {
  it('未读超过 99 时徽标截断为 99+。', () => {
    renderBell({ unread: 128 })
    const trigger = screen.getByRole('button', { name: '通知' })
    expect(within(trigger).getByText('99+')).toBeInTheDocument()
  })

  it('点条目按来源上报：任务类站内信、告警、其余各走各的。', async () => {
    const user = userEvent.setup()
    const props = renderBell({
      unread: 2,
      items: [
        entry({ id: 1, source: 'message', taskId: 't-9', title: '任务完成' }),
        entry({ id: 2, source: 'alert', level: 'error', title: '磁盘告警' }),
      ],
    })
    await user.click(screen.getByRole('button', { name: '通知' }))
    await user.click(await screen.findByText('任务完成'))
    expect(props.onOpenItem).toHaveBeenCalledWith(expect.objectContaining({ taskId: 't-9' }))
    await user.click(screen.getByText('磁盘告警'))
    expect(props.onOpenItem).toHaveBeenCalledWith(expect.objectContaining({ source: 'alert' }))
  })

  it('无条目时下拉显示空态，查看全部触发回调。', async () => {
    const user = userEvent.setup()
    const props = renderBell({ items: [] })
    await user.click(screen.getByRole('button', { name: '通知' }))
    expect(await screen.findByText('暂无通知')).toBeInTheDocument()
    await user.click(screen.getByText('查看全部通知'))
    expect(props.onViewAll).toHaveBeenCalled()
  })
})
