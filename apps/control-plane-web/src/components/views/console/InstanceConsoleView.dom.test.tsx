import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ComponentProps, ReactNode } from 'react'
import { InstanceConsoleView } from '@/components/views/console/InstanceConsoleView'
import type { ConsoleHistoryState } from '@jianmanager/ui/lib/console-history'

/**
 * 实例控制台视图 · 受控视图测（ADR-097）。
 *
 * 补的是**展示契约**：连接占位、搜索面板开关、命令栏禁用口径、输出区与命令栏并存。
 * 终端会话的真实连接与回溯取数由应用侧 TerminalPane / fr416 端到端用例覆盖
 * （它们经接线层注入名册、回溯控制器与提示通道）。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { copied: '已复制', copyFailed: '复制失败' },
        instanceDetail: {
          connecting: '正在连接…',
          terminalSearchOpen: '终端搜索',
          terminalSearchInput: '搜索输出',
          terminalSearchPlaceholder: '输入关键字…',
          terminalSearchPosition: '第 {{current}}/{{count}} 项',
          terminalSearchReady: '输入关键字开始搜索',
          terminalSearchPrevious: '上一个',
          terminalSearchNext: '下一个',
          consoleCommandLabel: '控制台命令输入',
          consoleCommandPlaceholder: '输入命令…',
          consoleCommandSend: '发送',
          consoleCommandDisabledPlaceholder: '命令输入已禁用',
          consoleEmpty: '暂无输出',
          consoleLevelAll: '全部',
          consoleLevelError: '错误',
          consoleLevelWarn: '警告',
          consoleLevelInfo: '信息',
          consoleLevelDebug: '调试',
          consoleLevelFilterLabel: '级别过滤',
          consoleLevelFilterEmpty: '没有符合过滤条件的输出',
          consoleWrapToggle: '自动换行',
          consoleJumpToBottom: '跳到底部',
          consoleCopyNothing: '没有可复制的内容',
          consoleSavedLog: '已存为 .log',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

/** 回溯控制器 stub：默认「什么都没加载」。 */
function historyBacktrack(over: Partial<ConsoleHistoryState> = {}): ConsoleHistoryState {
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
    ...over,
  }
}

function renderView(props: Partial<ComponentProps<typeof InstanceConsoleView>> = {}) {
  const onNotify = vi.fn()
  const merged = {
    instanceId: 7,
    historyBacktrack: historyBacktrack(),
    onNotify,
    ...props,
  }
  const { container } = render(
    <I18nextProvider i18n={testI18n}>
      <InstanceConsoleView {...(merged as ComponentProps<typeof InstanceConsoleView>)} />
    </I18nextProvider> as ReactNode,
  )
  return { ...merged, onNotify, container }
}

describe('InstanceConsoleView（FR-415 实例控制台受控视图）', () => {
  it('token 加载中显示连接占位，不渲染命令栏。', () => {
    renderView({ isLoading: true })
    expect(screen.getByText('正在连接…')).toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: '控制台命令输入' })).toBeNull()
  })

  it('就绪态同时渲染输出区与命令栏。', () => {
    const { container } = renderView()
    expect(screen.getByRole('textbox', { name: '控制台命令输入' })).toBeInTheDocument()
    // 输出区容器（虚拟列表根）在 DOM 里。
    expect(container.querySelector('[data-testid="console-output"]')).not.toBeNull()
  })

  it('searchOpen 为假时不渲染搜索面板。', () => {
    renderView({ searchOpen: false })
    expect(screen.queryByRole('search')).toBeNull()
  })

  it('searchOpen 为真时渲染搜索面板与位置提示。', () => {
    renderView({ searchOpen: true })
    const panel = screen.getByRole('search')
    expect(panel).toBeInTheDocument()
    expect(screen.getByRole('searchbox', { name: '搜索输出' })).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('输入关键字开始搜索')
  })

  it('readOnly 时命令栏禁用并给出禁用原因。', () => {
    renderView({ readOnly: true, disabledReason: '实例未运行' })
    expect(screen.getByRole('textbox', { name: '控制台命令输入' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled()
    expect(screen.getByText('实例未运行')).toBeInTheDocument()
  })

  it('禁用态可带直达动作（停机/崩溃态的「启动实例」按钮）。', () => {
    renderView({
      readOnly: true,
      disabledReason: '实例未运行',
      disabledAction: <button type="button">启动实例</button>,
    })
    expect(screen.getByRole('button', { name: '启动实例' })).toBeInTheDocument()
  })
})
