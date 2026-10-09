import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import InstanceBatchBar from '@/components/views/instances/InstanceBatchBar'
import type { InstanceBatchResult } from '@/lib/instances/instance-batch'

/**
 * FR-058 / FR-139 实例批量操作栏 · 受控视图测（ADR-097 b 范式）。
 *
 * 本组件在应用侧原本没有测试，故这里是**新补**而非迁移。断言聚焦本组件自己的语义：
 * 状态感知禁用、二次确认（停止/命令复述、kill 关键字）、部分失败的明细与「保留失败项选择」。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        instanceBatch: {
          selected: '已选 {{count}} 个',
          clear: '清空',
          command: '下发命令',
          commandPlaceholder: '输入要下发到选中运行中实例的命令（如 say hello）',
          start: '批量启动',
          stop: '批量停止',
          restart: '批量重启',
          kill: '批量强制关服',
          run: '执行',
          cancel: '取消',
          confirmStopTitle: '确认批量停止？',
          confirmStopDesc: '将向选中的 {{count}} 个实例下发停止指令。',
          confirmKillTitle: '确认批量强制关服？',
          confirmKillDesc: '强制关服会立即终止进程，可能丢失未保存数据。将作用于选中的 {{count}} 个实例。此操作不可撤销。',
          confirmCommandTitle: '确认批量下发命令？',
          confirmCommandDesc: '将向选中的 {{count}} 个实例下发命令：{{command}}',
          partial: '批量操作部分失败：成功 {{succeeded}}，失败 {{failed}}，跳过 {{skipped}}',
          selectFirst: '请先选择实例',
          noRunning: '所选实例中无运行中的',
          noStartable: '所选实例中无可启动的',
          noKillable: '所选实例中无可强制关服的',
          failTitle: '部分操作失败',
          failDesc: '以下实例操作失败，已保留其选择以便重试：',
          failClose: '知道了',
          confirmKillTypeHint: '请输入 {{keyword}} 以确认',
          confirmKeyword: 'FORCE',
          result: '批量完成：成功 {{succeeded}}，失败 {{failed}}，跳过 {{skipped}}',
          noTarget: '请先选择实例',
          needCommand: '请输入要下发的命令',
          failed: '批量操作失败',
        },
        rolling: { open: '滚动操作' },
      },
    },
  },
  interpolation: { escapeValue: false },
})

type Props = Parameters<typeof InstanceBatchBar>[0]

const RUNNING = { id: 1, name: 'survival-01', status: 'RUNNING' }
const STOPPED = { id: 2, name: 'lobby-02', status: 'STOPPED' }

function okResult(over: Partial<InstanceBatchResult> = {}): InstanceBatchResult {
  return {
    action: 'start',
    requested: 1,
    succeeded: 1,
    failed: 0,
    skipped: 0,
    errors: [],
    ...over,
  }
}

function renderBar(props: Partial<Props> = {}) {
  const merged = {
    selected: [RUNNING],
    onClear: vi.fn(),
    onRetainFailed: vi.fn(),
    onRun: vi.fn<Props['onRun']>().mockResolvedValue(okResult()),
    notify: vi.fn<Props['notify']>(),
    ...props,
  }
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
  }
  render(<InstanceBatchBar {...merged} />, { wrapper: Wrapper })
  return merged
}

describe('InstanceBatchBar（FR-058 / FR-139 · ADR-097 b 范式）', () => {
  it('未选实例时各动作禁用，并给出原因', () => {
    renderBar({ selected: [] })

    expect(screen.getByText('已选 0 个')).toBeInTheDocument()
    const start = screen.getByRole('button', { name: '批量启动' })
    expect(start).toBeDisabled()
    expect(start).toHaveAttribute('title', '请先选择实例')
    expect(screen.getByRole('button', { name: '滚动操作' })).toBeDisabled()
  })

  it('按选中集状态分布做禁用：只有 STOPPED 时不能停/重启/强杀/下发命令', () => {
    renderBar({ selected: [STOPPED] })

    expect(screen.getByRole('button', { name: '批量启动' })).toBeEnabled()
    expect(screen.getByRole('button', { name: '批量停止' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '批量重启' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '批量强制关服' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '下发命令' })).toBeDisabled()
  })

  it('命令为空时直接提示，不打开确认框', async () => {
    const user = userEvent.setup()
    const { notify, onRun } = renderBar()

    await user.click(screen.getByRole('button', { name: '下发命令' }))

    expect(notify).toHaveBeenCalledWith('error', '请输入要下发的命令')
    expect(onRun).not.toHaveBeenCalled()
    expect(screen.queryByText('确认批量下发命令？')).not.toBeInTheDocument()
  })

  it('命令非空：确认框复述命令，确认后按结构上报', async () => {
    const user = userEvent.setup()
    const { onRun } = renderBar()

    await user.type(screen.getByPlaceholderText(/输入要下发到选中运行中实例的命令/), 'say hello')
    await user.click(screen.getByRole('button', { name: '下发命令' }))

    expect(screen.getByText('确认批量下发命令？')).toBeInTheDocument()
    expect(screen.getByText('将向选中的 1 个实例下发命令：say hello')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '执行' }))
    await waitFor(() => expect(onRun).toHaveBeenCalledTimes(1))
    expect(vi.mocked(onRun).mock.calls[0][0]).toEqual({ action: 'command', ids: [1], command: 'say hello' })
  })

  it('停止需二次确认，确认后才执行', async () => {
    const user = userEvent.setup()
    const { onRun } = renderBar()

    await user.click(screen.getByRole('button', { name: '批量停止' }))
    expect(screen.getByText('确认批量停止？')).toBeInTheDocument()
    expect(onRun).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: '执行' }))
    await waitFor(() => expect(onRun).toHaveBeenCalledTimes(1))
    expect(vi.mocked(onRun).mock.calls[0][0]).toEqual({ action: 'stop', ids: [1] })
  })

  it('强杀要求输入关键字 FORCE 才放行「执行」', async () => {
    const user = userEvent.setup()
    const { onRun } = renderBar()

    await user.click(screen.getByRole('button', { name: '批量强制关服' }))
    expect(screen.getByText('请输入 FORCE 以确认')).toBeInTheDocument()

    const exec = screen.getByRole('button', { name: '执行' })
    expect(exec).toBeDisabled()

    await user.type(screen.getByPlaceholderText('FORCE'), 'FORC')
    expect(exec).toBeDisabled()
    await user.type(screen.getByPlaceholderText('FORCE'), 'E')
    expect(exec).toBeEnabled()

    await user.click(exec)
    await waitFor(() => expect(onRun).toHaveBeenCalledTimes(1))
    expect(vi.mocked(onRun).mock.calls[0][0]).toEqual({ action: 'kill', ids: [1] })
  })

  it('启动不需确认，直接执行', async () => {
    const user = userEvent.setup()
    // 启动只对可启动状态（STOPPED/CRASHED）开放，故此处选 STOPPED。
    const { onRun } = renderBar({ selected: [STOPPED] })

    await user.click(screen.getByRole('button', { name: '批量启动' }))

    await waitFor(() => expect(onRun).toHaveBeenCalledTimes(1))
    expect(screen.queryByText('确认批量停止？')).not.toBeInTheDocument()
  })

  it('全部成功：提示成功并清空选择', async () => {
    const user = userEvent.setup()
    const { notify, onClear, onRetainFailed } = renderBar({ selected: [STOPPED] })

    await user.click(screen.getByRole('button', { name: '批量启动' }))

    await waitFor(() => expect(onClear).toHaveBeenCalledTimes(1))
    expect(notify).toHaveBeenCalledWith('success', '批量完成：成功 1，失败 0，跳过 0')
    expect(onRetainFailed).not.toHaveBeenCalled()
  })

  it('部分失败：列明细、保留失败项选择、不清空选择', async () => {
    const user = userEvent.setup()
    const { notify, onClear, onRetainFailed } = renderBar({
      selected: [RUNNING, STOPPED],
      onRun: vi.fn<Props['onRun']>().mockResolvedValue(
        okResult({
          requested: 2,
          succeeded: 1,
          failed: 1,
          errors: [{ instanceId: 2, error: '实例未运行' }],
        }),
      ),
    })

    await user.click(screen.getByRole('button', { name: '批量启动' }))

    // 失败明细用实例名（而非 id）展示，并把失败项交回外壳保留选择。
    expect(await screen.findByText('lobby-02')).toBeInTheDocument()
    expect(screen.getByText('实例未运行')).toBeInTheDocument()
    expect(onRetainFailed).toHaveBeenCalledWith([2])
    expect(onClear).not.toHaveBeenCalled()
    expect(notify).toHaveBeenCalledWith('warning', '批量操作部分失败：成功 1，失败 1，跳过 0')
  })

  it('请求抛错：优先显示服务端 message', async () => {
    const user = userEvent.setup()
    const { notify } = renderBar({
      selected: [STOPPED],
      onRun: vi
        .fn<Props['onRun']>()
        .mockRejectedValue({ response: { data: { message: '目标实例已被锁定' } } }),
    })

    await user.click(screen.getByRole('button', { name: '批量启动' }))

    await waitFor(() => expect(notify).toHaveBeenCalledWith('error', '目标实例已被锁定'))
  })

  it('滚动编排对话框由插槽注入，点击才渲染', async () => {
    const user = userEvent.setup()
    const rollingDialog = vi.fn<NonNullable<Props['rollingDialog']>>(() => <div>滚动面板</div>)
    renderBar({ rollingDialog })

    expect(rollingDialog).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: '滚动操作' }))
    expect(rollingDialog).toHaveBeenCalledTimes(1)
    expect(screen.getByText('滚动面板')).toBeInTheDocument()
  })
})
