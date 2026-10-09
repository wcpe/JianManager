import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ComponentProps, ReactNode } from 'react'
import { QuotaPanel } from '@/components/views/console/QuotaPanel'
import type { InstanceQuotaStatus } from '@/lib/instances/quota-status'

/**
 * 配额面板 · 受控视图测（ADR-097）。
 *
 * 补的是**展示契约**：四态（加载 / 失败 / 无数据 / 有数据）、来源与强制状态、待收紧限额。
 * 取数与重试由应用侧接线层注入，这里只验证回调被正确调用。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { loading: '加载中…', refresh: '重试', noData: '无数据' },
        serverConsole: {
          quotaTitle: '配额',
          quotaHint: '限额与实时用量',
          quotaCpu: 'CPU',
          quotaMemory: '内存',
          quotaDisk: '磁盘',
          quotaCores: '核',
          quotaUnlimited: '不限',
          quotaNone: '无',
          quotaEnforced: '已强制',
          quotaLoadFailed: '配额读取失败',
          quotaThrottleUnsupported: '当前模式不支持内核级限流',
          quotaThrottlePending: '待收紧：CPU {{cpu}} / 内存 {{mem}}',
          quotaEnforceStateScope: '强制状态作用域：{{scope}}',
          quotaWriteHint: '限额修改在下次启动生效',
          quotaMode: { alert: '告警', throttle: '限流' },
          quotaSource: { instance: '实例级', group: '组派生', none: '不限' },
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

function quota(over: Partial<InstanceQuotaStatus> = {}): InstanceQuotaStatus {
  return {
    instanceId: 7,
    cpuCores: 2,
    memLimitMb: 2048,
    diskLimitMb: 0,
    enforceMode: 'throttle',
    memSource: 'instance',
    diskSource: 'none',
    cpuSource: 'group',
    groupId: 3,
    cpuPercent: 12.34,
    rssBytes: 512 * 1024 * 1024,
    diskBytes: 0,
    enforcedCpu: true,
    enforcedMem: false,
    enforcedDisk: false,
    enforceStateScope: 'in_process',
    throttleCpuLimit: 0,
    throttleMemLimitMb: 0,
    supportedThrottle: true,
    ...over,
  }
}

function renderPanel(props: Partial<ComponentProps<typeof QuotaPanel>> = {}) {
  const onRetry = vi.fn()
  const merged = { quota: quota(), isLoading: false, isError: false, onRetry, ...props }
  render(
    <I18nextProvider i18n={testI18n}>
      <QuotaPanel {...merged} />
    </I18nextProvider> as ReactNode,
  )
  return { merged, onRetry }
}

describe('QuotaPanel（FR-467 实例配额区受控视图）', () => {
  it('加载态显示加载文案。', () => {
    renderPanel({ isLoading: true, quota: undefined })
    expect(screen.getByText('加载中…')).toBeInTheDocument()
    expect(screen.queryByTestId('quota-mode')).toBeNull()
  })

  it('失败态显式说出失败并给重试，不回落成「加载中」。', async () => {
    const user = userEvent.setup()
    const { onRetry } = renderPanel({ isError: true, quota: undefined })
    expect(screen.getByTestId('quota-error')).toBeInTheDocument()
    expect(screen.getByText('配额读取失败')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '重试' }))
    expect(onRetry).toHaveBeenCalled()
  })

  it('无数据态显示无数据占位。', () => {
    renderPanel({ quota: undefined })
    expect(screen.getByText('无数据')).toBeInTheDocument()
  })

  it('有数据：三行用量 + 限额 + 来源标签，强制状态按维度标注。', () => {
    renderPanel()
    expect(screen.getByTestId('quota-mode')).toHaveTextContent('限流')
    // CPU：用量百分比 + 组派生来源 + 已强制。
    expect(screen.getByText('12.3%')).toBeInTheDocument()
    expect(screen.getAllByText('组派生').length).toBeGreaterThan(0)
    expect(screen.getByTestId('quota-enforced')).toBeInTheDocument()
    // 内存：MiB 用量与实例级来源。
    expect(screen.getByText('512.0 MiB')).toBeInTheDocument()
    expect(screen.getAllByText('实例级').length).toBeGreaterThan(0)
    // 磁盘：限额为 0 → 显示「不限」。
    expect(screen.getAllByText('不限').length).toBeGreaterThan(0)
  })

  it('不支持内核级限流时如实标注。', () => {
    renderPanel({ quota: quota({ supportedThrottle: false }) })
    expect(screen.getByTestId('quota-throttle-unsupported')).toBeInTheDocument()
  })

  it('待收紧限额有值时才提示，并写明下次启动生效。', () => {
    renderPanel({ quota: quota({ throttleCpuLimit: 1, throttleMemLimitMb: 1024 }) })
    expect(screen.getByTestId('quota-throttle-pending')).toHaveTextContent('待收紧')
    expect(screen.getByText('限额修改在下次启动生效')).toBeInTheDocument()
  })

  it('无待收紧限额时不渲染该提示。', () => {
    renderPanel()
    expect(screen.queryByTestId('quota-throttle-pending')).toBeNull()
  })

  it('强制状态作用域显式展示（避免把 false 读成「从未超限」）。', () => {
    renderPanel()
    expect(screen.getByTestId('quota-enforce-scope')).toHaveTextContent('in_process')
  })
})
