import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import RollingBatchDialog, { RollingProgress } from './RollingBatchDialog'
import type { RollingOp } from '@jianmanager/ui/lib/instance-rolling'

/**
 * FR-457 滚动/分批/灰度编排 · 受控视图测（ADR-097 b 范式）。
 *
 * 应用侧已有一份 msw 集成测试（覆盖创建载荷端到端、进度轮询、暂停/继续/取消），
 * 这里补的是**不依赖后端**的视图语义：灰度比例换算、批数计算、控制按钮的状态可见性、
 * 以及进度插槽的切换时机。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { cancel: '取消', loading: '加载中...' },
        instanceBatch: {
          restart: '批量重启',
          start: '批量启动',
          stop: '批量停止',
          kill: '批量强制关服',
          command: '下发命令',
          commandPlaceholder: '输入要下发到选中运行中实例的命令（如 say hello）',
        },
        rolling: {
          title: '滚动/分批/灰度编排',
          formDesc: '对选中的 {{count}} 个实例按批推进，避免一次性扇出打满并发。',
          progressDesc: '编排进行中，可暂停/继续或取消后续批。',
          action: '动作',
          command: '命令',
          batchSize: '批大小（台/批）',
          batchInterval: '批间隔（秒）',
          ratio: '灰度比例',
          ratioAll: '全量',
          failFast: '失败即停',
          start: '开始编排',
          starting: '创建中…',
          close: '关闭',
          opSummary: '{{action}} · 共 {{batches}} 批 · 每批 {{batchSize}} 台',
          succeeded: '成功',
          failed: '失败',
          skipped: '跳过',
          batches: '批',
          pause: '暂停',
          resume: '继续',
          cancel: '取消',
          state: {
            pending: '待开始',
            running: '进行中',
            paused: '已暂停',
            done: '已完成',
            canceled: '已取消',
          },
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

type Props = Parameters<typeof RollingBatchDialog>[0]

const selected = [
  { id: 101, name: 'a', status: 'RUNNING' },
  { id: 102, name: 'b', status: 'RUNNING' },
]

function op(over: Partial<RollingOp> = {}): RollingOp {
  return {
    id: 9,
    action: 'restart',
    batchSize: 5,
    batchIntervalSec: 30,
    failFast: true,
    ratio: 0,
    targets: [101, 102],
    cursor: 0,
    state: 'running',
    requested: 2,
    succeeded: 0,
    failed: 0,
    skipped: 0,
    errors: [],
    createdAt: '',
    updatedAt: '',
    ...over,
  }
}

function Wrapper({ children }: { children: ReactNode }) {
  return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
}

function renderDialog(props: Partial<Props> = {}) {
  const merged = {
    selected,
    onClose: vi.fn(),
    onCreate: vi.fn<Props['onCreate']>().mockResolvedValue(op()),
    ...props,
  }
  render(<RollingBatchDialog {...merged} />, { wrapper: Wrapper })
  return merged
}

describe('RollingBatchDialog 策略表单（FR-457 · ADR-097 b 范式）', () => {
  it('描述带出选中数量，默认策略为分批 5/间隔 30/失败即停/全量', () => {
    renderDialog()

    expect(screen.getByText('对选中的 2 个实例按批推进，避免一次性扇出打满并发。')).toBeInTheDocument()
    expect(screen.getByLabelText('批大小（台/批）')).toHaveValue(5)
    expect(screen.getByLabelText('批间隔（秒）')).toHaveValue(30)
    expect(screen.getByRole('checkbox')).toBeChecked()
    expect(screen.getByRole('button', { name: '全量' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('目标固定走 filter.instanceIds，且全量时 ratio 归 0', async () => {
    const user = userEvent.setup()
    const { onCreate } = renderDialog()

    await user.click(screen.getByRole('button', { name: '开始编排' }))

    await waitFor(() => expect(onCreate).toHaveBeenCalledTimes(1))
    expect(vi.mocked(onCreate).mock.calls[0][0]).toEqual({
      action: 'restart',
      // 必须走 filter：后端灰度的 ratio 抽样仅在 filter 模式生效，发 ids 会静默退化为全量。
      filter: { instanceIds: [101, 102] },
      batchSize: 5,
      batchIntervalSec: 30,
      failFast: true,
      ratio: 0,
    })
  })

  it('灰度比例换算：20% → 0.2，且 0 值的批参数保持 0（不分批）', async () => {
    const user = userEvent.setup()
    const { onCreate } = renderDialog()

    const size = screen.getByLabelText('批大小（台/批）')
    await user.clear(size)
    await user.type(size, '0')
    const interval = screen.getByLabelText('批间隔（秒）')
    await user.clear(interval)
    await user.type(interval, '0')
    await user.click(screen.getByRole('button', { name: '20%' }))
    await user.click(screen.getByRole('button', { name: '开始编排' }))

    await waitFor(() => expect(onCreate).toHaveBeenCalledTimes(1))
    const payload = vi.mocked(onCreate).mock.calls[0][0]
    expect(payload.batchSize).toBe(0)
    expect(payload.batchIntervalSec).toBe(0)
    expect(payload.ratio).toBe(0.2)
  })

  it('选命令动作才出现命令输入，且命令为空时不能开始', async () => {
    const user = userEvent.setup()
    const { onCreate } = renderDialog()

    expect(screen.queryByLabelText('命令')).not.toBeInTheDocument()

    await user.selectOptions(screen.getByLabelText('动作'), 'command')
    expect(screen.getByLabelText('命令')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '开始编排' })).toBeDisabled()

    await user.type(screen.getByLabelText('命令'), 'say hi')
    expect(screen.getByRole('button', { name: '开始编排' })).toBeEnabled()
    await user.click(screen.getByRole('button', { name: '开始编排' }))

    await waitFor(() => expect(onCreate).toHaveBeenCalledTimes(1))
    expect(vi.mocked(onCreate).mock.calls[0][0]).toMatchObject({ action: 'command', command: 'say hi' })
  })

  it('未选实例时不能开始', () => {
    renderDialog({ selected: [] })
    expect(screen.getByRole('button', { name: '开始编排' })).toBeDisabled()
  })

  it('创建成功后切到进度插槽，底部按钮变为「关闭」', async () => {
    const user = userEvent.setup()
    renderDialog({ progressSlot: (id) => <div>进度 #{id}</div> })

    await user.click(screen.getByRole('button', { name: '开始编排' }))

    expect(await screen.findByText('进度 #9')).toBeInTheDocument()
    expect(screen.getByText('编排进行中，可暂停/继续或取消后续批。')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '关闭' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '开始编排' })).not.toBeInTheDocument()
  })
})

describe('RollingProgress（受控进度视图）', () => {
  function renderProgress(props: Partial<Parameters<typeof RollingProgress>[0]> = {}) {
    const merged = { op: op(), onControl: vi.fn(), ...props }
    render(<RollingProgress {...merged} />, { wrapper: Wrapper })
    return merged
  }

  it('会话未就绪时显示加载态', () => {
    renderProgress({ op: null })
    expect(screen.getByText('加载中...')).toBeInTheDocument()
    expect(screen.queryByTestId('rolling-progress')).not.toBeInTheDocument()
  })

  it('批数按 batchSize 折算；batchSize=0 时单批全量', () => {
    // opSummary 是一整句（动作 · 共 N 批 · 每批 M 台），故用正则匹配其中片段。
    renderProgress({ op: op({ requested: 10, batchSize: 4 }) })
    expect(screen.getByText(/共 3 批/)).toBeInTheDocument()

    renderProgress({ op: op({ requested: 10, batchSize: 0 }) })
    expect(screen.getAllByText(/共 1 批/).length).toBeGreaterThan(0)
  })

  it('running 显示暂停与取消', async () => {
    const user = userEvent.setup()
    const { onControl } = renderProgress({ op: op({ state: 'running' }) })

    await user.click(screen.getByTestId('rolling-pause'))
    expect(onControl).toHaveBeenCalledWith('pause')
    await user.click(screen.getByTestId('rolling-cancel'))
    expect(onControl).toHaveBeenCalledWith('cancel')
  })

  it('paused 显示继续与取消', async () => {
    const user = userEvent.setup()
    const { onControl } = renderProgress({ op: op({ state: 'paused' }) })

    expect(screen.queryByTestId('rolling-pause')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('rolling-resume'))
    expect(onControl).toHaveBeenCalledWith('resume')
    expect(screen.getByTestId('rolling-cancel')).toBeInTheDocument()
  })

  it('终态（done）不再显示任何控制按钮', () => {
    renderProgress({ op: op({ state: 'done', succeeded: 2 }) })

    expect(screen.queryByTestId('rolling-pause')).not.toBeInTheDocument()
    expect(screen.queryByTestId('rolling-resume')).not.toBeInTheDocument()
    expect(screen.queryByTestId('rolling-cancel')).not.toBeInTheDocument()
    expect(screen.getByText('2/2')).toBeInTheDocument()
  })

  it('控制动作在途时按钮禁用', () => {
    renderProgress({ op: op({ state: 'running' }), controlling: true })
    expect(screen.getByTestId('rolling-pause')).toBeDisabled()
    expect(screen.getByTestId('rolling-cancel')).toBeDisabled()
  })

  it('有失败明细时列出实例号与原因', () => {
    renderProgress({
      op: op({ state: 'done', requested: 2, succeeded: 1, failed: 1, errors: [{ instanceId: 102, error: '实例未运行' }] }),
    })

    expect(screen.getByText('#102')).toBeInTheDocument()
    expect(screen.getByText('实例未运行')).toBeInTheDocument()
  })
})
