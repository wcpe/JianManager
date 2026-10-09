import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { ConsoleImmersiveMode } from '@/components/views/console/ConsoleImmersiveMode'
import type { ConsoleImmersiveModeProps } from '@/components/views/console/ConsoleImmersiveMode'
import { terminalSessionManager } from '@/lib/console/terminal-session-manager'

/**
 * 沉浸控制台工作台 · 受控视图测（ADR-097）。
 *
 * 补的是**注入面契约**：`usePaneData` 提供的实例名/指标被顶栏与 pane 用上、
 * `instances` 灌进实例选择器、`onSearchInstances` 在选择器开合时通知外壳、
 * `onNotify` 收到 pane 上限/最后一格的回执。
 * 交互纵切（ESC 分层、分屏与布局持久化、拾取器键盘化）由应用侧用例覆盖。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { cancel: '取消', loading: '加载中…' },
        metrics: { unavailable: '不可用' },
        serverConsole: { online: '在线' },
        instanceDetail: {
          consoleImmersiveLabel: '沉浸工作台',
          consoleImmersiveExit: '退出沉浸',
          consoleImmersiveAddPane: '添加面板',
          consoleImmersiveSplitHorizontal: '左右分屏',
          consoleImmersiveSplitVertical: '上下分屏',
          consoleImmersiveChangeInstance: '切换实例',
          consoleImmersiveMaximize: '放大面板',
          consoleImmersiveRestore: '恢复布局',
          consoleImmersiveClosePane: '关闭面板',
          consoleImmersivePaneCount: '面板',
          consoleImmersivePaneLimit: '最多 {{count}} 个面板',
          consoleImmersiveLastPane: '最后一个面板不能关闭',
          consoleImmersiveEscHint: '再按一次 ESC 退出',
          consoleImmersiveResize: '调整占比',
          consoleImmersiveNoInstance: '未选择实例',
          consoleImmersiveInstanceSearch: '搜索实例',
          consoleImmersivePickerTitle: '选择要显示的实例',
          consoleImmersivePickerSplitHint: '选一个实例放入新面板',
          consoleImmersivePickerReplaceHint: '选一个实例替换当前面板',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

function renderImmersive(props: Partial<ConsoleImmersiveModeProps> = {}) {
  const onExit = vi.fn()
  const onNotify = vi.fn()
  const onSearchInstances = vi.fn()
  const usePaneData = (id: number) => ({
    instance: { id, name: id === 1 ? 'survival-01' : 'lobby', status: 'RUNNING' },
    metrics: { probeAvailable: true, tps: 19.5, playersAvailable: true, onlinePlayers: 7, cpuPercent: 42.4 },
  })
  const merged: ConsoleImmersiveModeProps = {
    initialInstanceId: 1,
    onExit,
    renderPane: ({ instanceId, focused, onFocus }) => (
      <div>
        <input data-testid="pane-input" aria-label="pane input" />
        <button type="button" onClick={onFocus} data-focused={focused ? 'true' : undefined}>
          pane-{instanceId}
        </button>
      </div>
    ),
    instances: [
      { id: 1, name: 'survival-01', status: 'RUNNING' },
      { id: 2, name: 'lobby', status: 'RUNNING' },
    ],
    onNotify,
    onSearchInstances,
    usePaneData,
    ...props,
  }
  render(
    <I18nextProvider i18n={testI18n}>
      <ConsoleImmersiveMode {...merged} />
    </I18nextProvider> as ReactNode,
  )
  return { onExit, onNotify, onSearchInstances }
}

describe('ConsoleImmersiveMode（ADR-087 沉浸工作台受控视图）', () => {
  beforeEach(() => {
    sessionStorage.clear()
    terminalSessionManager.disposeAll()
  })

  it('注入的 usePaneData 提供顶栏实例名与指标段（TPS/在线/CPU）。', () => {
    renderImmersive()
    // 实例名同时出现在顶栏与 pane 标题栏，故用 getAllByText。
    expect(screen.getAllByText('survival-01').length).toBeGreaterThan(0)
    expect(screen.getByText('19.5')).toBeInTheDocument()
    expect(screen.getByText('7')).toBeInTheDocument()
    expect(screen.getByText('42%')).toBeInTheDocument()
  })

  it('指标不可得时显「不可用」，不以 0 冒充。', () => {
    renderImmersive({
      usePaneData: (id: number) => ({
        instance: { id, name: 'survival-01', status: 'RUNNING' },
        metrics: { probeAvailable: false, tps: 0, playersAvailable: false, onlinePlayers: 0, cpuPercent: 0 },
      }),
    })
    expect(screen.getAllByText('不可用').length).toBeGreaterThan(0)
  })

  it('renderPane 注入的内容渲染在 pane 内，pane 数量随分屏增加。', async () => {
    renderImmersive()
    expect(screen.getByText('pane-1')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '左右分屏' }))
    // 注入的 instances 灌进选择器。
    expect(await screen.findByRole('dialog', { name: '选择要显示的实例' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /lobby/ })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /lobby/ }))
    expect(screen.getByText('pane-2')).toBeInTheDocument()
  })

  it('选择器开合经 onSearchInstances 通知外壳（关闭时 open=false）。', async () => {
    const { onSearchInstances } = renderImmersive()
    // 挂载即通知一次「关闭」。
    expect(onSearchInstances).toHaveBeenCalledWith({ open: false, query: '' })

    fireEvent.click(screen.getByRole('button', { name: '左右分屏' }))
    expect(onSearchInstances).toHaveBeenCalledWith({ open: true, query: '' })
  })

  it('F11 退出沉浸（不注册 tmux 式前缀键）。', () => {
    const { onExit } = renderImmersive()
    fireEvent.keyDown(window, { key: 'b', ctrlKey: true, bubbles: true })
    expect(onExit).not.toHaveBeenCalled()

    fireEvent.keyDown(window, { key: 'F11', bubbles: true })
    expect(onExit).toHaveBeenCalledTimes(1)
  })

  it('ESC 分层：输入框内按 ESC 不退出；裸 ESC 双击才退出，首次经 onNotify 提示。', () => {
    const { onExit, onNotify } = renderImmersive()

    fireEvent.keyDown(screen.getByTestId('pane-input'), { key: 'Escape', bubbles: true })
    expect(onExit).not.toHaveBeenCalled()

    fireEvent.keyDown(window, { key: 'Escape', bubbles: true })
    expect(onExit).not.toHaveBeenCalled()
    expect(onNotify).toHaveBeenCalledWith('info', '再按一次 ESC 退出')

    fireEvent.keyDown(window, { key: 'Escape', bubbles: true })
    expect(onExit).toHaveBeenCalledTimes(1)
  })

  it('单 pane 时不渲染「关闭面板」（最后一格不可关，UI 不给死路按钮）。', () => {
    renderImmersive()
    expect(screen.queryByRole('button', { name: '关闭面板' })).toBeNull()
  })

  it('面板上限经 onNotify 回执（jsdom 视口 1024px → 至多 2 个）。', async () => {
    const { onNotify } = renderImmersive()
    // 视口 1024px 落在 <1280 档，pane 上限为 2：分屏一次即到顶。
    fireEvent.click(screen.getByRole('button', { name: '左右分屏' }))
    fireEvent.click(await screen.findByRole('button', { name: /lobby/ }, { timeout: 3000 }))
    expect(screen.getAllByTestId(/console-immersive-pane-/)).toHaveLength(2)

    fireEvent.click(screen.getByRole('button', { name: '添加面板' }))
    expect(onNotify).toHaveBeenCalledWith('error', '最多 2 个面板')
  })
})
