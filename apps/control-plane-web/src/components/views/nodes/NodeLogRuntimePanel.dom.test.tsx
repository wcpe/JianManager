import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import NodeLogRuntimePanel, { type LogRuntimeView } from '@/components/views/nodes/NodeLogRuntimePanel'

/**
 * 本文件自带局部 i18n 实例，而不复用 `src/test/i18n.tsx` 的共享实例。
 *
 * 原因：本组件的文案面是完整的 `logsRuntime.*` 命名空间（十余条，且**不带 defaultValue**），
 * 塞进共享实例会让它变成第二份应用语言包（该文件已明确反对）。就近自包含同时把
 * 「本组件要求消费方提供哪些键」写在了使用它的测试里；真实语言包由主控台提供。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        logsRuntime: {
          asset: { dispatch: '下发资产', dispatchHelp: '把已缓存的审批包下发到该节点' },
          namespace: { notReady: '未就绪', offline: '离线', migrate: '迁移分区', help: '分区用途说明' },
          gaps: { resolve: '核销已覆盖缺口', help: '核销说明' },
          date: { label: '迁移日期（该日分区）', help: '日期说明' },
          error: {
            // 与主控台 zh.json 的 logsRuntime.error.* 保持一致：这些文案是「按 code 本地化」的
            // 断言目标，若与真实语言包不符，测试通过也说明不了线上表现。
            unauthorized: '日志平台鉴权失败，请检查节点凭据',
            budget: '节点资源预算不足，日志平台已降级',
            notReady: '日志平台正在启动，请稍后重试',
            archiveMissing: '历史日志尚未取回',
            unsupported: '本节点尚未启用日志平台（需先下发 VictoriaLogs 资产并配置）',
          },
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

type PanelProps = Parameters<typeof NodeLogRuntimePanel>[0]

/** 受控渲染：数据与回调全部经 props 注入，无需 MSW、无需 QueryClient。 */
function renderPanel(props: Partial<PanelProps> = {}) {
  const handlers = {
    onRefresh: vi.fn(),
    onUpload: vi.fn().mockResolvedValue(true),
    onInstall: vi.fn().mockResolvedValue(true),
    onControl: vi.fn().mockResolvedValue(true),
    onMigrate: vi.fn().mockResolvedValue(true),
    onResolveGaps: vi.fn().mockResolvedValue(true),
    ...props,
  }
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
  }
  const result = render(
    <NodeLogRuntimePanel os="linux" arch="amd64" online {...handlers} />,
    { wrapper: Wrapper },
  )
  return { ...handlers, ...result }
}

const runningRuntime: LogRuntimeView = {
  instances: [
    { namespace: 'hot', state: 'RUNNING', listen_addr: '127.0.0.1:19441', asset_tag: 'v1.52.0', health_ok: true, query_ready: false },
    { namespace: 'cold', state: 'STOPPED', health_ok: false, query_ready: false },
  ],
}

describe('NodeLogRuntimePanel（ADR-097 b 范式）', () => {
  it('进程健康与可查询分别呈现，且三个动作各自上报回调', async () => {
    const user = userEvent.setup()
    const { onInstall, onMigrate, onResolveGaps } = renderPanel({
      runtime: runningRuntime,
      approvedCached: true,
    })

    expect(screen.getByText(/审批包已缓存/)).toBeInTheDocument()
    // 进程健康（health_ok）与可查询（query_ready）是两个独立信号，不可混为一谈。
    expect(screen.getByText('进程健康')).toBeInTheDocument()
    expect(screen.queryByText('可查询')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '下发资产' }))
    expect(onInstall).toHaveBeenCalledTimes(1)

    fireEvent.change(screen.getByLabelText('迁移日期（该日分区）'), { target: { value: '2026-09-23' } })
    await user.click(screen.getByRole('button', { name: '迁移分区' }))
    // 视图只交日期；`node:<id>` 前缀属应用侧知识，由外壳拼装。
    await waitFor(() => expect(onMigrate).toHaveBeenCalledWith('2026-09-23'))

    await user.click(screen.getByRole('button', { name: '核销已覆盖缺口' }))
    expect(onResolveGaps).toHaveBeenCalledTimes(1)
  })

  it('离线或审批包未缓存时禁用下发，且未知错误码回退原始消息', () => {
    renderPanel({
      online: false,
      approvedCached: false,
      runtime: { error: { message: 'LOG_NOT_READY' }, instances: [] },
    })

    expect(screen.getByText(/审批包未缓存/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '下发资产' })).toBeDisabled()
    // 无 code → 按结构化映射失败，回退后端原始 message。
    expect(screen.getByText('LOG_NOT_READY')).toBeInTheDocument()
  })

  it('已知错误码按 code 本地化，不暴露后端英文诊断', () => {
    renderPanel({
      runtime: { error: { code: 5, message: 'managed VictoriaLogs supervisor is not configured' }, instances: [] },
    })

    expect(screen.getByText('日志平台正在启动，请稍后重试')).toBeInTheDocument()
    expect(screen.queryByText(/supervisor is not configured/)).not.toBeInTheDocument()
  })

  it('写操作在途时统一禁用全部动作', () => {
    renderPanel({ runtime: runningRuntime, approvedCached: true, busy: true })
    expect(screen.getByRole('button', { name: '下发资产' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '核销已覆盖缺口' })).toBeDisabled()
  })
})
