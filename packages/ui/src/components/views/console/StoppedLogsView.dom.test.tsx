import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ComponentProps, ReactNode } from 'react'
import { StoppedLogsView } from './StoppedLogsView'
import type { LogEntry } from '@jianmanager/ui/lib/console-log-types'

/**
 * 停机日志回放 · 受控视图测（ADR-097）。
 *
 * 补的是**展示契约**：加载/空态、旧→新顺序、级别着色、复制回执经 onNotify、
 * 「完整历史」链接经 renderLink 注入。取数（useLogs）由应用侧接线层承担。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { loading: '加载中…', copied: '已复制', copyFailed: '复制失败' },
        instanceDetail: {
          stoppedShowingHistory: '实例未运行（{{status}}），显示历史日志',
          consoleCopyLogs: '复制日志',
          viewFullHistory: '查看完整历史',
          noHistoryLogs: '暂无历史日志',
          consoleCopyNothing: '没有可复制的内容',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

function entry(over: Partial<LogEntry> = {}): LogEntry {
  return {
    id: 1,
    source: 'instance',
    level: 'info',
    instanceId: 2,
    instanceUuid: 'stopped-2',
    nodeId: 1,
    message: 'hello',
    time: '2026-07-18T12:00:01Z',
    ...over,
  }
}

function renderView(props: Partial<ComponentProps<typeof StoppedLogsView>> = {}) {
  const onNotify = vi.fn()
  const renderLink = vi.fn(({ to, className, children }: { to: string; className?: string; children: ReactNode }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ))
  const merged = { instanceId: 2, status: 'STOPPED', entries: [entry()], isLoading: false, onNotify, renderLink, ...props }
  render(
    <I18nextProvider i18n={testI18n}>
      <StoppedLogsView {...merged} />
    </I18nextProvider> as ReactNode,
  )
  return { ...merged, onNotify, renderLink }
}

describe('StoppedLogsView（FR-345 停机日志回放受控视图）', () => {
  it('标题写明当前状态，正文按旧→新展示（后端倒序已反转）。', () => {
    renderView({
      entries: [
        entry({ id: 2, message: 'newer', time: '2026-07-18T12:00:02Z' }),
        entry({ id: 1, message: 'older', time: '2026-07-18T12:00:01Z' }),
      ],
    })
    expect(screen.getByText('实例未运行（STOPPED），显示历史日志')).toBeInTheDocument()
    const older = screen.getByText('older')
    const newer = screen.getByText('newer')
    expect(older.compareDocumentPosition(newer) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
  })

  it('加载态显示加载文案。', () => {
    renderView({ isLoading: true, entries: undefined })
    expect(screen.getByText('加载中…')).toBeInTheDocument()
  })

  it('无历史日志时给居中空态卡片。', () => {
    renderView({ entries: [] })
    expect(screen.getByText('暂无历史日志')).toBeInTheDocument()
  })

  it('级别着色：error 红、warn 琥珀，info 不着色。', () => {
    renderView({
      entries: [entry({ id: 1, level: 'error', message: 'boom' }), entry({ id: 2, level: 'warn', message: 'careful' })],
    })
    expect(screen.getByText('boom').className).toContain('text-red-400')
    expect(screen.getByText('careful').className).toContain('text-amber-400')
  })

  it('「查看完整历史」经注入的 renderLink 渲染（包内不依赖路由）。', () => {
    const { renderLink } = renderView()
    expect(renderLink).toHaveBeenCalled()
    expect(screen.getByRole('link', { name: '查看完整历史' })).toHaveAttribute('href', '/logs?instanceId=2')
  })

  it('未注入 renderLink 时不渲染该入口（不产生死链接）。', () => {
    renderView({ renderLink: undefined })
    expect(screen.queryByRole('link', { name: '查看完整历史' })).toBeNull()
  })

  it('复制成功经 onNotify 回执。', async () => {
    const user = userEvent.setup()
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: vi.fn(async () => undefined) },
    })
    const { onNotify } = renderView({ entries: [entry({ message: 'line-a' })] })
    await user.click(screen.getByRole('button', { name: /复制日志/ }))
    await vi.waitFor(() => expect(onNotify).toHaveBeenCalledWith('success', '已复制'))
  })

  it('无内容可复制时给失败回执，不静默。', async () => {
    const user = userEvent.setup()
    const { onNotify } = renderView({ entries: [] })
    await user.click(screen.getByRole('button', { name: /复制日志/ }))
    expect(onNotify).toHaveBeenCalledWith('error', '没有可复制的内容')
  })
})
