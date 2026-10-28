import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { AssistantSettingsView } from '@/components/views/settings/AssistantSettingsView'
import type { AssistantModel, AssistantProvider } from '@/api/assistant'

/**
 * AI 配置页 · 探测按钮（FR-514 spec §8）。
 *
 * 断言聚焦本组件自己负责的两件事：
 *  1) 「测试连接 / 测试模型」可按可访问名命中并触发回调；
 *  2) 模型行把自身的 tools 能力作为 withTools 传出去——否则「能不能调工具」的
 *     验证会退化成「跑一次普通文本补全」，给出假绿。
 *
 * 探测本身的分类与脱敏在服务端，不在这里测。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { edit: '编辑', delete: '删除' },
        assistant: {
          title: 'AI 助手',
          subtitle: '配置模型供应商、模型与默认模型。',
          testProvider: '测试连接',
          testModel: '测试模型',
          setDefault: '设为默认',
          enabled: '启用',
          disabled: '停用',
          credentialSet: '已配置',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

// wrapper 必须同时提供 i18n 与 QueryClient：
// 本视图内嵌 `MCPConnectionsPanel`（FR-517），它用 React Query 取数。
// 只给 i18n 会让整个文件在渲染阶段抛「No QueryClient set」，11 个用例全红——
// 而那失败与被测行为无关，属于脚手架缺口。
function wrapper({ children }: { children: ReactNode }) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return (
    <QueryClientProvider client={queryClient}>
      <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
    </QueryClientProvider>
  )
}

const provider: AssistantProvider = {
  id: 7,
  name: '主供应商',
  protocol: 'openai-chat',
  baseUrl: 'https://api.example.com',
  credentialMode: 'none',
  hasCredential: false,
  enabled: true,
  version: 1,
  createdAt: '2026-01-01T00:00:00Z',
  updatedAt: '2026-01-01T00:00:00Z',
}

const modelWithTools: AssistantModel = {
  id: 11,
  providerId: 7,
  providerName: '主供应商',
  protocol: 'openai-chat',
  providerEnabled: true,
  modelId: 'vendor-model-a',
  displayName: '通用对话',
  streaming: true,
  tools: true,
  contextWindow: 128000,
  maxOutputTokens: 4096,
  enabled: true,
  version: 1,
}

const modelNoTools: AssistantModel = { ...modelWithTools, id: 12, tools: false }

/** 视图的默认 props（用例可覆盖任意字段）。 */
function baseProps(overrides: Partial<Parameters<typeof AssistantSettingsView>[0]> = {}) {
  return {
    isPlatformAdmin: true,
    providers: [provider],
    models: [modelNoTools, modelWithTools],
    availableModels: [],
    defaultModelId: null,
    onCreateProvider: vi.fn().mockResolvedValue(true),
    onUpdateProvider: vi.fn().mockResolvedValue(true),
    onDeleteProvider: vi.fn().mockResolvedValue(true),
    onCreateModel: vi.fn().mockResolvedValue(true),
    onUpdateModel: vi.fn().mockResolvedValue(true),
    onDeleteModel: vi.fn().mockResolvedValue(true),
    onSetDefaultModel: vi.fn().mockResolvedValue(true),
    onTestProvider: vi.fn().mockResolvedValue(undefined),
    onTestModel: vi.fn().mockResolvedValue(undefined),
    onSaveEgress: vi.fn().mockResolvedValue(true),
    ...overrides,
  }
}

/** 渲染视图，回调用 spy 注入。 */
function renderView(overrides: Partial<Parameters<typeof AssistantSettingsView>[0]> = {}) {
  const props = baseProps(overrides)
  const view = render(<AssistantSettingsView {...props} />, { wrapper })
  return {
    onTestProvider: props.onTestProvider as ReturnType<typeof vi.fn>,
    onTestModel: props.onTestModel as ReturnType<typeof vi.fn>,
    onSaveEgress: props.onSaveEgress as ReturnType<typeof vi.fn>,
    rerender: (next: Partial<Parameters<typeof AssistantSettingsView>[0]>) =>
      view.rerender(<AssistantSettingsView {...baseProps(next)} />),
  }
}

describe('AssistantSettingsView 探测按钮', () => {
  it('点击「测试连接」触发供应商探测', async () => {
    const user = userEvent.setup()
    const { onTestProvider } = renderView()

    await user.click(screen.getByRole('button', { name: '测试连接' }))
    await waitFor(() => expect(onTestProvider).toHaveBeenCalledWith(7))
  })

  it('模型行把 tools 能力作为 withTools 传出', async () => {
    // 这条是两个方向都验的关键用例：声明了 tools 的模型应以 true 探测，
    // 未声明的不该被强行带工具——后者服务端会直接 422，前端不该制造这种失败。
    const user = userEvent.setup()
    const { onTestModel } = renderView()

    const buttons = screen.getAllByRole('button', { name: '测试模型' })
    expect(buttons).toHaveLength(2)

    // 行序与 models 一致：第一个是 modelNoTools，第二个带工具。
    await user.click(buttons[0])
    await waitFor(() => expect(onTestModel).toHaveBeenCalledWith(12, false))

    await user.click(buttons[1])
    await waitFor(() => expect(onTestModel).toHaveBeenCalledWith(11, true))
  })

  it('探测按钮在请求进行中禁用，避免重复付费调用', async () => {
    // 让回调挂住，观察按钮是否被禁用（连点会发起多次上游调用，按次计费）。
    let release: () => void = () => {}
    const pending = new Promise<void>((resolve) => {
      release = resolve
    })
    const onTestProvider = vi.fn().mockReturnValue(pending)
    const user = userEvent.setup()
    renderView({ onTestProvider })

    const button = screen.getByRole('button', { name: '测试连接' })
    await user.click(button)
    await waitFor(() => expect(button).toBeDisabled())

    release()
    await waitFor(() => expect(button).not.toBeDisabled())
  })
})

describe('AssistantSettingsView 输入与输出优化', () => {
  it('摘要条显示供应商/可用模型/默认模型', () => {
    // 这些数字此前已取回却只用于「未配置」提示，管理员看不到「现在到底能用什么」。
    renderView({
      providers: [provider, { ...provider, id: 8, name: '备用', enabled: false }],
      availableModels: [
        { id: 11, modelId: 'vendor-model-a', displayName: '通用对话', protocol: 'openai-chat', streaming: true, tools: true },
      ],
      defaultModelId: 11,
    })
    // 供应商「1 启用 / 共 2」与模型「1 可用 / 共 2」同形，故断言出现两处而非各取一次。
    expect(screen.getAllByText('1 / 2')).toHaveLength(2)
    // 显示名在摘要条与表格里都会出现，故用 getAllByText。
    expect(screen.getAllByText('通用对话').length).toBeGreaterThan(0)
  })

  it('未设默认模型时摘要条明示「未设置」', () => {
    renderView({
      availableModels: [
        { id: 11, modelId: 'm', displayName: '通用对话', protocol: 'openai-chat', streaming: true, tools: false },
      ],
      defaultModelId: null,
    })
    expect(screen.getByText('未设置')).toBeInTheDocument()
  })

  it('地址非 https 时输入时就地提示（不等保存后 422）', async () => {
    const user = userEvent.setup()
    renderView({ providers: [], models: [] })
    // 空态与面板头部都有「添加供应商」，取第一个即可（两者打开同一对话框）。
    await user.click(screen.getAllByRole('button', { name: '添加供应商' })[0])

    // 必填字段的标签带红色星号，可访问名成了「地址*」，故用正则。
    const input = screen.getByLabelText(/^地址/)
    await user.clear(input)
    await user.type(input, 'http://api.example.com')
    // 提示为「生产只接受 https」——不阻断（dev 部署可能放行），但要让人现在就知道。
    expect(screen.getByText(/只接受 https/)).toBeInTheDocument()
  })

  it('地址非法时提示且标记 aria-invalid', async () => {
    const user = userEvent.setup()
    renderView({ providers: [], models: [] })
    await user.click(screen.getAllByRole('button', { name: '添加供应商' })[0])

    // 必填字段的标签带红色星号，可访问名成了「地址*」，故用正则。
    const input = screen.getByLabelText(/^地址/)
    await user.clear(input)
    await user.type(input, 'not a url')
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(screen.getByText(/不是合法的 URL/)).toBeInTheDocument()
  })

  it('探测失败时行内展示分类码与处置建议，而不是只弹 toast', async () => {
    // 这条守的是输出侧的核心诉求：结果要能停留、能看清「下一步做什么」。
    const failed = {
      ok: false,
      latencyMs: 42,
      classifiedError: 'upstream_auth',
      message: 'upstream_auth（HTTP 401）: Incorrect API key',
    }
    renderView({ probeResults: { 'provider:7': failed } })
    expect(screen.getByRole('status')).toBeInTheDocument()
    expect(screen.getByText('测试失败')).toBeInTheDocument()
    expect(screen.getByText('upstream_auth')).toBeInTheDocument()
    expect(screen.getByText(/请检查密钥是否正确/)).toBeInTheDocument()
  })

  it('模型测试成功时展示工具解析结论与文本预览', () => {
    const ok = {
      ok: true,
      latencyMs: 88,
      textPreview: 'pong',
      promptTokens: 7,
      outputTokens: 2,
      toolCallParsed: true,
    }
    renderView({ probeResults: { 'model:12': ok } })
    expect(screen.getByText('连通正常')).toBeInTheDocument()
    // 「工具参数 已解析」由同一 <span> 内的两段文本拼成，直接对比整块文本更稳。
    expect(screen.getByRole('status').textContent).toContain('已解析')
    expect(screen.getByText('→ pong')).toBeInTheDocument()
  })

  it('egress 区块：空态 + 非法 CIDR 时禁用添加 + 风险提示', async () => {
    const user = userEvent.setup()
    renderView({ settings: { defaultModelId: null, allowPrivateCidrs: [], version: 1, insecureAllowed: false } })

    expect(screen.getByText('没有放行任何私网段')).toBeInTheDocument()
    // 放行会扩大可达范围，风险提示必须始终可见。
    expect(screen.getByText(/任何能被 HTTP 访问的服务/)).toBeInTheDocument()

    const addBtn = screen.getByRole('button', { name: '添加' })
    expect(addBtn).toBeDisabled()

    const input = screen.getByLabelText('添加网段')
    await user.type(input, 'not-a-cidr')
    expect(screen.getByText(/格式应为 CIDR/)).toBeInTheDocument()
    expect(addBtn).toBeDisabled()

    await user.clear(input)
    await user.type(input, '10.0.0.0/8')
    await waitFor(() => expect(addBtn).toBeEnabled())
  })

  it('egress 添加与移除都上报完整列表', async () => {
    const user = userEvent.setup()
    const onSaveEgress = vi.fn().mockResolvedValue(true)
    renderView({
      settings: { defaultModelId: null, allowPrivateCidrs: ['10.0.0.0/8'], version: 1, insecureAllowed: true },
      onSaveEgress,
    })

    await user.type(screen.getByLabelText('添加网段'), '192.168.1.0/24')
    await user.click(screen.getByRole('button', { name: '添加' }))
    await waitFor(() =>
      expect(onSaveEgress).toHaveBeenCalledWith(['10.0.0.0/8', '192.168.1.0/24']),
    )

    // 移除：上报的是「剩下的」，不是「被删的」。
    await user.click(screen.getByRole('button', { name: '移除该网段' }))
    await waitFor(() => expect(onSaveEgress).toHaveBeenCalledWith([]))
  })
})
