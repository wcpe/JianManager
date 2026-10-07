import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ComponentProps, ReactNode } from 'react'
import { TerminalPane } from './TerminalPane'
import type { ConsoleHistoryState } from '@jianmanager/ui/lib/console-history'
import { terminalSessionManager } from '@jianmanager/ui/lib/terminal-session-manager'

/**
 * 终端面板 · 受控视图测（ADR-097）。
 *
 * 补的是**注入面契约**：状态由 props 决定分支（停机 → 注入的日志回放、未知 → 不连 WS）、
 * 凭据失败态、权限禁用口径、隐藏工具栏。真实连接与重连纵切由应用侧用例覆盖。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { loading: '加载中…', error: '出错了' },
        console: { title: '控制台', split: '分屏', splitSoon: '即将支持' },
        permissions: { terminalDenied: '没有终端访问权限' },
        instanceDetail: {
          terminalConnectFailed: '终端连接失败',
          terminalWritable: '可写',
          terminalReadOnlyBadge: '只读',
          consoleInputDisabled: '实例未运行（{{status}}），命令输入已禁用',
          consoleStartInstance: '启动实例',
          consoleCommandLabel: '控制台命令输入',
          consoleCommandPlaceholder: '输入命令…',
          consoleCommandSend: '发送',
          consoleCommandDisabledPlaceholder: '命令输入已禁用',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

/** 回溯控制器 stub：恒空（本文件只验证面板自身的分支，不涉及回溯交互）。 */
function historyStub(): ConsoleHistoryState {
  return {
    lines: [],
    rowCount: 0,
    loading: false,
    exhausted: false,
    error: null,
    loadEarlier: vi.fn(),
    loadUntil: vi.fn(async () => 'loaded' as never),
    findStartupTime: vi.fn(async () => null),
    seqAtTime: vi.fn(() => null),
  }
}

function renderPane(props: Partial<ComponentProps<typeof TerminalPane>> = {}) {
  const renderStoppedLogs = vi.fn(({ status }: { instanceId: number; status: string }) => (
    <div data-testid="stopped-logs">历史日志（{status}）</div>
  ))
  const merged = {
    instanceId: 2,
    historyBacktrack: historyStub(),
    renderStoppedLogs,
    ...props,
  }
  render(
    <I18nextProvider i18n={testI18n}>
      <TerminalPane {...merged} />
    </I18nextProvider> as ReactNode,
  )
  return { ...merged, renderStoppedLogs }
}

describe('TerminalPane（FR-037 工作区终端面板受控视图）', () => {
  it('停机实例渲染注入的日志回放（不连 WS），并保留禁用的命令栏。', () => {
    const { renderStoppedLogs } = renderPane({ status: 'STOPPED' })
    expect(renderStoppedLogs).toHaveBeenCalled()
    expect(screen.getByTestId('stopped-logs')).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: '控制台命令输入' })).toBeDisabled()
  })

  it('停机且可启动时给「启动实例」直达动作，点击走注入回调。', async () => {
    const onStartInstance = vi.fn()
    renderPane({ status: 'STOPPED', onStartInstance })
    const button = screen.getByRole('button', { name: /启动实例/ })
    button.click()
    expect(onStartInstance).toHaveBeenCalled()
  })

  it('状态未知（未注入）时不连 WS：既不渲染终端也不渲染日志回放。', () => {
    renderPane({ status: '' })
    expect(screen.queryByTestId('stopped-logs')).toBeNull()
    // 未知态没有 token 请求 → 显示只读占位而非终端。
    expect(screen.queryByTestId('console-output')).toBeNull()
  })

  it('凭据获取失败时显式展示失败原因，不静默。', () => {
    renderPane({ status: 'RUNNING', tokenError: 'token already used' })
    expect(screen.getByText(/终端连接失败/)).toHaveTextContent('token already used')
  })

  it('无 terminal.access 时命令栏禁用并给出权限原因。', () => {
    renderPane({ status: 'RUNNING', canAccessTerminal: false })
    expect(screen.getByRole('textbox', { name: '控制台命令输入' })).toBeDisabled()
    expect(screen.getByText('没有终端访问权限')).toBeInTheDocument()
  })

  it('hideHeader 时不渲染自带工具栏（由卡壳承载卡头）。', () => {
    renderPane({ status: 'RUNNING', hideHeader: true })
    expect(screen.queryByText('控制台')).toBeNull()

    renderPane({ status: 'RUNNING', instanceName: 'survival-01' })
    expect(screen.getAllByText('控制台').length).toBeGreaterThan(0)
    expect(screen.getByText('survival-01')).toBeInTheDocument()
  })

  it('卸载后不残留会话订阅（管理器侧可见）。', () => {
    const { unmount } = render(
      <I18nextProvider i18n={testI18n}>
        <TerminalPane instanceId={9} status="STOPPED" historyBacktrack={historyStub()} />
      </I18nextProvider> as ReactNode,
    )
    unmount()
    expect(terminalSessionManager.getLines(9)).toHaveLength(0)
  })
})
