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

/** 协议值的展示名 i18n key（未知值回退为原值本身，避免显示空白）。 */
export function protocolLabelKey(protocol: string): string | null {
  return ASSISTANT_PROTOCOLS.find((p) => p.value === protocol)?.labelKey ?? null
}

/** 凭据模式值的展示名 i18n key。 */
export function credentialModeLabelKey(mode: string): string | null {
  return ASSISTANT_CREDENTIAL_MODES.find((m) => m.value === mode)?.labelKey ?? null
}

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
  /** 出站代理模式（FR-539）：direct / env / custom。 */
  proxyMode: string
  /** 代理是否带 userinfo（只回布尔，绝不回显地址与口令）。 */
  proxyHasAuth: boolean
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
  /** 思考强度（FR-541）；空串表示不指定。 */
  reasoningEffort: string
  enabled: boolean
  providerEnabled: boolean
  version: number
}

/** 普通用户可见的模型摘要。 */
export interface AssistantAvailableModel {
  id: number
  /** 供应商显示名（仅名称；不含端点与凭据）。用于「数据将发送给谁」的告知。 */
  providerName: string
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
  /** 出站代理模式（FR-539）：direct / env / custom；空串表示不改动。 */
  proxyMode?: string
  /** 自定义代理地址（含 userinfo 时整串落库且永不回显）。 */
  proxyUrl?: string
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
  /** 思考强度（FR-541）；空串表示不指定（由上游默认）。 */
  reasoningEffort: string
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


/** 供应商探测结论。ok=false 是结论而非请求失败。 */
export interface AssistantProbeResult {
  ok: boolean
  latencyMs: number
  modelCount?: number
  /**
   * 分类码（如 upstream_auth / upstream_rate_limited / egress_denied）。
   * 前端据它给出处置建议，不解析 message。
   */
  classifiedError?: string
  message?: string
}

/** 单模型测试结论。 */
export interface AssistantModelTestResult {
  ok: boolean
  latencyMs: number
  textPreview?: string
  /** 上游未报告时为 null（不是 0）。 */
  promptTokens: number | null
  outputTokens: number | null
  toolCallParsed: boolean
  classifiedError?: string
  message?: string
}

/**
 * 探测供应商连通性。
 *
 * 失败**不会**抛异常到调用方的 catch：上游失败是「结论」（ok=false + 分类码），
 * 只有供应商不存在、非管理员这类请求级问题才会 reject。
 */
export function useTestProvider() {
  return useMutation({
    mutationFn: async (id: number) => {
      const { data } = await api.post<AssistantProbeResult>(`/assistant/admin/providers/${id}/test`)
      return data
    },
  })
}

/** 用指定模型跑一次最小补全（带工具时额外验证参数完整性，不执行工具）。 */
export function useTestModel() {
  return useMutation({
    mutationFn: async ({ id, withTools }: { id: number; withTools?: boolean }) => {
      const { data } = await api.post<AssistantModelTestResult>(
        `/assistant/admin/models/${id}/test`,
        { withTools: withTools ?? false },
      )
      return data
    },
  })
}

/** 助手全局设置（默认模型 + 出站放行段，单例资源整体读写）。 */
export interface AssistantAdminSettings {
  /** 默认模型；null 表示未设置。 */
  defaultModelId: number | null
  /** 已放行的私网段（规范化后的字符串，如 "10.0.0.0/8"）。 */
  allowPrivateCidrs: string[]
  /** 自定义系统提示词（FR-541）；空串表示只用内置基线。 */
  systemPrompt: string
  /** 是否向助手暴露平台自带工具（FR-540）；未配置时后端回显 true。 */
  builtinToolsEnabled: boolean
  version: number
  /** 当前部署是否接受 http:// 明文目标（dev 环境才为真），只读回显。 */
  insecureAllowed: boolean
}

/** 保存参数的**整体替换**语义：未提供的字段等于置空。 */
export interface AssistantAdminSettingsPayload {
  defaultModelId: number | null
  allowPrivateCidrs: string[]
  /** 自定义系统提示词（整体替换语义：省略即清空）。 */
  systemPrompt: string
  /** 自带工具开关；不传表示不改动。 */
  builtinToolsEnabled?: boolean
  version: number
}

/** 读取助手全局设置。 */
export function useAssistantSettings(enabled = true) {
  return useQuery({
    queryKey: ['assistant', 'settings'],
    queryFn: async () => {
      const { data } = await api.get<AssistantAdminSettings>('/assistant/admin/settings')
      return data
    },
    enabled,
  })
}

/**
 * 保存助手全局设置（整体替换）。
 *
 * 保存后后端会在放行段变化时**立即重建出站工厂**，因此要让整组 assistant 缓存失效：
 * 否则页面还显示「上次被 egress_denied 拒绝」的旧探测结论，而实际已经放行。
 */
export function useSaveAssistantSettings() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: AssistantAdminSettingsPayload) => {
      const { data } = await api.put<AssistantAdminSettings>('/assistant/admin/settings', payload)
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['assistant'] })
    },
  })
}

// ── 外部 MCP 连接（FR-517） ──────────────────────────────────────
//
// 全部仅平台管理员；凭据只在**提交时**上行（`authSecret`），下行永远没有它，
// 只有 `hasCredential` 布尔。

/** 一条 origin 放行规则（精确 origin + CIDR）。 */
export interface MCPAllowedOrigin {
  scheme: string
  host: string
  port: number
  cidrs: string[]
}

/** 外部连接视图（凭据永不出现）。 */
export interface MCPConnection {
  id: number
  slug: string
  name: string
  endpoint: string
  authStyle: string
  authHeaderName: string
  authSource: string
  /** 只表示「有没有凭据」，不含值。 */
  hasCredential: boolean
  allowedOrigins: MCPAllowedOrigin[] | null
  enabled: boolean
  policyVersion: number
  lastProbeAt: string | null
  lastProbeOk: boolean | null
  /** 上次探测失败 → 目录可能是陈旧的。 */
  discoveryStale: boolean
  toolCount: number
  version: number
  createdAt: string
  updatedAt: string
}

/** 外部工具视图（`description` 是上游文本，**不可信**）。 */
export interface MCPTool {
  id: number
  connectionId: number
  remoteName: string
  namespacedName: string
  description: string
  inputSchema: string
  risk: string
  enabled: boolean
  schemaHash: string
  discoveredAt: string
}

/** 连接列表。 */
export function useMCPConnections() {
  return useQuery({
    queryKey: ['assistant', 'mcp', 'connections'],
    queryFn: async () => {
      const { data } = await api.get<{ items: MCPConnection[] }>('/assistant/admin/connections')
      return data.items ?? []
    },
  })
}

/** 某连接的工具清单。 */
export function useMCPTools(connectionId: number | null) {
  return useQuery({
    queryKey: ['assistant', 'mcp', 'tools', connectionId],
    queryFn: async () => {
      const { data } = await api.get<{ items: MCPTool[] }>(
        `/assistant/admin/connections/${connectionId}/tools`,
      )
      return data.items ?? []
    },
    enabled: connectionId !== null,
  })
}

/** 创建/更新连接的请求体。 */
export interface MCPConnectionPayload {
  slug: string
  name: string
  endpoint: string
  authStyle: string
  authHeaderName: string
  authSource: string
  authEnv: string
  /** 明文凭据；留空表示保留原值。 */
  authSecret: string
  clearCredential: boolean
  allowedOrigins: MCPAllowedOrigin[]
  enabled: boolean
  version?: number
}

/** 新建连接。 */
export function useCreateMCPConnection() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: MCPConnectionPayload) => {
      const { data } = await api.post<MCPConnection>('/assistant/admin/connections', payload)
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['assistant', 'mcp'] })
    },
  })
}

/** 更新连接。 */
export function useUpdateMCPConnection() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, payload }: { id: number; payload: MCPConnectionPayload }) => {
      const { data } = await api.put<MCPConnection>(`/assistant/admin/connections/${id}`, payload)
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['assistant', 'mcp'] })
    },
  })
}

/** 删除连接（连同其工具）。 */
export function useDeleteMCPConnection() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: number) => {
      await api.delete(`/assistant/admin/connections/${id}`)
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['assistant', 'mcp'] })
    },
  })
}

/** 连通性探测（不落库工具）。 */
export function useProbeMCPConnection() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: number) => {
      const { data } = await api.post<{ ok: boolean; latencyMs: number }>(
        `/assistant/admin/connections/${id}/probe`,
        {},
      )
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['assistant', 'mcp', 'connections'] })
    },
  })
}

/**
 * 拉取工具清单。
 *
 * 超额（>200）时后端回 409 `MCP_TOOLS_LIMIT` 并**整次失败、不落库**——
 * 前端必须把这个当成明确错误展示，而不是「发现到一半」。
 */
export function useDiscoverMCPTools() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: number) => {
      const { data } = await api.post<{ items: MCPTool[] }>(
        `/assistant/admin/connections/${id}/discover`,
        {},
      )
      return data.items ?? []
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['assistant', 'mcp'] })
    },
  })
}

/** 启停工具 / 设定风险分类（PATCH 语义：只改给出的字段）。 */
export function useUpdateMCPTool() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({
      connectionId,
      toolId,
      enabled,
      risk,
    }: {
      connectionId: number
      toolId: number
      enabled?: boolean
      risk?: string
    }) => {
      await api.patch(`/assistant/admin/connections/${connectionId}/tools/${toolId}`, {
        ...(enabled === undefined ? {} : { enabled }),
        ...(risk === undefined ? {} : { risk }),
      })
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['assistant', 'mcp'] })
    },
  })
}

// ── 模型自动发现（FR-538） ──────────────────────────────────────
//
// 只读候选，不自动入库：`contextWindow` / `tools` 不能从模型名推断，
// 必须由管理员确认后走 useCreateModel 落库。

export interface DiscoveredModel {
  modelId: string
  /** 本地已配置过同 ID：默认不勾选，避免重复导入。 */
  alreadyConfigured: boolean
}

export interface DiscoverModelsResult {
  discovered: DiscoveredModel[]
  total: number
  /** 上游条目超过上限时为 true（不是「上游只有这么多」）。 */
  truncated: boolean
  skipped?: Record<string, string>
}

export function useDiscoverModels() {
  return useMutation({
    mutationFn: async (providerId: number) => {
      const { data } = await api.post<DiscoverModelsResult>(
        `/assistant/admin/providers/${providerId}/discover-models`,
      )
      return data
    },
  })
}

// ── 技能（FR-541） ──────────────────────────────────────────────

export interface AssistantSkill {
  name: string
  description: string
  whenToUse?: string
  version?: string
  source?: string
  /** SKILL.md 正文：管理页要能预览「模型会被告知什么」。 */
  instructions: string
  path: string
}

export interface AssistantSkillsResult {
  skills: AssistantSkill[]
  /** 装载被跳过的技能与原因。只回「有哪些技能」会让改坏的技能凭空消失而无解释。 */
  skipped: Record<string, string>
}

export function useAssistantSkills(enabled = true) {
  return useQuery({
    queryKey: ['assistant', 'skills'],
    queryFn: async () => {
      const { data } = await api.get<AssistantSkillsResult>('/assistant/admin/skills')
      return data
    },
    enabled,
  })
}

export function useReloadAssistantSkills() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => {
      const { data } = await api.post<{ loaded: number; skipped: Record<string, string> }>(
        '/assistant/admin/skills/reload',
      )
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['assistant', 'skills'] })
    },
  })
}
