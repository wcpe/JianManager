import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * AI 助手配置 API（FR-514，见 ADR-105）。
 *
 * 两条边界写在这里，不在视图里重复：
 * - 可用模型摘要（`/assistant/models`）对所有登录用户开放，只含协议与能力，
 *   **不含**端点与凭据；
 * - 管理面（`/assistant/admin/*`）由后端按 IsPlatformAdmin 把守，前端只做入口隐藏，
 *   真正的拒绝在服务端。
 */

/** 受支持的协议（与后端 model.AssistantProtocol* 对齐）。 */
export const ASSISTANT_PROTOCOLS = [
  { value: 'openai-chat', labelKey: 'assistant.protocol.openaiChat' },
  { value: 'openai-responses', labelKey: 'assistant.protocol.openaiResponses' },
  { value: 'anthropic', labelKey: 'assistant.protocol.anthropic' },
] as const

/** 凭据模式三态。 */
export const ASSISTANT_CREDENTIAL_MODES = [
  { value: 'none', labelKey: 'assistant.credential.none' },
  { value: 'env', labelKey: 'assistant.credential.env' },
  { value: 'stored', labelKey: 'assistant.credential.stored' },
] as const

/** 供应商视图（后端永不下发明文或密文，只给 hasCredential 标志）。 */
export interface AssistantProvider {
  id: number
  name: string
  protocol: string
  baseUrl: string
  credentialMode: 'none' | 'env' | 'stored'
  /** 环境变量名（不是值）——后端有意回显，便于管理员知道引用了哪个。 */
  credentialEnv?: string
  /** 是否已配置可用凭据。 */
  hasCredential: boolean
  enabled: boolean
  version: number
  createdAt: string
  updatedAt: string
}

/** 模型视图（含供应商名与协议回填，前端不再为显示一个名字拉全量供应商）。 */
export interface AssistantModel {
  id: number
  providerId: number
  providerName: string
  protocol: string
  modelId: string
  displayName: string
  streaming: boolean
  tools: boolean
  contextWindow: number | null
  maxOutputTokens: number | null
  enabled: boolean
  providerEnabled: boolean
  version: number
}

/** 普通用户可见的模型摘要。 */
export interface AssistantAvailableModel {
  id: number
  modelId: string
  displayName: string
  protocol: string
  streaming: boolean
  tools: boolean
}

/** 供应商保存载荷。 */
export interface ProviderPayload {
  name: string
  protocol: string
  baseUrl: string
  enabled: boolean
  credentialMode: 'none' | 'env' | 'stored'
  credentialEnv?: string
  /** 空串表示保留原密文（编辑态）或必填（创建态，由后端判定）。 */
  credentialSecret?: string
  clearCredential?: boolean
  version?: number
}

/** 模型保存载荷。 */
export interface ModelPayload {
  providerId: number
  modelId: string
  displayName: string
  streaming: boolean
  tools: boolean
  contextWindow: number | null
  maxOutputTokens: number | null
  enabled: boolean
  version?: number
}

/** 可用模型列表（所有登录用户）。 */
export function useAssistantModels() {
  return useQuery({
    queryKey: ['assistant', 'models'],
    queryFn: async () => {
      const { data } = await api.get<{ items: AssistantAvailableModel[]; defaultModelId: number | null }>(
        '/assistant/models',
      )
      return data
    },
  })
}

/** 管理面供应商列表。 */
export function useAssistantProviders(enabled = true) {
  return useQuery({
    queryKey: ['assistant', 'admin', 'providers'],
    queryFn: async () => {
      const { data } = await api.get<{ items: AssistantProvider[] }>('/assistant/admin/providers')
      return data.items ?? []
    },
    enabled,
  })
}

/** 管理面模型列表。 */
export function useAssistantAdminModels(enabled = true) {
  return useQuery({
    queryKey: ['assistant', 'admin', 'models'],
    queryFn: async () => {
      const { data } = await api.get<{ items: AssistantModel[] }>('/assistant/admin/models')
      return data.items ?? []
    },
    enabled,
  })
}

/** 管理面写操作统一失效这几个键。 */
function useInvalidateAssistant() {
  const qc = useQueryClient()
  return () => {
    void qc.invalidateQueries({ queryKey: ['assistant'] })
  }
}

/** 创建供应商。 */
export function useCreateProvider() {
  const invalidate = useInvalidateAssistant()
  return useMutation({
    mutationFn: async (payload: ProviderPayload) => {
      const { data } = await api.post<AssistantProvider>('/assistant/admin/providers', payload)
      return data
    },
    onSuccess: invalidate,
  })
}

/** 更新供应商（带 version 乐观并发）。 */
export function useUpdateProvider() {
  const invalidate = useInvalidateAssistant()
  return useMutation({
    mutationFn: async ({ id, payload }: { id: number; payload: ProviderPayload }) => {
      const { data } = await api.put<AssistantProvider>(`/assistant/admin/providers/${id}`, payload)
      return data
    },
    onSuccess: invalidate,
  })
}

/** 删除供应商。 */
export function useDeleteProvider() {
  const invalidate = useInvalidateAssistant()
  return useMutation({
    mutationFn: async (id: number) => {
      await api.delete(`/assistant/admin/providers/${id}`)
    },
    onSuccess: invalidate,
  })
}

/** 创建模型。 */
export function useCreateModel() {
  const invalidate = useInvalidateAssistant()
  return useMutation({
    mutationFn: async (payload: ModelPayload) => {
      const { data } = await api.post<AssistantModel>('/assistant/admin/models', payload)
      return data
    },
    onSuccess: invalidate,
  })
}

/** 更新模型。 */
export function useUpdateModel() {
  const invalidate = useInvalidateAssistant()
  return useMutation({
    mutationFn: async ({ id, payload }: { id: number; payload: ModelPayload }) => {
      const { data } = await api.put<AssistantModel>(`/assistant/admin/models/${id}`, payload)
      return data
    },
    onSuccess: invalidate,
  })
}

/** 删除模型。 */
export function useDeleteModel() {
  const invalidate = useInvalidateAssistant()
  return useMutation({
    mutationFn: async (id: number) => {
      await api.delete(`/assistant/admin/models/${id}`)
    },
    onSuccess: invalidate,
  })
}

/** 设置默认模型（null 表示清空）。 */
export function useSetDefaultModel() {
  const invalidate = useInvalidateAssistant()
  return useMutation({
    mutationFn: async (modelId: number | null) => {
      await api.put('/assistant/admin/settings/default-model', { modelId })
    },
    onSuccess: invalidate,
  })
}
