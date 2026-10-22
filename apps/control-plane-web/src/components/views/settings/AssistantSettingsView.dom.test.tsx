import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
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

function wrapper({ children }: { children: ReactNode }) {
  return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
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

/** 渲染视图，回调用 spy 注入。 */
function renderView(overrides: Partial<Parameters<typeof AssistantSettingsView>[0]> = {}) {
  const onTestProvider = vi.fn().mockResolvedValue(undefined)
  const onTestModel = vi.fn().mockResolvedValue(undefined)
  const props = {
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
    onTestProvider,
    onTestModel,
    ...overrides,
  }
  render(<AssistantSettingsView {...props} />, { wrapper })
  return { onTestProvider, onTestModel }
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
