import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 发压节点容量（GET /bots/load-nodes）。 */
export interface BotLoadNodeCapacity {
  nodeId: number
  nodeUuid: string
  nodeName: string
  online: boolean
  tunnelConnected: boolean
  botWorkerReady: boolean
  legacy: boolean
  maxBots: number
  activeBots: number
  reservedBots: number
  availableBots: number
  capacityGeneration: number
  workerEpoch?: string
  botWorkerVersion?: string
  runtimeSource?: string
  rssBytes?: number
  eventLoopP95Ms?: number
  lastHeartbeatAt?: string
  unavailableReason?: string
}

/** 预检分片分配。 */
export interface BotLoadAllocation {
  batchId: string
  ordinal: number
  executorNodeId: number
  executorNodeUuid: string
  executorNodeName: string
  plannedCount: number
  connectStartAt: string
  connectIntervalMs: number
  idempotencyKey: string
}

/** 单条命令声明。 */
export interface BotLoadCommand {
  id: string
  atMs: number
  command: string
  repeat?: { intervalMs: number; count: number }
}

/** 命令编排计划。 */
export interface BotLoadCommandSchedule {
  commands: BotLoadCommand[]
  durationMs: number
  jitterMs?: number
}

/** 负载曲线。 */
export type BotLoadProfile =
  | { type: 'stable'; targetBots: number; rampUpSeconds: number; durationSeconds: number }
  | { type: 'step'; stages: Array<{ targetBots: number; holdSeconds: number }>; stopOnThresholdFailure: boolean }
  | {
      type: 'spike'
      targetBots: number
      connectWindowSeconds: number
      barrier?: { key: string; releaseWindowMs: number }
      holdSeconds: number
    }

/** 判定阈值。 */
export interface BotLoadThresholds {
  minOnlineRate: number
  minCommandSentRate: number
  minScheduleCompletionRate: number
  minWorkerHealthRate: number
  minBarrierArrivalRate: number
  maxScheduleLagP95Ms: number
  maxProcessCrashes: number
  safety?: {
    maxExecutorMemoryRate: number
    maxEventLoopP95Ms: number
    sustainSeconds: number
  }
  legacy?: {
    enabled: boolean
    minTps?: number
    maxMsptP95?: number
    requireBusinessObservation?: boolean
  }
}

/** 压测模板。 */
export interface BotLoadTemplate {
  id: number
  uuid: string
  name: string
  description: string
  commandSchedule: BotLoadCommandSchedule
  loadProfile: BotLoadProfile
  thresholds: BotLoadThresholds
  tags: string[]
  createdBy: number
  createdAt: string
  updatedAt: string
}

/** 创建/更新模板请求体。 */
export interface BotLoadTemplateInput {
  name: string
  description: string
  commandSchedule: BotLoadCommandSchedule
  loadProfile: BotLoadProfile
  thresholds: BotLoadThresholds
  tags: string[]
}

export interface BotLoadTemplateListParams {
  page?: number
  pageSize?: number
  q?: string
  tag?: string
  ownerId?: number
}

export interface BotLoadTemplateListResponse {
  items: BotLoadTemplate[]
  total: number
  page: number
  pageSize: number
}

export interface BotLoadNodesResponse {
  items: BotLoadNodeCapacity[]
  totalCapacity: number
  availableCapacity: number
  updatedAt: string
}

/** 预检结果（当前契约 + 可选 planned 扩展字段）。 */
export interface BotLoadPreflightResult {
  runId: number
  runUuid: string
  ready: boolean
  planToken?: string
  expiresAt?: string
  targetBots: number
  totalAvailable: number
  allocations: BotLoadAllocation[]
  nodeCapacities: BotLoadNodeCapacity[]
  probe: {
    required: false
    connected: boolean
    instanceId: number
    instanceUuid: string
    message?: string
  }
  estimatedDurationSeconds: number
  warnings: Array<{ code: string; message: string }>
  blockers: Array<{ code: string; message: string; nodeId?: number }>
  instanceId?: number
  commandSchedule?: BotLoadCommandSchedule
}

export interface CreateBotLoadRunRequest {
  instanceId: number
  count: number
  name: string
  namePrefix: string
  config: { server: string; port: number; auth: 'offline'; version?: string }
  executorNodeIds?: number[]
  loadProfile?: BotLoadProfile
  thresholds?: BotLoadThresholds
  commandSchedule?: BotLoadCommandSchedule
}

export interface CreateBotLoadRunFromTemplateRequest {
  instanceId: number
  name: string
  namePrefix: string
  config: { server: string; port: number; auth: 'offline'; version?: string }
  executorNodeIds?: number[]
  commandScheduleOverride?: BotLoadCommandSchedule | null
  loadProfileOverride?: BotLoadProfile | null
  thresholdsOverride?: BotLoadThresholds | null
}

/** 运行摘要/详情（向导启动后跳转用，字段按契约最小集）。 */
export interface BotLoadRun {
  id: number
  uuid: string
  instanceId: number
  name: string
  namePrefix: string
  count: number
  status: string
  runState?: string
  schemaVersion?: number
  targetBots?: number
  createdAt: string
  updatedAt: string
}

const TEMPLATE_KEY = ['bots', 'load-templates'] as const
const NODES_KEY = ['bots', 'load-nodes'] as const
const RUNS_KEY = ['bots', 'stress-sessions'] as const

/** 分页查询压测模板。 */
export function useBotLoadTemplates(params?: BotLoadTemplateListParams) {
  return useQuery({
    queryKey: [...TEMPLATE_KEY, params],
    queryFn: async () => {
      const { data } = await api.get<BotLoadTemplateListResponse>('/bots/load-templates', { params })
      return data
    },
  })
}

/** 查询单个模板。 */
export function useBotLoadTemplate(id: number | null) {
  return useQuery({
    queryKey: [...TEMPLATE_KEY, id],
    queryFn: async () => {
      const { data } = await api.get<BotLoadTemplate>(`/bots/load-templates/${id}`)
      return data
    },
    enabled: id !== null,
  })
}

/** 创建模板。 */
export function useCreateBotLoadTemplate() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: BotLoadTemplateInput) => {
      const { data } = await api.post<BotLoadTemplate>('/bots/load-templates', payload)
      return data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: TEMPLATE_KEY }),
  })
}

/** 全量更新模板。 */
export function useUpdateBotLoadTemplate() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, payload }: { id: number; payload: BotLoadTemplateInput }) => {
      const { data } = await api.put<BotLoadTemplate>(`/bots/load-templates/${id}`, payload)
      return data
    },
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: TEMPLATE_KEY })
      qc.invalidateQueries({ queryKey: [...TEMPLATE_KEY, vars.id] })
    },
  })
}

/** 删除模板。 */
export function useDeleteBotLoadTemplate() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: number) => {
      await api.delete(`/bots/load-templates/${id}`)
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: TEMPLATE_KEY }),
  })
}

/**
 * 查询发压节点容量。
 * 仅在向导打开时启用；refetchInterval 5s 由调用方控制 enabled。
 */
export function useBotLoadNodes(instanceId: number | null, enabled = true) {
  return useQuery({
    queryKey: [...NODES_KEY, instanceId],
    queryFn: async () => {
      const { data } = await api.get<BotLoadNodesResponse>('/bots/load-nodes', {
        params: { instanceId },
      })
      return data
    },
    enabled: enabled && instanceId !== null && instanceId > 0,
    refetchInterval: enabled ? 5000 : false,
  })
}

/** 创建运行（直接提交 commandSchedule）。 */
export function useCreateBotLoadRun() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: CreateBotLoadRunRequest) => {
      const { data } = await api.post<BotLoadRun>('/bots/stress-sessions', payload)
      return data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: RUNS_KEY }),
  })
}

/** 从模板创建运行。 */
export function useCreateBotLoadRunFromTemplate() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, payload }: { id: number; payload: CreateBotLoadRunFromTemplateRequest }) => {
      const { data } = await api.post<BotLoadRun>(`/bots/load-templates/${id}/runs`, payload)
      return data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: RUNS_KEY }),
  })
}

/** 预检运行。 */
export function usePreflightBotLoadRun() {
  return useMutation({
    mutationFn: async ({
      id,
      executorNodeIds,
      connectRatePerSecondPerNode,
    }: {
      id: number
      executorNodeIds?: number[]
      connectRatePerSecondPerNode?: number
    }) => {
      const { data } = await api.post<BotLoadPreflightResult>(`/bots/stress-sessions/${id}/preflight`, {
        executorNodeIds,
        connectRatePerSecondPerNode,
      })
      return data
    },
  })
}

/** 用 planToken 启动运行。 */
export function useStartBotLoadRun() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, planToken }: { id: number; planToken: string }) => {
      const { data } = await api.post<BotLoadRun>(`/bots/stress-sessions/${id}/start`, { planToken })
      return data
    },
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: RUNS_KEY })
      qc.invalidateQueries({ queryKey: ['bots'] })
      qc.invalidateQueries({ queryKey: [...RUNS_KEY, vars.id] })
    },
  })
}
