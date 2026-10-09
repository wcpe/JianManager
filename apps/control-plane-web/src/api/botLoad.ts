import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 发压容量与预检契约已回迁应用侧，双侧共用（ADR-097）；对外保留原导出名，调用点无需改动。
 */
export type { BotLoadNodeCapacity, BotLoadAllocation, BotLoadPreflightResult } from '@/lib/bot-load-types'
import type { BotLoadNodeCapacity, BotLoadPreflightResult } from '@/lib/bot-load-types'

// 命令/曲线/阈值类型已回迁应用侧（原 ADR-097 迁包已撤销）；此处转出，调用点零改动。
import type { BotLoadCommandSchedule, BotLoadProfile, BotLoadThresholds } from '@/lib/bot-load-types'
export type { BotLoadCommand, BotLoadCommandSchedule, BotLoadProfile, BotLoadThresholds } from '@/lib/bot-load-types'

// 模板类型已随命令/曲线/阈值一并迁至 `@jianmanager/ui`；此处转出，调用点零改动。
import type { BotLoadTemplate, BotLoadTemplateInput } from '@/lib/bot-load-types'
export type { BotLoadTemplate, BotLoadTemplateInput } from '@/lib/bot-load-types'

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
